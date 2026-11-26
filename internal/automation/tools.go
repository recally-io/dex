package automation

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/session"
)

// ToolService owns source authority and atomic receipts for durable tool replay.
type ToolService struct {
	db      *sql.DB
	service *Service
	wake    func(int64)
}

func NewToolService(db *sql.DB, service *Service, wake func(int64)) *ToolService {
	return &ToolService{db: db, service: service, wake: wake}
}

func toolCaller(ctx context.Context, q *sqlc.Queries, c engine.TaskContext) (sqlc.Session, sqlc.Submission, error) {
	conv, err := q.GetSessionByExecution(ctx, sqlc.GetSessionByExecutionParams{BotID: c.BotID, ExecutionID: sql.NullInt64{Int64: c.ConversationID, Valid: true}})
	if err != nil {
		return conv, sqlc.Submission{}, err
	}
	if !session.Writable(conv.ChannelKey) {
		return conv, sqlc.Submission{}, fmt.Errorf("%w: automations require Main or a writable session", ErrInvalid)
	}
	source, err := q.GetSubmission(ctx, c.SourceRequestID)
	if err != nil {
		return conv, source, err
	}
	if source.SessionID != conv.ID || source.Kind != "message" || source.Origin == "automation" || source.Origin == "task" || source.Origin == "dream" {
		return conv, source, fmt.Errorf("%w: automation tools require a user input in this conversation", ErrInvalid)
	}
	return conv, source, nil
}

func toolRun(r Run) ToolRun {
	return ToolRun{Outcome: r.Outcome, Summary: r.Summary, ChangeKey: r.ChangeKey, Duplicate: r.Duplicate, ID: r.ID, DueAt: r.DueAt, CreatedAt: r.CreatedAt, Status: r.Status, Reason: r.Reason, SessionID: r.SessionID}
}

func toolView(a Automation) ToolInfo {
	out := ToolInfo{Notification: a.Notification, ID: a.ID, Name: a.Name, Prompt: a.Prompt, Schedule: ToolSchedule(a.Schedule), TimeZone: a.TimeZone, Session: a.Session, TargetSessionID: a.TargetSessionID, Enabled: a.Enabled, Cwd: a.Cwd, RunnerID: a.RunnerID, NextRuns: a.NextRuns, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt}
	if a.LastRun != nil {
		out.LastRun = new(toolRun(*a.LastRun))
	}
	return out
}

func (s *ToolService) List(ctx context.Context, c engine.TaskContext) ([]ToolInfo, error) {
	if _, _, err := toolCaller(ctx, s.service.q, c); err != nil {
		return nil, err
	}
	rows, err := s.service.List(ctx, c.BotID)
	if err != nil {
		return nil, err
	}
	out := make([]ToolInfo, len(rows))
	for i, a := range rows {
		out[i] = toolView(a)
		out[i].Prompt = ""
	}
	return out, nil
}

func (s *ToolService) Get(ctx context.Context, c engine.TaskContext, id string) (ToolInfo, error) {
	if _, _, err := toolCaller(ctx, s.service.q, c); err != nil {
		return ToolInfo{}, err
	}
	a, err := s.service.Get(ctx, c.BotID, id)
	if err != nil {
		return ToolInfo{}, err
	}
	runs, err := s.service.Runs(ctx, c.BotID, id, 10)
	if err != nil {
		return ToolInfo{}, err
	}
	out := toolView(a)
	out.Runs = make([]ToolRun, len(runs))
	for i, r := range runs {
		out.Runs[i] = toolRun(r)
	}
	return out, nil
}

// mutate returns the committed result even when replay occurs after input settlement.
// A new mutation must still have an active source; the writer lock orders it with Stop.
func (s *ToolService) mutate(ctx context.Context, c engine.TaskContext, action string, invoke func(*Service, sqlc.Session) (any, error), out any) error {
	if c.Key == "" || !strings.HasPrefix(c.Key, c.BotID+"/") {
		return invalid("a durable tool key is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.service.q.WithTx(tx)
	if err = q.LockAutomationTools(ctx); err != nil {
		return err
	}
	conv, source, err := toolCaller(ctx, q, c)
	if err != nil {
		return err
	}
	prior, err := q.GetAutomationToolOperation(ctx, c.Key)
	if err == nil {
		if prior.BotID != c.BotID || prior.SessionID != conv.ID || prior.SourceRequestID != source.ID || prior.Action != action {
			return invalid("tool key belongs to another operation")
		}
		return json.Unmarshal([]byte(prior.Result), out)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if source.Status != "pending" {
		return invalid("the source run is no longer active")
	}
	local := &Service{
		q: q,
		enqueue: func(ctx context.Context, in session.AutomationInput) (session.AutomationInputResult, error) {
			return session.StoreAutomationInput(ctx, q, in)
		},
		now: s.service.now,
	}
	result, err := invoke(local, conv)
	if err != nil {
		return err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if err = q.CreateAutomationToolOperation(ctx, sqlc.CreateAutomationToolOperationParams{Key: c.Key, BotID: c.BotID, SessionID: conv.ID, SourceRequestID: source.ID, Action: action, Result: string(data)}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if run, ok := result.(ToolRun); ok {
		if run.SessionID != 0 {
			s.wake(run.SessionID)
		} else if s.service.wakeOutbox != nil {
			s.service.wakeOutbox(ctx, run.ID)
		}
	}
	return json.Unmarshal(data, out)
}

func applyPatch(in *Input, p ToolPatch) {
	if p.Notification != nil {
		in.Notification = p.Notification
	}
	if p.Name != nil {
		in.Name = *p.Name
	}
	if p.Prompt != nil {
		in.Prompt = *p.Prompt
	}
	if p.Schedule != nil {
		in.Schedule = Schedule(*p.Schedule)
	}
	if p.TimeZone != nil {
		in.TimeZone = *p.TimeZone
	}
	if p.Session != nil {
		in.Session = *p.Session
		if in.Session != "session" {
			in.TargetSessionID = 0
		}
	}
	if p.TargetSessionID != nil {
		in.TargetSessionID = *p.TargetSessionID
	}
	if p.Enabled != nil {
		in.Enabled = *p.Enabled
	}
	// A change of Runner clears the working directory.
	if p.RunnerID != nil {
		if *p.RunnerID != in.RunnerID {
			in.Cwd = ""
		}
		in.RunnerID = *p.RunnerID
	}
	if p.Cwd != nil {
		in.Cwd = *p.Cwd
	}
}

func (s *ToolService) Create(ctx context.Context, c engine.TaskContext, p ToolPatch) (ToolInfo, error) {
	var out ToolInfo
	err := s.mutate(ctx, c, "create", func(local *Service, conv sqlc.Session) (any, error) {
		if p.ID != "" || p.Name == nil || p.Prompt == nil || p.Schedule == nil || p.TimeZone == nil {
			return nil, invalid("name, prompt, schedule and explicit timeZone are required")
		}
		// The Automation uses the Session's Runner and working directory.
		in := Input{Enabled: true, Session: "new", Cwd: conv.Cwd, RunnerID: conv.RunnerID, Notification: &Notification{Mode: "default"}}
		applyPatch(&in, p)
		a, err := local.Create(ctx, c.BotID, in)
		return toolView(a), err
	}, &out)
	return out, err
}

func (s *ToolService) Update(ctx context.Context, c engine.TaskContext, p ToolPatch) (ToolInfo, error) {
	var out ToolInfo
	err := s.mutate(ctx, c, "update", func(local *Service, _ sqlc.Session) (any, error) {
		a, err := local.Get(ctx, c.BotID, p.ID)
		if err != nil {
			return nil, err
		}
		in := a.Input
		applyPatch(&in, p)
		// A non-scheduling edit must preserve the current cursor.
		// Explicitly setting an already equal schedule is also a no-op for timing.
		cleanIn, _, err := clean(in)
		if err != nil {
			return nil, err
		}
		before, err := json.Marshal(a.Schedule)
		if err != nil {
			return nil, err
		}
		after, err := json.Marshal(cleanIn.Schedule)
		if err != nil {
			return nil, err
		}
		if string(before) == string(after) && a.TimeZone == cleanIn.TimeZone && a.Enabled == cleanIn.Enabled {
			row, err := local.get(ctx, c.BotID, p.ID)
			if err != nil {
				return nil, err
			}
			if err := local.applyNotification(ctx, &row, cleanIn.Notification); err != nil {
				return nil, err
			}
			if err := local.checkRunner(ctx, cleanIn.RunnerID, row.RunnerID); err != nil {
				return nil, err
			}
			if err := local.checkTarget(ctx, c.BotID, cleanIn); err != nil {
				return nil, err
			}
			row.Cwd, row.RunnerID, row.TargetSessionID = cleanIn.Cwd, cleanIn.RunnerID, targetID(cleanIn)
			row.Name = cleanIn.Name
			row.Prompt = cleanIn.Prompt
			row.Session = cleanIn.Session
			row.UpdatedAt = local.now().UnixMilli()
			_, err = local.q.UpdateAutomation(ctx, sqlc.UpdateAutomationParams{Cwd: row.Cwd, Name: row.Name, Prompt: row.Prompt, Schedule: row.Schedule, TimeZone: row.TimeZone, Session: row.Session, Enabled: row.Enabled, NextDueAt: row.NextDueAt, NotificationRoute: row.NotificationRoute, NotificationTargetID: row.NotificationTargetID, RunnerID: row.RunnerID, TargetSessionID: row.TargetSessionID, UpdatedAt: row.UpdatedAt, ID: row.ID, BotID: row.BotID})
			if err != nil {
				return nil, err
			}
			a, err = local.view(ctx, row)
			return toolView(a), err
		}
		a, err = local.Update(ctx, c.BotID, p.ID, cleanIn)
		return toolView(a), err
	}, &out)
	return out, err
}

func (s *ToolService) Run(ctx context.Context, c engine.TaskContext, id string) (ToolRun, error) {
	var out ToolRun
	// The Runner check does network I/O. The tool does it before the transaction
	// and the input transaction compares the Runner again. The transaction checks
	// if the Automation exists so a retry of a stored call can still replay.
	var pre *Target
	var preRow sqlc.Automation
	if row, err := s.service.get(ctx, c.BotID, id); err == nil {
		t, err := s.service.Preflight(ctx, row)
		if err != nil {
			return out, err
		}
		pre, preRow = &t, row
	}
	err := s.mutate(ctx, c, "run", func(local *Service, _ sqlc.Session) (any, error) {
		row, err := local.get(ctx, c.BotID, id)
		if err != nil {
			return nil, err
		}
		// The writer can confirm the Runner, but two sessions can share it. Do not
		// redirect a tool run to a replacement target selected during preflight.
		if row.Session == "session" && pre != nil && row.TargetSessionID.Valid && (preRow.Session != row.Session || preRow.TargetSessionID != row.TargetSessionID) {
			return nil, fmt.Errorf("%w: the automation target changed during its check; run it again", engine.ErrBusy)
		}
		r, err := local.admit(ctx, row, sql.NullInt64{}, "", pre)
		return toolRun(r), err
	}, &out)
	return out, err
}
