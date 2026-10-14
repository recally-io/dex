// Package automation runs prompts on a schedule. The web UI manages automations, and later an agent
// tool will.
//
// Each automation stores its next scheduled time (next_due_at). Check runs the automations whose
// time has come; the scheduler calls it every minute, so the schedule lives in the database, not
// in the scheduling library. When dex did not run at a scheduled time, Check runs only the newest
// missed time, and only when it is at most an hour old; it skips the others.
package automation

import (
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

	"github.com/vaayne/dex/internal/db/sqlc"
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

// Poster stores a message in a chat and delivers it to the worker.
type Poster interface {
	Post(ctx context.Context, agentID, channelKey, title, id, content string) (int64, error)
}

// Input is what the user sets.
type Input struct {
	Name     string   `json:"name"`
	Prompt   string   `json:"prompt"`
	Schedule Schedule `json:"schedule"`
	TimeZone string   `json:"timeZone"`
	Session  string   `json:"session"` // new or reuse
	Enabled  bool     `json:"enabled"`
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
	ID        string `json:"id"`
	DueAt     int64  `json:"dueAt,omitzero"` // 0 for a run that the user started
	CreatedAt int64  `json:"createdAt"`
	Status    string `json:"status"` // running, done, failed, or skipped
	Reason    string `json:"reason,omitempty"`
	ChatID    int64  `json:"chatId,omitzero"` // 0 when the run has no message
}

// Service manages the automations of one agent.
type Service struct {
	q       *sqlc.Queries
	chats   Poster
	agentID string
	now     func() time.Time
}

// New returns a Service. Call Check every minute.
func New(q *sqlc.Queries, chats Poster, agentID string, now func() time.Time) *Service {
	return &Service{q: q, chats: chats, agentID: agentID, now: now}
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
	in.Name = strings.TrimSpace(in.Name)
	in.Prompt = strings.TrimSpace(in.Prompt)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 100 {
		return in, nil, invalid("the name must have 1 to 100 characters")
	}
	if in.Prompt == "" || utf8.RuneCountInString(in.Prompt) > maxPrompt {
		return in, nil, invalid("the prompt must have 1 to 32000 characters")
	}
	if in.Session != "new" && in.Session != "reuse" {
		return in, nil, invalid("the session must be new or reuse")
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

// Create stores a new automation.
func (s *Service) Create(ctx context.Context, in Input) (Automation, error) {
	in, loc, err := clean(in)
	if err != nil {
		return Automation{}, err
	}
	now := s.now()
	row := sqlc.Automation{
		ID: uuid.NewV7().String(), AgentID: s.agentID, CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli(),
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
func (s *Service) Update(ctx context.Context, id string, in Input) (Automation, error) {
	in, loc, err := clean(in)
	if err != nil {
		return Automation{}, err
	}
	row, err := s.get(ctx, id)
	if err != nil {
		return Automation{}, err
	}
	now := s.now()
	row.UpdatedAt = now.UnixMilli()
	if err := fill(&row, in, loc, now); err != nil {
		return Automation{}, err
	}
	_, err = s.q.UpdateAutomation(ctx, sqlc.UpdateAutomationParams{
		Name: row.Name, Prompt: row.Prompt, Schedule: row.Schedule, TimeZone: row.TimeZone,
		Session: row.Session, Enabled: row.Enabled, NextDueAt: row.NextDueAt, UpdatedAt: row.UpdatedAt,
		ID: row.ID, AgentID: s.agentID,
	})
	if err != nil {
		return Automation{}, err
	}
	return s.view(ctx, row)
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
func (s *Service) Delete(ctx context.Context, id string) error {
	if _, err := s.get(ctx, id); err != nil {
		return err
	}
	if err := s.q.DeleteAutomationRuns(ctx, id); err != nil {
		return err
	}
	return s.q.DeleteAutomation(ctx, sqlc.DeleteAutomationParams{ID: id, AgentID: s.agentID})
}

// Get returns an automation, or ErrNotFound.
func (s *Service) Get(ctx context.Context, id string) (Automation, error) {
	row, err := s.get(ctx, id)
	if err != nil {
		return Automation{}, err
	}
	return s.view(ctx, row)
}

func (s *Service) get(ctx context.Context, id string) (sqlc.Automation, error) {
	row, err := s.q.GetAutomation(ctx, sqlc.GetAutomationParams{ID: id, AgentID: s.agentID})
	if errors.Is(err, sql.ErrNoRows) {
		return row, ErrNotFound
	}
	return row, err
}

// List returns all automations, oldest first.
func (s *Service) List(ctx context.Context) ([]Automation, error) {
	rows, err := s.q.ListAutomations(ctx, s.agentID)
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
		Name: row.Name, Prompt: row.Prompt, TimeZone: row.TimeZone, Session: row.Session, Enabled: row.Enabled == 1,
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
		a.NextRuns = append([]int64{first.UnixMilli()}, millis(a.Schedule.upcoming(first, time.UnixMilli(row.UpdatedAt), loc, 2))...)
	}
	runs, err := s.Runs(ctx, row.ID, 1)
	if err != nil {
		return a, err
	}
	if len(runs) > 0 {
		a.LastRun = &runs[0]
	}
	return a, nil
}

// Runs returns the newest runs of an automation, at most limit.
func (s *Service) Runs(ctx context.Context, id string, limit int64) ([]Run, error) {
	rows, err := s.q.ListAutomationRuns(ctx, sqlc.ListAutomationRunsParams{AutomationID: id, Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]Run, len(rows))
	for i, r := range rows {
		out[i] = Run{ID: r.ID, DueAt: r.DueAt.Int64, CreatedAt: r.CreatedAt, Status: r.Status, Reason: r.Reason.String, ChatID: r.ChatID.Int64}
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
func (s *Service) RunNow(ctx context.Context, id string) (Run, error) {
	row, err := s.get(ctx, id)
	if err != nil {
		return Run{}, err
	}
	run, _, err := s.admit(ctx, row, sql.NullInt64{}, "")
	if err != nil {
		return Run{}, err
	}
	if run.Status == "started" {
		s.deliver(ctx, row, &run)
	}
	return run, nil
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
	anchor := time.UnixMilli(row.UpdatedAt)
	latest := sched.latest(time.UnixMilli(row.NextDueAt.Int64), now, anchor, loc)
	var skip string
	if now.Sub(latest) > grace {
		skip = "dex did not run at the scheduled time"
	}
	// The run comes before the next time: a crash between them repeats the check, which finds the
	// run and does not store it again.
	run, created, err := s.admit(ctx, row, sql.NullInt64{Int64: latest.UnixMilli(), Valid: true}, skip)
	if err != nil {
		return err
	}
	next, ok := sched.next(now, anchor, loc)
	err = s.q.SetAutomationNextDue(ctx, sqlc.SetAutomationNextDueParams{
		NextDueAt: sql.NullInt64{Int64: next.UnixMilli(), Valid: ok}, ID: row.ID,
	})
	if err != nil {
		return err
	}
	if created && run.Status == "started" {
		s.deliver(ctx, row, &run)
	}
	return nil
}

// admit stores a run for due, skipped with the reason skip, or when the agent has no model or a
// run of the automation is busy. It returns false when a run for due exists already.
func (s *Service) admit(ctx context.Context, row sqlc.Automation, due sql.NullInt64, skip string) (Run, bool, error) {
	if skip == "" {
		agent, err := s.q.GetAgent(ctx, row.AgentID)
		if err != nil {
			return Run{}, false, err
		}
		busy, err := s.q.CountAutomationPending(ctx, row.ID)
		if err != nil {
			return Run{}, false, err
		}
		switch {
		case agent.ModelID == "":
			skip = "the agent has no model"
		case busy > 0:
			skip = "the chat of the automation is busy"
		}
	}
	run := Run{ID: uuid.NewV7().String(), DueAt: due.Int64, CreatedAt: s.now().UnixMilli(), Status: "started", Reason: skip}
	if skip != "" {
		run.Status = "skipped"
	}
	created, err := s.q.CreateAutomationRun(ctx, sqlc.CreateAutomationRunParams{
		ID: run.ID, AutomationID: row.ID, DueAt: due, Status: run.Status,
		Reason: sql.NullString{String: skip, Valid: skip != ""}, CreatedAt: run.CreatedAt,
	})
	return run, created > 0, err
}

// deliver posts the prompt of a started run. A failure marks the run failed.
func (s *Service) deliver(ctx context.Context, row sqlc.Automation, run *Run) {
	key := Channel + row.ID
	if row.Session == "new" {
		key += ":" + run.ID
	}
	chatID, err := s.chats.Post(ctx, row.AgentID, key, row.Name, run.ID, row.Prompt)
	if err == nil {
		run.Status, run.ChatID = "running", chatID
		return
	}
	run.Status, run.Reason = "failed", err.Error()
	if err := s.q.FailAutomationRun(ctx, sqlc.FailAutomationRunParams{
		Reason: sql.NullString{String: err.Error(), Valid: true}, ID: run.ID,
	}); err != nil {
		slog.Error("record a failed automation run", "run", run.ID, "err", err)
	}
}
