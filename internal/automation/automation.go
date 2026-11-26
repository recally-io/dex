// Package automation runs prompts on a schedule. The web UI manages automations, and later a bot
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

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/runner"
	"github.com/recally-io/dex/internal/session"
)

// Channel starts the channel key of an automation's sessions: automation:<id> for an automation
// that reuses its session, and automation:<id>:<run id> for each run of the other automations.
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
	Notification    *Notification `json:"notification,omitempty"`
	Cwd             string        `json:"cwd"`
	Name            string        `json:"name"`
	Prompt          string        `json:"prompt"`
	Schedule        Schedule      `json:"schedule"`
	TimeZone        string        `json:"timeZone"`
	Session         string        `json:"session"` // new, reuse, or session
	TargetSessionID int64         `json:"targetSessionId,omitzero"`
	Enabled         bool          `json:"enabled"`
	// RunnerID is Local or an SSH Runner ID. An empty ID follows the Bot's Runner.
	RunnerID string `json:"runnerId"`
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
	SessionID        int64  `json:"sessionId,omitzero"` // 0 when the run has no message
	// RunnerID and Cwd are where the run started: the effective Runner, never empty
	// for a run with a message.
	RunnerID string `json:"runnerId,omitempty"`
	Cwd      string `json:"cwd,omitempty"`
}

// Service manages automations for all bots.
type Service struct {
	q          *sqlc.Queries
	enqueue    func(context.Context, session.AutomationInput) (session.AutomationInputResult, error)
	now        func() time.Time
	wakeOutbox func(context.Context, string)
	// probe opens the SSH Runner before the run starts; nil skips the check.
	probe  func(ctx context.Context, botID, runnerID string) error
	status func(ctx context.Context, botID, runnerID string) string
}

// ConfigureProbe sets the check that an SSH Runner is reachable before a run. A run on an
// unreachable host is skipped, so it never starts a session that cannot use its machine.
func (s *Service) ConfigureProbe(probe func(ctx context.Context, botID, runnerID string) error) {
	s.probe = probe
}

// ConfigureRunnerStatus lets a bound Automation detect an offline target without opening it.
func (s *Service) ConfigureRunnerStatus(status func(context.Context, string, string) string) {
	s.status = status
}

// New returns a Service. Call Check every minute.
func New(q *sqlc.Queries, sessions *session.Service, now func() time.Time) *Service {
	s := &Service{q: q, now: now}
	if sessions != nil {
		s.enqueue = sessions.EnqueueAutomation
		s.wakeOutbox = sessions.WakeAutomationOutbox
	}
	return s
}

// SessionAutomation returns the ID of the automation that started the session with channelKey.
func SessionAutomation(channelKey string) (string, bool) {
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
		if in.TargetSessionID <= 0 {
			return in, nil, invalid("choose a target session")
		}
		in.Cwd, in.RunnerID = "", ""
	case "new", "reuse":
		if in.TargetSessionID != 0 {
			return in, nil, invalid("only session mode accepts a target session")
		}
		cwd, err := session.ProjectPath(in.Cwd)
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

func (s *Service) checkTarget(ctx context.Context, botID string, in Input) error {
	if in.Session != "session" {
		return nil
	}
	_, err := session.AutomationTarget(ctx, s.q, botID, targetID(in))
	if errors.Is(err, session.ErrAutomationTarget) {
		return invalid(err.Error())
	}
	return err
}

func targetID(in Input) sql.NullInt64 {
	return sql.NullInt64{Int64: in.TargetSessionID, Valid: in.TargetSessionID != 0}
}

// Create stores a new automation.
func (s *Service) Create(ctx context.Context, botID string, in Input) (Automation, error) {
	in, loc, err := clean(in)
	if err != nil {
		return Automation{}, err
	}
	if err := s.checkTarget(ctx, botID, in); err != nil {
		return Automation{}, err
	}
	if err := s.checkRunner(ctx, in.RunnerID, ""); err != nil {
		return Automation{}, err
	}
	now := s.now()
	row := sqlc.Automation{
		ID: uuid.NewV7().String(), BotID: botID, CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli(), NotificationRoute: "none",
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
func (s *Service) Update(ctx context.Context, botID string, id string, in Input) (Automation, error) {
	in, loc, err := clean(in)
	if err != nil {
		return Automation{}, err
	}
	row, err := s.get(ctx, botID, id)
	if err != nil {
		return Automation{}, err
	}
	if err := s.checkTarget(ctx, botID, in); err != nil {
		return Automation{}, err
	}
	if err := s.checkRunner(ctx, in.RunnerID, row.RunnerID); err != nil {
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
		RunnerID: row.RunnerID, TargetSessionID: row.TargetSessionID, UpdatedAt: row.UpdatedAt, ID: row.ID, BotID: botID,
	})
	if err != nil {
		return Automation{}, err
	}
	return s.view(ctx, row)
}

// checkRunner allows an empty ID, LocalID, or a saved SSH Runner. An Automation
// can use E2B or Outbound only if it follows the Bot's Runner. The current ID
// stays valid for editing or pausing the Automation after its Runner is removed.
func (s *Service) checkRunner(ctx context.Context, id, current string) error {
	if id == "" || id == current || id == runner.LocalID {
		return nil
	}
	row, err := s.q.GetRunner(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return invalid("the Runner " + id + " does not exist")
	}
	if err != nil {
		return err
	}
	if runner.Kind(row.Kind) != runner.KindSSH {
		return invalid(fmt.Sprintf("%s is a %s Runner; select Local or an SSH Runner", row.Name, row.Kind))
	}
	return nil
}

func (s *Service) applyNotification(ctx context.Context, row *sqlc.Automation, n *Notification) error {
	if n == nil {
		return nil
	}
	if n.Mode == "target" {
		if _, err := s.q.GetAutomationNotificationTarget(ctx, sqlc.GetAutomationNotificationTargetParams{ID: n.TargetID, BotID: row.BotID}); err != nil {
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
	row.Cwd, row.RunnerID, row.TargetSessionID = in.Cwd, in.RunnerID, targetID(in)
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

// Delete removes an automation and its runs. Its sessions stay.
func (s *Service) Delete(ctx context.Context, botID string, id string) error {
	if _, err := s.get(ctx, botID, id); err != nil {
		return err
	}
	if err := s.q.DeleteAutomationRuns(ctx, id); err != nil {
		return err
	}
	return s.q.DeleteAutomation(ctx, sqlc.DeleteAutomationParams{ID: id, BotID: botID})
}

// Get returns an automation, or ErrNotFound.
func (s *Service) Get(ctx context.Context, botID string, id string) (Automation, error) {
	row, err := s.get(ctx, botID, id)
	if err != nil {
		return Automation{}, err
	}
	return s.view(ctx, row)
}

func (s *Service) get(ctx context.Context, botID string, id string) (sqlc.Automation, error) {
	row, err := s.q.GetAutomation(ctx, sqlc.GetAutomationParams{ID: id, BotID: botID})
	if errors.Is(err, sql.ErrNoRows) {
		return row, ErrNotFound
	}
	return row, err
}

// ListTargetCandidates returns the bot's existing sessions for the Automation picker.
func (s *Service) ListTargetCandidates(ctx context.Context, botID string) ([]sqlc.Session, error) {
	return s.q.ListAutomationTargetCandidates(ctx, botID)
}

// List returns all automations, oldest first.
func (s *Service) List(ctx context.Context, botID string) ([]Automation, error) {
	rows, err := s.q.ListAutomations(ctx, botID)
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
		Notification: &Notification{Mode: row.NotificationRoute, TargetID: row.NotificationTargetID.String},
		Cwd:          row.Cwd, Name: row.Name, Prompt: row.Prompt, TimeZone: row.TimeZone, Session: row.Session, Enabled: row.Enabled == 1, RunnerID: row.RunnerID, TargetSessionID: row.TargetSessionID.Int64,
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
	runs, err := s.Runs(ctx, row.BotID, row.ID, 1)
	if err != nil {
		return a, err
	}
	if len(runs) > 0 {
		a.LastRun = &runs[0]
	}
	return a, nil
}

// Runs returns the newest runs of an automation, at most limit.
func (s *Service) Runs(ctx context.Context, botID string, id string, limit int64) ([]Run, error) {
	if _, err := s.get(ctx, botID, id); err != nil {
		return nil, err
	}
	rows, err := s.q.ListAutomationRuns(ctx, sqlc.ListAutomationRunsParams{AutomationID: id, RowLimit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]Run, len(rows))
	for i, r := range rows {
		out[i] = Run{Outcome: r.Outcome, Summary: r.Summary, ChangeKey: r.ChangeKey, Duplicate: r.Duplicate == 1, ID: r.ID, DueAt: r.DueAt.Int64, CreatedAt: r.CreatedAt, Status: r.Status, Reason: r.Reason.String, NotificationNote: r.NotificationNote, SessionID: r.SessionID.Int64, RunnerID: r.RunnerID, Cwd: r.Cwd}
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
func (s *Service) RunNow(ctx context.Context, botID string, id string) (Run, error) {
	row, err := s.get(ctx, botID, id)
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
		skip = session.RoutineSkipMissed
	}
	// The run comes before the next time: a crash between them repeats the check, which finds the
	// run and does not store it again.
	_, err = s.admit(ctx, row, sql.NullInt64{Int64: latest.UnixMilli(), Valid: true}, skip, nil)
	// An archived bot keeps the due time; restore runs it if the grace period allows.
	if errors.Is(err, session.ErrAutomationChanged) || errors.Is(err, db.ErrBotArchived) {
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
// of the automation is busy, or its Runner cannot be used. An existing scheduled run creates
// no new input. A nil pre checks the Runner here; the check does network I/O, so it never
// runs in a transaction.
func (s *Service) admit(ctx context.Context, row sqlc.Automation, due sql.NullInt64, skip string, pre *Target) (Run, error) {
	if skip == "" && row.Session != "session" {
		bot, err := s.q.GetBot(ctx, row.BotID)
		if err != nil {
			return Run{}, err
		}
		busy, err := s.q.CountAutomationPending(ctx, row.ID)
		if err != nil {
			return Run{}, err
		}
		switch {
		case bot.ModelID == "":
			skip = session.RoutineSkipNoModel
		case busy > 0:
			skip = session.RoutineSkipBusy
		}
	}
	run := Run{ID: uuid.NewV7().String(), DueAt: due.Int64, CreatedAt: s.now().UnixMilli(), Status: "started", Reason: skip}
	params := sqlc.CreateAutomationRunParams{
		ID: run.ID, AutomationID: row.ID, DueAt: due, Status: run.Status,
		CreatedAt: run.CreatedAt,
	}
	if skip != "" {
		// A predetermined skip needs no Runner probe. Store it in the same transaction
		// as any alert, including when a tool started the Automation.
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
	input := session.AutomationInput{Run: params, BotID: row.BotID, ChannelKey: key, Title: row.Name, Content: row.Prompt, Cwd: row.Cwd, RunnerID: row.RunnerID, Target: pre.ID, Skip: pre.Skip, Expected: &row}
	result, err := s.enqueue(ctx, input)
	if !due.Valid && errors.Is(err, session.ErrAutomationChanged) {
		// A scheduled run checks again next minute; a manual run tells the caller.
		err = fmt.Errorf("%w: the automation changed during its check; run it again", engine.ErrBusy)
	}
	switch {
	case result.Created && result.SessionID == 0:
		run.Status, run.Reason = "skipped", result.Skip
	case result.Created:
		run.Status, run.SessionID = "running", result.SessionID
	}
	return run, err
}

// Target is the effective Runner of a run, checked before the run is stored. Skip is the
// reason when the run cannot use it.
type Target struct {
	ID, Skip string
}

// Preflight resolves the effective Runner of a run of row and checks that it can be used.
// The input transaction compares the Runner again, so a change in between does not admit a
// run on a Runner that it did not check.
func (s *Service) Preflight(ctx context.Context, row sqlc.Automation) (Target, error) {
	bot, err := s.q.GetBot(ctx, row.BotID)
	if err != nil {
		return Target{}, err
	}
	runnerID := row.RunnerID
	if row.Session == "session" {
		conv, err := session.AutomationTarget(ctx, s.q, row.BotID, row.TargetSessionID)
		if errors.Is(err, session.ErrAutomationTarget) {
			return Target{Skip: err.Error()}, nil
		}
		if err != nil {
			return Target{}, err
		}
		runnerID = conv.RunnerID
	}
	id := cmp.Or(runnerID, bot.RunnerID, runner.LocalID)
	skip, err := s.unavailable(ctx, row.BotID, id)
	if skip == "" && err == nil && row.Session == "session" && s.status != nil && s.status(ctx, row.BotID, id) != "available" {
		skip = session.RoutineUnavailableRunner(id)
	}
	return Target{ID: id, Skip: skip}, err
}

// unavailable returns a skip reason instead of letting the run use a different
// Runner. It probes SSH only. Local is always available. Opening an E2B Sandbox
// or an Outbound Machine would start the run's work.
func (s *Service) unavailable(ctx context.Context, botID, id string) (string, error) {
	if id == runner.LocalID {
		return "", nil
	}
	env, err := s.q.GetRunner(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return session.RoutineRemovedRunner(id), nil
	}
	if err != nil || runner.Kind(env.Kind) != runner.KindSSH || s.probe == nil {
		return "", err
	}
	if err := s.probe(ctx, botID, id); err != nil {
		// Probe errors can contain host details or credentials. Do not store or send them.
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return session.RoutineUnavailableRunner(env.Name), nil
	}
	return "", nil
}
