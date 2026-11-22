// Package automation runs prompts on a schedule. The web UI manages automations, and later an agent
// tool will.
//
// Each automation stores its next scheduled time (next_due_at). Check runs the automations whose
// time has come; the scheduler calls it every minute, so the schedule lives in the database, not
// in the scheduling library. When dex did not run at a scheduled time, Check runs only the newest
// missed time, and only when it is at most an hour old; it skips the others.
package automation

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/recally-io/dex/internal/db"

	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/execenv"
)

// Channel starts the channel key of an automation's chats: automation:<id> for an automation
// that reuses its chat, and automation:<id>:<run id> for each run of the other automations.
const Channel = "automation:"

// A missed time older than this is skipped.
const grace = time.Hour

// The longest prompt, in characters, as for a message.
const maxPrompt = 32000

// ErrInvalid wraps the reason why an automation is not valid.
var ErrInvalid = errors.New("invalid automation")

// ErrNotFound means that the automation does not exist.
var ErrNotFound = errors.New("automation not found")

// Input is what the user sets.
type Input struct {
	Notification         *engine.RoutineNotification `json:"notification,omitempty"`
	Cwd                  string                      `json:"cwd"`
	Name                 string                      `json:"name"`
	Prompt               string                      `json:"prompt"`
	Schedule             Schedule                    `json:"schedule"`
	TimeZone             string                      `json:"timeZone"`
	Session              string                      `json:"session"` // new, reuse, or session
	TargetConversationID int64                       `json:"targetConversationId,omitzero"`
	Enabled              bool                        `json:"enabled"`
	// EnvironmentID is local or an SSH environment ID. Empty follows the environment of the bot.
	EnvironmentID string `json:"environmentId"`
}

// Automation is an automation with its next times and its last run.
type Automation struct {
	Input
	ID        string  `json:"id"`
	NextRuns  []int64 `json:"nextRuns"` // at most 3, Unix milliseconds
	LastRun   *Run    `json:"lastRun,omitempty"`
	CreatedAt int64   `json:"createdAt"`
	UpdatedAt int64   `json:"updatedAt"`
}

// Run is one run of an automation.
type Run struct {
	AdmittedAt       *int64 `json:"admittedAt,omitzero"`
	SettledAt        *int64 `json:"settledAt,omitzero"`
	Outcome          string `json:"outcome"`
	Summary          string `json:"summary"`
	ChangeKey        string `json:"changeKey,omitempty"`
	Duplicate        bool   `json:"duplicate"`
	ID               string `json:"id"`
	DueAt            int64  `json:"dueAt,omitzero"` // 0 for a run that the user started
	CreatedAt        int64  `json:"createdAt"`
	Status           string `json:"status"` // running, done, failed, or skipped
	Reason           string `json:"reason,omitempty"`
	NotificationNote string `json:"notificationNote,omitempty"`
	ChatID           int64  `json:"chatId,omitzero"` // 0 when the run has no message
	// EnvironmentID and Cwd are where the run started: the effective environment, never empty
	// for a run with a message.
	EnvironmentID string `json:"environmentId,omitempty"`
	Cwd           string `json:"cwd,omitempty"`
}

// Service manages automations for all agents.
type Service struct {
	q          *sqlc.Queries
	enqueue    func(context.Context, chat.AutomationInput) (chat.AutomationInputResult, error)
	db         *sql.DB
	now        func() time.Time
	wakeOutbox func(context.Context, string)
	// probe opens an SSH environment of a run before its input; nil skips the check.
	probe  func(ctx context.Context, agentID, environmentID string) error
	status func(ctx context.Context, agentID, environmentID string) string
}

// ConfigureProbe sets the check that an SSH environment is reachable before a run. A run on an
// unreachable host is skipped, so it never starts a chat that cannot use its machine.
func (s *Service) ConfigureProbe(probe func(ctx context.Context, agentID, environmentID string) error) {
	s.probe = probe
}

// ConfigureEnvironmentStatus lets a bound Routine detect an offline target without opening it.
func (s *Service) ConfigureEnvironmentStatus(status func(context.Context, string, string) string) {
	s.status = status
}

// New returns a Service. Call Check every minute.
func New(conn *sql.DB, chats *chat.Service, now func() time.Time) *Service {
	s := &Service{db: conn, q: sqlc.New(conn), now: now}
	if chats != nil {
		s.enqueue = chats.EnqueueAutomation
		s.wakeOutbox = chats.WakeRoutineOutbox
	}
	return s
}

// ChatAutomation returns the ID of the automation that started the chat with channelKey.
func ChatAutomation(channelKey string) (string, bool) {
	rest, ok := strings.CutPrefix(channelKey, Channel)
	if !ok {
		return "", false
	}
	id, _, _ := strings.Cut(rest, ":")
	return id, true
}

func invalid(reason string) error { return fmt.Errorf("%w: %s", ErrInvalid, reason) }

// clean validates in and returns it with a clean schedule, and its time zone.
func clean(in Input) (Input, *time.Location, error) {
	switch in.Session {
	case "session":
		if in.TargetConversationID <= 0 {
			return in, nil, invalid("choose a target session")
		}
		in.Cwd, in.EnvironmentID = "", ""
	case "new", "reuse":
		if in.TargetConversationID != 0 {
			return in, nil, invalid("only session mode accepts a target session")
		}
		cwd, err := chat.ProjectPath(in.Cwd)
		if err != nil {
			return in, nil, fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		in.Cwd = cwd
	default:
		return in, nil, invalid("the session must be new, reuse, or session")
	}
	if n := in.Notification; n != nil {
		if (n.Mode != "none" && n.Mode != "default" && n.Mode != "target") || (n.Mode == "target") != (n.TargetID != "") {
			return in, nil, invalid("invalid notification route")
		}
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Prompt = strings.TrimSpace(in.Prompt)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 100 {
		return in, nil, invalid("the name must have 1 to 100 characters")
	}
	if in.Prompt == "" || utf8.RuneCountInString(in.Prompt) > maxPrompt {
		return in, nil, invalid("the prompt must have 1 to 32000 characters")
	}
	loc, err := location(in.TimeZone)
	if err != nil {
		return in, nil, err
	}
	in.Schedule, err = in.Schedule.clean()
	if err != nil {
		return in, nil, invalid(err.Error())
	}
	return in, loc, nil
}

func location(name string) (*time.Location, error) {
	// LoadLocation takes "" and "Local" as the computer's zone, which can change.
	if name == "" || name == "Local" {
		return nil, invalid("choose a time zone")
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, invalid("unknown time zone " + name)
	}
	return loc, nil
}

// Preview returns at most 3 times of a schedule after now.
func (s *Service) Preview(sched Schedule, timeZone string) ([]int64, error) {
	loc, err := location(timeZone)
	if err != nil {
		return nil, err
	}
	sched, err = sched.clean()
	if err != nil {
		return nil, invalid(err.Error())
	}
	now := s.now()
	return millis(sched.upcoming(now, now, loc, 3)), nil
}

func millis(times []time.Time) []int64 {
	out := make([]int64, len(times))
	for i, t := range times {
		out[i] = t.UnixMilli()
	}
	return out
}

// saveBound orders target validation and the binding write against archive and delete.
// Tool mutations already hold the writer and use a transaction-local service.
func (s *Service) saveBound(ctx context.Context, save func(*Service) (Automation, error)) (Automation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Automation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	local := *s
	local.db, local.q = nil, s.q.WithTx(tx)
	if err := local.q.LockRoutineTools(ctx); err != nil {
		return Automation{}, err
	}
	a, err := save(&local)
	if err != nil {
		return Automation{}, err
	}
	return a, tx.Commit()
}

func (s *Service) checkTarget(ctx context.Context, agentID string, in Input) error {
	if in.Session != "session" {
		return nil
	}
	_, err := chat.AutomationTarget(ctx, s.q, agentID, targetID(in))
	if errors.Is(err, chat.ErrAutomationTarget) {
		return invalid(err.Error())
	}
	return err
}

func targetID(in Input) sql.NullInt64 {
	return sql.NullInt64{Int64: in.TargetConversationID, Valid: in.TargetConversationID != 0}
}

// Create stores a new automation.
func (s *Service) Create(ctx context.Context, agentID string, in Input) (Automation, error) {
	if in.Session == "session" && s.db != nil {
		return s.saveBound(ctx, func(local *Service) (Automation, error) { return local.Create(ctx, agentID, in) })
	}
	in, loc, err := clean(in)
	if err != nil {
		return Automation{}, err
	}
	if err := s.checkTarget(ctx, agentID, in); err != nil {
		return Automation{}, err
	}
	if err := s.checkEnvironment(ctx, in.EnvironmentID, ""); err != nil {
		return Automation{}, err
	}
	now := s.now()
	row := sqlc.Automation{
		ID: uuid.NewV7().String(), AgentID: agentID, CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli(), NotificationRoute: "none",
	}
	if err := s.applyNotification(ctx, &row, in.Notification); err != nil {
		return Automation{}, err
	}
	if err := fill(&row, in, loc, now); err != nil {
		return Automation{}, err
	}
	err = s.q.CreateAutomation(ctx, sqlc.CreateAutomationParams(row))
	if err != nil {
		return Automation{}, err
	}
	return s.view(ctx, row)
}

// Update changes an automation. Its next time counts from now, so a change never runs a past time.
func (s *Service) Update(ctx context.Context, agentID string, id string, in Input) (Automation, error) {
	if in.Session == "session" && s.db != nil {
		return s.saveBound(ctx, func(local *Service) (Automation, error) { return local.Update(ctx, agentID, id, in) })
	}
	in, loc, err := clean(in)
	if err != nil {
		return Automation{}, err
	}
	row, err := s.get(ctx, agentID, id)
	if err != nil {
		return Automation{}, err
	}
	if err := s.checkTarget(ctx, agentID, in); err != nil {
		return Automation{}, err
	}
	if err := s.checkEnvironment(ctx, in.EnvironmentID, row.EnvironmentID); err != nil {
		return Automation{}, err
	}
	if err := s.applyNotification(ctx, &row, in.Notification); err != nil {
		return Automation{}, err
	}
	now := s.now()
	row.UpdatedAt = now.UnixMilli()
	if err := fill(&row, in, loc, now); err != nil {
		return Automation{}, err
	}
	_, err = s.q.UpdateAutomation(ctx, sqlc.UpdateAutomationParams{
		Cwd: row.Cwd, Name: row.Name, Prompt: row.Prompt, Schedule: row.Schedule, TimeZone: row.TimeZone,
		Session: row.Session, Enabled: row.Enabled, NextDueAt: row.NextDueAt, NotificationRoute: row.NotificationRoute, NotificationTargetID: row.NotificationTargetID,
		EnvironmentID: row.EnvironmentID, TargetConversationID: row.TargetConversationID, UpdatedAt: row.UpdatedAt, ID: row.ID, AgentID: agentID,
	})
	if err != nil {
		return Automation{}, err
	}
	return s.view(ctx, row)
}

// checkEnvironment fails with ErrInvalid unless id is empty, LocalID, or a saved pi-env
// environment. Routines can use E2B or runner only through their bot's default environment.
// The saved value still passes so the owner can edit or pause a routine whose environment was removed.
func (s *Service) checkEnvironment(ctx context.Context, id, current string) error {
	if id == "" || id == current || id == execenv.LocalID {
		return nil
	}
	row, err := s.q.GetEnvironment(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return invalid("the environment " + id + " does not exist")
	}
	if err != nil {
		return err
	}
	if execenv.Kind(row.Kind) != execenv.KindPiEnv {
		return invalid(fmt.Sprintf("%s is a %s environment; select local or an SSH environment", row.Name, row.Kind))
	}
	return nil
}

func (s *Service) applyNotification(ctx context.Context, row *sqlc.Automation, n *engine.RoutineNotification) error {
	if n == nil {
		return nil
	}
	if n.Mode == "target" {
		if _, err := s.q.GetRoutineNotificationTarget(ctx, sqlc.GetRoutineNotificationTargetParams{ID: n.TargetID, AgentID: row.AgentID}); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return invalid("notification target not found")
			}
			return err
		}
	}
	row.NotificationRoute = n.Mode
	row.NotificationTargetID = sql.NullString{String: n.TargetID, Valid: n.TargetID != ""}
	return nil
}

// fill sets the fields of row from in, and the next time after now. An interval counts from now.
func fill(row *sqlc.Automation, in Input, loc *time.Location, now time.Time) error {
	sched, err := json.Marshal(in.Schedule)
	if err != nil {
		return err
	}
	if in.Enabled && in.Schedule.Kind == "once" && !time.UnixMilli(in.Schedule.At).After(now) {
		return invalid("the time is in the past")
	}
	row.Cwd, row.EnvironmentID, row.TargetConversationID = in.Cwd, in.EnvironmentID, targetID(in)
	row.Name, row.Prompt, row.Schedule, row.TimeZone, row.Session = in.Name, in.Prompt, string(sched), in.TimeZone, in.Session
	row.Enabled, row.NextDueAt = 0, sql.NullInt64{}
	if in.Enabled {
		row.Enabled = 1
		if next, ok := in.Schedule.next(now, now, loc); ok {
			row.NextDueAt = sql.NullInt64{Int64: next.UnixMilli(), Valid: true}
		}
	}
	return nil
}

// Delete removes an automation and its runs. Its chats stay.
func (s *Service) Delete(ctx context.Context, agentID string, id string) error {
	if _, err := s.get(ctx, agentID, id); err != nil {
		return err
	}
	if err := s.q.DeleteAutomationRuns(ctx, id); err != nil {
		return err
	}
	return s.q.DeleteAutomation(ctx, sqlc.DeleteAutomationParams{ID: id, AgentID: agentID})
}

// Get returns an automation, or ErrNotFound.
func (s *Service) Get(ctx context.Context, agentID string, id string) (Automation, error) {
	row, err := s.get(ctx, agentID, id)
	if err != nil {
		return Automation{}, err
	}
	return s.view(ctx, row)
}

func (s *Service) get(ctx context.Context, agentID string, id string) (sqlc.Automation, error) {
	row, err := s.q.GetAutomation(ctx, sqlc.GetAutomationParams{ID: id, AgentID: agentID})
	if errors.Is(err, sql.ErrNoRows) {
		return row, ErrNotFound
	}
	return row, err
}

// ListTargetCandidates returns the bot's existing conversations for the Routine picker.
func (s *Service) ListTargetCandidates(ctx context.Context, agentID string) ([]sqlc.Conversation, error) {
	return s.q.ListRoutineTargetCandidates(ctx, agentID)
}

// List returns all automations, oldest first.
func (s *Service) List(ctx context.Context, agentID string) ([]Automation, error) {
	rows, err := s.q.ListAutomations(ctx, agentID)
	if err != nil {
		return nil, err
	}
	out := make([]Automation, len(rows))
	for i, row := range rows {
		if out[i], err = s.view(ctx, row); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Service) view(ctx context.Context, row sqlc.Automation) (Automation, error) {
	a := Automation{
		Notification: &engine.RoutineNotification{Mode: row.NotificationRoute, TargetID: row.NotificationTargetID.String},
		Cwd:          row.Cwd, Name: row.Name, Prompt: row.Prompt, TimeZone: row.TimeZone, Session: row.Session, Enabled: row.Enabled == 1, EnvironmentID: row.EnvironmentID, TargetConversationID: row.TargetConversationID.Int64,
		ID: row.ID, NextRuns: []int64{}, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if err := json.Unmarshal([]byte(row.Schedule), &a.Schedule); err != nil {
		return a, fmt.Errorf("automation %s: %w", row.ID, err)
	}
	if row.NextDueAt.Valid {
		loc, err := time.LoadLocation(row.TimeZone)
		if err != nil {
			return a, err
		}
		first := time.UnixMilli(row.NextDueAt.Int64)
		// The times after the next one; a time that passed while dex did not run shows as is.
		a.NextRuns = append([]int64{first.UnixMilli()}, millis(a.Schedule.upcoming(first, first, loc, 2))...)
	}
	runs, err := s.Runs(ctx, row.AgentID, row.ID, 1)
	if err != nil {
		return a, err
	}
	if len(runs) > 0 {
		a.LastRun = &runs[0]
	}
	return a, nil
}

// Runs returns the newest runs of an automation, at most limit.
func (s *Service) Runs(ctx context.Context, agentID string, id string, limit int64) ([]Run, error) {
	if _, err := s.get(ctx, agentID, id); err != nil {
		return nil, err
	}
	rows, err := s.q.ListAutomationRuns(ctx, sqlc.ListAutomationRunsParams{AutomationID: id, RowLimit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]Run, len(rows))
	for i, r := range rows {
		out[i] = Run{Outcome: r.Outcome, Summary: r.Summary, ChangeKey: r.ChangeKey, Duplicate: r.Duplicate == 1, ID: r.ID, DueAt: r.DueAt.Int64, CreatedAt: r.CreatedAt, Status: r.Status, Reason: r.Reason.String, NotificationNote: r.NotificationNote, ChatID: r.ChatID.Int64, EnvironmentID: r.EnvironmentID, Cwd: r.Cwd}
		if r.AdmittedAt.Valid {
			out[i].AdmittedAt = new(r.AdmittedAt.Int64)
		}
		if r.SettledAt.Valid {
			out[i].SettledAt = new(r.SettledAt.Int64)
		}
		if r.Status != "started" {
			continue
		}
		switch r.MessageStatus.String {
		case "pending":
			out[i].Status = "running"
		case "done":
			out[i].Status = "done"
		case "unanswered":
			out[i].Status, out[i].Reason = "failed", r.MessageReason.String
		default:
			// A crash between the run and its message.
			out[i].Status, out[i].Reason = "failed", "dex stopped before it stored the message"
		}
	}
	return out, nil
}

// RunNow runs an automation outside its schedule.
func (s *Service) RunNow(ctx context.Context, agentID string, id string) (Run, error) {
	row, err := s.get(ctx, agentID, id)
	if err != nil {
		return Run{}, err
	}
	return s.admit(ctx, row, sql.NullInt64{}, "", nil)
}

// Check runs each automation whose next time has come, and moves its next time past now.
func (s *Service) Check(ctx context.Context) {
	now := s.now()
	due, err := s.q.ListDueAutomations(ctx, sql.NullInt64{Int64: now.UnixMilli(), Valid: true})
	if err != nil {
		slog.Error("list due automations", "err", err)
		return
	}
	for _, row := range due {
		if err := s.check(ctx, row, now); err != nil && ctx.Err() == nil {
			slog.Error("run automation", "automation", row.ID, "err", err)
		}
	}
}

func (s *Service) check(ctx context.Context, row sqlc.Automation, now time.Time) error {
	var sched Schedule
	if err := json.Unmarshal([]byte(row.Schedule), &sched); err != nil {
		return err
	}
	loc, err := time.LoadLocation(row.TimeZone)
	if err != nil {
		return err
	}
	// A saved due time is an aligned interval anchor, even after a metadata edit.
	anchor := time.UnixMilli(row.NextDueAt.Int64)
	latest := sched.latest(time.UnixMilli(row.NextDueAt.Int64), now, anchor, loc)
	var skip string
	if now.Sub(latest) > grace {
		skip = chat.RoutineSkipMissed
	}
	// The run comes before the next time: a crash between them repeats the check, which finds the
	// run and does not store it again.
	_, err = s.admit(ctx, row, sql.NullInt64{Int64: latest.UnixMilli(), Valid: true}, skip, nil)
	// An archived bot keeps the due time; restore runs it if the grace period allows.
	if errors.Is(err, chat.ErrAutomationChanged) || errors.Is(err, db.ErrAgentArchived) {
		return nil
	}
	if err != nil {
		return err
	}
	next, ok := sched.next(now, anchor, loc)
	// A concurrent schedule edit owns its new cursor.
	_, err = s.q.SetAutomationNextDue(ctx, sqlc.SetAutomationNextDueParams{
		NextDueAt: sql.NullInt64{Int64: next.UnixMilli(), Valid: ok}, ID: row.ID,
		ExpectedDueAt: row.NextDueAt, ExpectedUpdatedAt: row.UpdatedAt,
		ExpectedSchedule: row.Schedule, ExpectedTimeZone: row.TimeZone,
	})
	if err != nil {
		return err
	}
	return nil
}

// admit stores a run for due, skipped with the reason skip, or when the bot has no model, a run
// of the automation is busy, or its environment cannot be used. An existing scheduled run creates
// no new input. A nil pre checks the environment here; the check does network I/O, so it never
// runs in a transaction.
func (s *Service) admit(ctx context.Context, row sqlc.Automation, due sql.NullInt64, skip string, pre *Target) (Run, error) {
	if skip == "" && row.Session != "session" {
		agent, err := s.q.GetAgent(ctx, row.AgentID)
		if err != nil {
			return Run{}, err
		}
		busy, err := s.q.CountAutomationPending(ctx, row.ID)
		if err != nil {
			return Run{}, err
		}
		switch {
		case agent.ModelID == "":
			skip = chat.RoutineSkipNoModel
		case busy > 0:
			skip = chat.RoutineSkipBusy
		}
	}
	run := Run{ID: uuid.NewV7().String(), DueAt: due.Int64, CreatedAt: s.now().UnixMilli(), Status: "started", Reason: skip}
	params := sqlc.CreateAutomationRunParams{
		ID: run.ID, AutomationID: row.ID, DueAt: due, Status: run.Status,
		CreatedAt: run.CreatedAt,
	}
	if skip != "" {
		// A predetermined skip needs no environment probe. Store it in the same transaction
		// as any alert, including when the routine was invoked through a tool.
		pre = &Target{Skip: skip}
	} else if pre == nil {
		t, err := s.Preflight(ctx, row)
		if err != nil {
			return Run{}, err
		}
		pre = &t
	}
	key := Channel + row.ID
	if row.Session == "new" {
		key += ":" + run.ID
	}
	// The input stores row only while row is current: the preflight gives an edit time to commit.
	input := chat.AutomationInput{Run: params, AgentID: row.AgentID, ChannelKey: key, Title: row.Name, Content: row.Prompt, Cwd: row.Cwd, EnvironmentID: row.EnvironmentID, Target: pre.ID, Skip: pre.Skip, Expected: &row}
	result, err := s.enqueue(ctx, input)
	if !due.Valid && errors.Is(err, chat.ErrAutomationChanged) {
		// A scheduled run checks again next minute; a manual run tells the caller.
		err = fmt.Errorf("%w: the routine changed during its check; run it again", engine.ErrBusy)
	}
	switch {
	case result.Created && result.ConversationID == 0:
		run.Status, run.Reason = "skipped", result.Skip
	case result.Created:
		run.Status, run.ChatID = "running", result.ConversationID
	}
	return run, err
}

// Target is the effective environment of a run, checked before the run is stored. Skip is the
// reason when the run cannot use it.
type Target struct {
	ID, Skip string
}

// Preflight resolves the effective environment of a run of row and checks that it can be used.
// The input transaction compares the environment again, so a change in between does not admit a
// run on an environment that was not checked.
func (s *Service) Preflight(ctx context.Context, row sqlc.Automation) (Target, error) {
	agent, err := s.q.GetAgent(ctx, row.AgentID)
	if err != nil {
		return Target{}, err
	}
	environmentID := row.EnvironmentID
	if row.Session == "session" {
		conv, err := chat.AutomationTarget(ctx, s.q, row.AgentID, row.TargetConversationID)
		if errors.Is(err, chat.ErrAutomationTarget) {
			return Target{Skip: err.Error()}, nil
		}
		if err != nil {
			return Target{}, err
		}
		environmentID = conv.EnvironmentID
	}
	id := cmp.Or(environmentID, agent.EnvironmentID, execenv.LocalID)
	skip, err := s.unavailable(ctx, row.AgentID, id)
	if skip == "" && err == nil && row.Session == "session" && s.status != nil && s.status(ctx, row.AgentID, id) != "available" {
		skip = chat.RoutineUnavailableEnvironment(id)
	}
	return Target{ID: id, Skip: skip}, err
}

// unavailable returns the skip reason when a run cannot use environment id, so a run never falls
// back to another machine. Only an SSH host is probed: local is always there, and opening an E2B
// sandbox or a runner would start the work that the run does.
func (s *Service) unavailable(ctx context.Context, agentID, id string) (string, error) {
	if id == execenv.LocalID {
		return "", nil
	}
	env, err := s.q.GetEnvironment(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return chat.RoutineRemovedEnvironment(id), nil
	}
	if err != nil || execenv.Kind(env.Kind) != execenv.KindPiEnv || s.probe == nil {
		return "", err
	}
	if err := s.probe(ctx, agentID, id); err != nil {
		// Probe errors can contain host details or credentials. Do not store or send them.
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return chat.RoutineUnavailableEnvironment(env.Name), nil
	}
	return "", nil
}
