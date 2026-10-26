package automation

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
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

func routineCaller(ctx context.Context, q *sqlc.Queries, c engine.TaskContext) (sqlc.Conversation, sqlc.Submission, error) {
	conv, err := q.GetConversationByExecution(ctx, sqlc.GetConversationByExecutionParams{AgentID: c.AgentID, ExecutionID: sql.NullInt64{Int64: c.ConversationID, Valid: true}})
	if err != nil {
		return conv, sqlc.Submission{}, err
	}
	if !chat.Writable(conv.ChannelKey) {
		return conv, sqlc.Submission{}, fmt.Errorf("%w: routines require main or a writable Chat", ErrInvalid)
	}
	source, err := q.GetSubmission(ctx, c.SourceRequestID)
	if err != nil {
		return conv, source, err
	}
	if source.ConversationID != conv.ID || source.Kind != "message" || source.Origin == "automation" || source.Origin == "task" || source.Origin == "dream" {
		return conv, source, fmt.Errorf("%w: routine tools require a user input in this conversation", ErrInvalid)
	}
	return conv, source, nil
}

func routineView(a Automation) engine.RoutineInfo {
	out := engine.RoutineInfo{ID: a.ID, Name: a.Name, Prompt: a.Prompt, Schedule: engine.RoutineSchedule(a.Schedule), TimeZone: a.TimeZone, Session: a.Session, Enabled: a.Enabled, Cwd: a.Cwd, NextRuns: a.NextRuns, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt}
	if a.LastRun != nil {
		out.LastRun = new(engine.RoutineRun(*a.LastRun))
	}
	return out
}

func (s *ToolService) List(ctx context.Context, c engine.TaskContext) ([]engine.RoutineInfo, error) {
	if _, _, err := routineCaller(ctx, s.service.q, c); err != nil {
		return nil, err
	}
	rows, err := s.service.List(ctx, c.AgentID)
	if err != nil {
		return nil, err
	}
	out := make([]engine.RoutineInfo, len(rows))
	for i, a := range rows {
		out[i] = routineView(a)
		out[i].Prompt = ""
	}
	return out, nil
}

func (s *ToolService) Get(ctx context.Context, c engine.TaskContext, id string) (engine.RoutineInfo, error) {
	if _, _, err := routineCaller(ctx, s.service.q, c); err != nil {
		return engine.RoutineInfo{}, err
	}
	a, err := s.service.Get(ctx, c.AgentID, id)
	if err != nil {
		return engine.RoutineInfo{}, err
	}
	runs, err := s.service.Runs(ctx, c.AgentID, id, 10)
	if err != nil {
		return engine.RoutineInfo{}, err
	}
	out := routineView(a)
	out.Runs = make([]engine.RoutineRun, len(runs))
	for i, r := range runs {
		out.Runs[i] = engine.RoutineRun(r)
	}
	return out, nil
}

// transactionPoster reuses normal run/input storage within the operation receipt transaction.
type transactionPoster struct{ q *sqlc.Queries }

func (p transactionPoster) EnqueueAutomation(ctx context.Context, in chat.AutomationInput) (int64, bool, error) {
	return chat.StoreAutomationInput(ctx, p.q, in)
}

// mutate returns the committed result even when replay occurs after input settlement.
// A new mutation must still have an active source; the writer lock orders it with Stop.
func (s *ToolService) mutate(ctx context.Context, c engine.TaskContext, action string, invoke func(*Service, sqlc.Conversation) (any, error), out any) error {
	if c.Key == "" || !strings.HasPrefix(c.Key, c.AgentID+"/") {
		return invalid("a durable tool key is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.service.q.WithTx(tx)
	if err = q.LockRoutineTools(ctx); err != nil {
		return err
	}
	conv, source, err := routineCaller(ctx, q, c)
	if err != nil {
		return err
	}
	prior, err := q.GetRoutineToolOperation(ctx, c.Key)
	if err == nil {
		if prior.AgentID != c.AgentID || prior.ConversationID != conv.ID || prior.SourceRequestID != source.ID || prior.Action != action {
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
	local := New(q, transactionPoster{q}, s.service.now)
	result, err := invoke(local, conv)
	if err != nil {
		return err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if err = q.CreateRoutineToolOperation(ctx, sqlc.CreateRoutineToolOperationParams{Key: c.Key, AgentID: c.AgentID, ConversationID: conv.ID, SourceRequestID: source.ID, Action: action, Result: string(data)}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if run, ok := result.(engine.RoutineRun); ok && run.ChatID != 0 {
		s.wake(run.ChatID)
	}
	return json.Unmarshal(data, out)
}

func applyPatch(in *Input, p engine.RoutinePatch) {
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
	}
	if p.Enabled != nil {
		in.Enabled = *p.Enabled
	}
	if p.Cwd != nil {
		in.Cwd = *p.Cwd
	}
}

func (s *ToolService) Create(ctx context.Context, c engine.TaskContext, p engine.RoutinePatch) (engine.RoutineInfo, error) {
	var out engine.RoutineInfo
	err := s.mutate(ctx, c, "create", func(local *Service, conv sqlc.Conversation) (any, error) {
		if p.ID != "" || p.Name == nil || p.Prompt == nil || p.Schedule == nil || p.TimeZone == nil {
			return nil, invalid("name, prompt, schedule and explicit timeZone are required")
		}
		in := Input{Enabled: true, Session: "new", Cwd: conv.Cwd}
		applyPatch(&in, p)
		a, err := local.Create(ctx, c.AgentID, in)
		return routineView(a), err
	}, &out)
	return out, err
}

func (s *ToolService) Update(ctx context.Context, c engine.TaskContext, p engine.RoutinePatch) (engine.RoutineInfo, error) {
	var out engine.RoutineInfo
	err := s.mutate(ctx, c, "update", func(local *Service, _ sqlc.Conversation) (any, error) {
		a, err := local.Get(ctx, c.AgentID, p.ID)
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
			row, err := local.get(ctx, c.AgentID, p.ID)
			if err != nil {
				return nil, err
			}
			row.Cwd = cleanIn.Cwd
			row.Name = cleanIn.Name
			row.Prompt = cleanIn.Prompt
			row.Session = cleanIn.Session
			row.UpdatedAt = local.now().UnixMilli()
			_, err = local.q.UpdateAutomation(ctx, sqlc.UpdateAutomationParams{Cwd: row.Cwd, Name: row.Name, Prompt: row.Prompt, Schedule: row.Schedule, TimeZone: row.TimeZone, Session: row.Session, Enabled: row.Enabled, NextDueAt: row.NextDueAt, UpdatedAt: row.UpdatedAt, ID: row.ID, AgentID: row.AgentID})
			if err != nil {
				return nil, err
			}
			a, err = local.view(ctx, row)
			return routineView(a), err
		}
		a, err = local.Update(ctx, c.AgentID, p.ID, cleanIn)
		return routineView(a), err
	}, &out)
	return out, err
}

func (s *ToolService) Run(ctx context.Context, c engine.TaskContext, id string) (engine.RoutineRun, error) {
	var out engine.RoutineRun
	err := s.mutate(ctx, c, "run", func(local *Service, _ sqlc.Conversation) (any, error) {
		r, err := local.RunNow(ctx, c.AgentID, id)
		return engine.RoutineRun(r), err
	}, &out)
	return out, err
}
