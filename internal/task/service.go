// Package task records Tasks independently of execution sessions.
package task

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// Title returns the trimmed title, or ErrInvalid when it is empty or longer than 160 characters.
func Title(title string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" || utf8.RuneCountInString(title) > 160 {
		return "", ErrInvalid
	}
	return title, nil
}

// Content returns the trimmed entry text, or ErrInvalid when it is empty or longer than 32000 characters.
func Content(content string) (string, error) {
	content = strings.TrimSpace(content)
	if content == "" || utf8.RuneCountInString(content) > 32000 {
		return "", ErrInvalid
	}
	return content, nil
}

// Service stores Tasks in the control database.
type Service struct {
	db      *sql.DB
	q       *sqlc.Queries
	wake    func(sessionID int64)
	onEntry func(entryID int64)
}

// New opens a Task service on the existing control database. wake can be nil.
func New(conn *sql.DB, wake func(sessionID int64)) *Service {
	return &Service{db: conn, q: sqlc.New(conn), wake: wake}
}

// SetOnEntry sets the optional post-commit hook. Set it before serving requests.
func (s *Service) SetOnEntry(onEntry func(entryID int64)) { s.onEntry = onEntry }

// Notify publishes entries and wakes Main after the Ledger commits. Entries can
// publish out of order; subscribers must read by the durable entry cursor.
func (s *Service) Notify(l *Ledger) {
	if s == nil || l == nil {
		return
	}
	if s.onEntry != nil {
		for _, id := range l.entryIDs {
			s.onEntry(id)
		}
	}
	if s.wake != nil {
		for _, id := range l.wakeSessions {
			s.wake(id)
		}
	}
}

func (s *Service) write(ctx context.Context, change func(*Ledger, int64) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	if err := q.LockTaskWriter(ctx); err != nil {
		return err
	}
	ledger := NewLedger(q)
	if err := change(ledger, time.Now().UnixMilli()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.Notify(ledger)
	return nil
}

func (s *Service) CreateTask(ctx context.Context, actor Actor, origin Origin, p CreateTaskParams) (task sqlc.Task, err error) {
	err = s.write(ctx, func(l *Ledger, now int64) error {
		task, err = l.CreateTask(ctx, actor, origin, p, now)
		return err
	})
	return task, err
}

// Detail holds a Task and its related records from one snapshot.
type Detail struct {
	Task                   sqlc.Task
	ParentTask             *sqlc.Task
	Subtasks, Dependencies []sqlc.Task
	Entries                []sqlc.TaskEntry
	Questions              []sqlc.Question
	Objects                []sqlc.TaskObject
	Waits                  []sqlc.TaskWait
	Sessions               []sqlc.ListTaskSessionsRow
	Blockers               []Blocker
}

// Detail reads the Owner's full timeline by Task key or UUID.
func (s *Service) Detail(ctx context.Context, key string) (Detail, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Detail{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	task, err := Lookup(ctx, q, key)
	if err != nil {
		return Detail{}, err
	}
	entries, err := q.ListTaskEntries(ctx, task.ID)
	if err != nil {
		return Detail{}, err
	}
	questions, err := q.ListTaskQuestions(ctx, task.ID)
	if err != nil {
		return Detail{}, err
	}
	objects, err := q.ListTaskObjects(ctx, task.ID)
	if err != nil {
		return Detail{}, err
	}
	waits, err := q.ListTaskWaits(ctx, task.ID)
	if err != nil {
		return Detail{}, err
	}
	sessions, err := q.ListTaskSessions(ctx, task.ID)
	if err != nil {
		return Detail{}, err
	}
	blockers, err := ComputeBlockers(ctx, q, task)
	if err != nil {
		return Detail{}, err
	}
	out := Detail{Task: task, Entries: entries, Questions: questions, Objects: objects, Waits: waits, Sessions: sessions, Blockers: blockers}
	out.ParentTask, out.Dependencies, err = UpstreamTasks(ctx, q, task)
	if err != nil {
		return Detail{}, err
	}
	out.Subtasks, err = q.ListSubtasks(ctx, sql.NullString{String: task.ID, Valid: true})
	if err != nil {
		return Detail{}, err
	}
	return out, tx.Commit()
}

// ModelTitle keeps source-provided titles out of model context. An explicit
// rename records a new title separately from the source event.
func ModelTitle(ctx context.Context, q *sqlc.Queries, task sqlc.Task) (string, error) {
	if task.CreatorKind != CreatorSource {
		return task.Title, nil
	}
	renamed, err := q.HasTaskTitleEntry(ctx, sqlc.HasTaskTitleEntryParams{TaskID: task.ID, Title: task.Title})
	if renamed {
		return task.Title, err
	}
	return "Source Task " + FormatKey(task.Number), err
}

func (s *Service) PostEntry(ctx context.Context, actor Actor, origin Origin, key string, p PostEntryParams, revision *int64) (entry sqlc.TaskEntry, err error) {
	err = s.write(ctx, func(l *Ledger, now int64) error {
		task, err := Lookup(ctx, l.q, key)
		if err != nil {
			return err
		}
		if err := checkRevision(task, revision); err != nil {
			return err
		}
		entry, err = l.PostEntry(ctx, actor, origin, task, p, now)
		return err
	})
	return entry, err
}

// TransitionStatus applies a Task transition and records any review wake in the same transaction.
func (l *Ledger) TransitionStatus(ctx context.Context, actor Actor, origin Origin, task sqlc.Task, status string, now int64) (sqlc.Task, error) {
	action, valid := transition(task, status)
	if err := authorize(ctx, l.q, actor, origin, task, action); err != nil {
		return sqlc.Task{}, err
	}
	if !valid {
		return sqlc.Task{}, ErrInvalid
	}
	result, err := l.changeStatus(ctx, actor, task, status, now)
	if err == nil && status == StatusInReview && task.ReviewerKind == ActorBot {
		_, err = l.queueWake(ctx, task.ReviewerID, result, WakeReviewRequested, now)
	}
	return result, err
}

func transition(task sqlc.Task, to string) (Action, bool) {
	from := task.Status
	switch {
	case to == StatusCancelled:
		return ActionOwnerLifecycle, IsOpenStatus(from)
	case !IsOpenStatus(from):
		return ActionOwnerLifecycle, to == StatusInProgress
	case to == StatusInReview:
		return ActionSubmitReview, from == StatusInProgress
	case to == StatusDone:
		return ActionComplete, from == StatusInProgress && task.ReviewerKind == ""
	case to == StatusBacklog || to == StatusReady || to == StatusInProgress:
		return ActionTransition, from != to && from != StatusInReview
	default:
		return ActionTransition, false
	}
}

// Review records the decision and any Assignee wake in the same transaction.
func (l *Ledger) Review(ctx context.Context, actor Actor, origin Origin, task sqlc.Task, decision string, reviewSeq int64, feedback string, now int64) (sqlc.Task, error) {
	if err := authorize(ctx, l.q, actor, origin, task, ActionReview); err != nil {
		return sqlc.Task{}, err
	}
	if task.Status != StatusInReview || decision == ReviewApprove && reviewSeq != task.ReviewSeq {
		return sqlc.Task{}, ErrConflict
	}
	status := StatusDone
	switch decision {
	case ReviewApprove:
	case ReviewRequestChanges:
		status = StatusInProgress
	default:
		return sqlc.Task{}, ErrInvalid
	}
	var err error
	feedback = strings.TrimSpace(feedback)
	if feedback != "" || decision == ReviewRequestChanges {
		if feedback, err = Content(feedback); err != nil {
			return sqlc.Task{}, err
		}
	}
	meta, err := json.Marshal(struct {
		Decision  string `json:"decision"`
		ReviewSeq int64  `json:"reviewSeq"`
		Status    string `json:"status"`
	}{decision, task.ReviewSeq, status})
	if err != nil {
		return sqlc.Task{}, err
	}
	result, _, err := l.append(ctx, actor, task, status, task.Title, 0, sqlc.InsertTaskEntryParams{
		Kind: KindReview, Content: feedback, MetaJson: string(meta), CreatedAt: now,
	})
	if err == nil {
		if decision == ReviewRequestChanges {
			_, err = l.queueWake(ctx, task.AssigneeBotID, result, WakeChangesRequested, now)
		} else {
			err = l.notifyDependents(ctx, task, result, now)
		}
	}
	return result, err
}
