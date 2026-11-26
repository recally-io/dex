// Package worktask records work independently of execution sessions.
package worktask

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// Authors of entries in the legacy HTTP projection.
const (
	AuthorOwner = "owner"
	AuthorAgent = "agent"
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
	wake    func(conversationID int64)
	onEntry func(entryID int64)
}

// New opens a Task service on the existing control database. wake can be nil.
func New(conn *sql.DB, wake func(conversationID int64)) *Service {
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
		for _, id := range l.wakeConversations {
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
	if err := q.LockWorkTaskWriter(ctx); err != nil {
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

// Create starts tracking work without starting a Session.
func (s *Service) Create(ctx context.Context, agentID, title string) (sqlc.WorkTask, error) {
	return s.CreateTask(ctx, Owner, OriginUser, CreateTaskParams{AssigneeBotID: agentID, Title: title})
}

func (s *Service) CreateTask(ctx context.Context, actor Actor, origin Origin, p CreateTaskParams) (task sqlc.WorkTask, err error) {
	err = s.write(ctx, func(l *Ledger, now int64) error {
		task, err = l.CreateTask(ctx, actor, origin, p, now)
		return err
	})
	return task, err
}

// Detail holds a Task and its related records from one snapshot.
type Detail struct {
	Task      sqlc.WorkTask
	Entries   []sqlc.WorkTaskEntry
	Questions []sqlc.Question
	Objects   []sqlc.WorkTaskObject
	Waits     []sqlc.WorkTaskWait
	Sessions  []sqlc.ListWorkTaskSessionsRow
	Blockers  []Blocker
}

// List returns the Owner's Tasks across active Bots.
func (s *Service) List(ctx context.Context, includeClosed bool) ([]sqlc.WorkTask, error) {
	return s.q.ListWorkTasks(ctx, includeClosed)
}

// Detail reads the Task and timeline in one snapshot.
func (s *Service) Detail(ctx context.Context, agentID, id string) (Detail, error) {
	return s.detail(ctx, agentID, id)
}

// BoardDetail reads the Owner's full timeline by Task key or UUID.
func (s *Service) BoardDetail(ctx context.Context, key string) (Detail, error) {
	return s.detail(ctx, "", key)
}

func (s *Service) detail(ctx context.Context, agentID, key string) (Detail, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Detail{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	var task sqlc.WorkTask
	if agentID == "" {
		task, err = boardTask(ctx, q, key)
	} else {
		task, err = legacyTask(ctx, q, agentID, key)
	}
	if err != nil {
		return Detail{}, err
	}
	var entries []sqlc.WorkTaskEntry
	if agentID == "" {
		entries, err = q.ListBoardTimeline(ctx, task.ID)
	} else {
		entries, err = q.ListWorkTaskEntries(ctx, task.ID)
	}
	if err != nil {
		return Detail{}, err
	}
	questions, err := q.ListWorkTaskQuestions(ctx, task.ID)
	if err != nil {
		return Detail{}, err
	}
	objects, err := q.ListWorkTaskObjects(ctx, task.ID)
	if err != nil {
		return Detail{}, err
	}
	waits, err := q.ListBoardTaskWaits(ctx, task.ID)
	if err != nil {
		return Detail{}, err
	}
	sessions, err := q.ListWorkTaskSessions(ctx, task.ID)
	if err != nil {
		return Detail{}, err
	}
	blockers, err := ComputeBlockers(ctx, q, task)
	if err != nil {
		return Detail{}, err
	}
	return Detail{Task: task, Entries: entries, Questions: questions, Objects: objects, Waits: waits, Sessions: sessions, Blockers: blockers}, tx.Commit()
}

// ModelTitle keeps source-provided titles out of model context. An explicit
// rename records a new title separately from the source event.
func ModelTitle(ctx context.Context, q *sqlc.Queries, task sqlc.WorkTask) (string, error) {
	if task.CreatorKind != CreatorSource {
		return task.Title, nil
	}
	renamed, err := q.HasWorkTaskTitleEntry(ctx, sqlc.HasWorkTaskTitleEntryParams{TaskID: task.ID, Title: task.Title})
	if renamed {
		return task.Title, err
	}
	return "Source Task " + FormatKey(task.Number), err
}

func legacyTask(ctx context.Context, q *sqlc.Queries, agentID, id string) (sqlc.WorkTask, error) {
	return q.GetWorkTask(ctx, sqlc.GetWorkTaskParams{ID: id, AgentID: agentID})
}

// SetLifecycle applies an explicit Owner decision without changing run state.
func (s *Service) SetLifecycle(ctx context.Context, agentID, id, lifecycle string) (task sqlc.WorkTask, err error) {
	err = s.write(ctx, func(l *Ledger, now int64) error {
		task, err = legacyTask(ctx, l.q, agentID, id)
		if err != nil {
			return err
		}
		task, err = l.SetLegacyLifecycle(ctx, task, lifecycle, now)
		return err
	})
	return task, err
}

// Append records an Owner note without starting a run.
func (s *Service) Append(ctx context.Context, agentID, id, content string) (entry sqlc.WorkTaskEntry, err error) {
	err = s.write(ctx, func(l *Ledger, now int64) error {
		task, err := legacyTask(ctx, l.q, agentID, id)
		if err != nil {
			return err
		}
		entry, err = l.PostEntry(ctx, Owner, OriginUser, task, PostEntryParams{Content: content}, now)
		return err
	})
	return entry, err
}

func (s *Service) PostEntry(ctx context.Context, actor Actor, origin Origin, taskID string, p PostEntryParams) (entry sqlc.WorkTaskEntry, err error) {
	err = s.write(ctx, func(l *Ledger, now int64) error {
		task, err := l.q.GetBoardTask(ctx, taskID)
		if err != nil {
			return err
		}
		entry, err = l.PostEntry(ctx, actor, origin, task, p, now)
		return err
	})
	return entry, err
}

func (s *Service) TransitionStatus(ctx context.Context, actor Actor, origin Origin, taskID, status string, expectedRevision *int64) (result sqlc.WorkTask, err error) {
	err = s.write(ctx, func(l *Ledger, now int64) error {
		task, err := l.q.GetBoardTask(ctx, taskID)
		if err != nil {
			return err
		}
		result, err = l.TransitionStatus(ctx, actor, origin, task, status, expectedRevision, now)
		return err
	})
	return result, err
}

// TransitionStatus applies a Board transition and records any review wake in the same transaction.
func (l *Ledger) TransitionStatus(ctx context.Context, actor Actor, origin Origin, task sqlc.WorkTask, status string, expectedRevision *int64, now int64) (sqlc.WorkTask, error) {
	action, valid := transition(task, status)
	if err := authorize(ctx, l.q, actor, origin, task, action); err != nil {
		return sqlc.WorkTask{}, err
	}
	if expectedRevision != nil && *expectedRevision != task.Revision {
		return sqlc.WorkTask{}, ErrConflict
	}
	if !valid {
		return sqlc.WorkTask{}, ErrInvalid
	}
	result, err := l.changeStatus(ctx, actor, task, status, now)
	if err == nil && status == StatusInReview && task.ReviewerKind == ActorBot {
		_, err = l.queueWake(ctx, task.ReviewerID, result, WakeReviewRequested, now)
	}
	return result, err
}

func transition(task sqlc.WorkTask, to string) (Action, bool) {
	from := task.Status
	switch {
	case to == StatusCancelled:
		return ActionOwnerLifecycle, IsOpenStatus(from)
	case !IsOpenStatus(from):
		return ActionOwnerLifecycle, to == StatusInProgress
	case to == StatusInReview:
		return ActionSubmitReview, from == StatusInProgress
	case to == StatusDone:
		return ActionTransition, from == StatusInProgress && task.ReviewerKind == ""
	case to == StatusBacklog || to == StatusReady || to == StatusInProgress:
		return ActionTransition, from != to && from != StatusInReview
	default:
		return ActionTransition, false
	}
}

func (s *Service) Review(ctx context.Context, actor Actor, origin Origin, taskID, decision string, reviewSeq int64, feedback string) (result sqlc.WorkTask, err error) {
	err = s.write(ctx, func(l *Ledger, now int64) error {
		task, err := l.q.GetBoardTask(ctx, taskID)
		if err != nil {
			return err
		}
		result, err = l.Review(ctx, actor, origin, task, decision, reviewSeq, feedback, now)
		return err
	})
	return result, err
}

// Review records the decision and any Assignee wake in the same transaction.
func (l *Ledger) Review(ctx context.Context, actor Actor, origin Origin, task sqlc.WorkTask, decision string, reviewSeq int64, feedback string, now int64) (sqlc.WorkTask, error) {
	if err := authorize(ctx, l.q, actor, origin, task, ActionReview); err != nil {
		return sqlc.WorkTask{}, err
	}
	if task.Status != StatusInReview || decision == ReviewApprove && reviewSeq != task.ReviewSeq {
		return sqlc.WorkTask{}, ErrConflict
	}
	status := StatusDone
	switch decision {
	case ReviewApprove:
	case ReviewRequestChanges:
		status = StatusInProgress
	default:
		return sqlc.WorkTask{}, ErrInvalid
	}
	var err error
	feedback = strings.TrimSpace(feedback)
	if feedback != "" || decision == ReviewRequestChanges {
		if feedback, err = Content(feedback); err != nil {
			return sqlc.WorkTask{}, err
		}
	}
	meta, err := json.Marshal(struct {
		Decision  string `json:"decision"`
		ReviewSeq int64  `json:"reviewSeq"`
		Status    string `json:"status"`
	}{decision, task.ReviewSeq, status})
	if err != nil {
		return sqlc.WorkTask{}, err
	}
	result, _, err := l.append(ctx, actor, task, status, task.Title, 0, sqlc.InsertWorkTaskEntryParams{
		Kind: KindReview, Content: feedback, MetaJson: string(meta), CreatedAt: now,
	})
	if err == nil && decision == ReviewRequestChanges {
		_, err = l.queueWake(ctx, task.AssigneeBotID, result, WakeChangesRequested, now)
	}
	return result, err
}

func (s *Service) TombstoneEntry(ctx context.Context, actor Actor, origin Origin, taskID string, seq int64) (entry sqlc.WorkTaskEntry, err error) {
	err = s.write(ctx, func(l *Ledger, now int64) error {
		task, err := l.q.GetBoardTask(ctx, taskID)
		if err != nil {
			return err
		}
		if err := authorize(ctx, l.q, actor, origin, task, ActionTombstone); err != nil {
			return err
		}
		target, err := l.q.GetWorkTaskEntry(ctx, sqlc.GetWorkTaskEntryParams{TaskID: taskID, Seq: seq})
		if err != nil {
			return err
		}
		switch target.Kind {
		case KindNote, KindArtifact, KindExternal:
		default:
			return ErrInvalid
		}
		retracted, err := l.q.IsWorkTaskEntryRetracted(ctx, sqlc.IsWorkTaskEntryRetractedParams{TaskID: taskID, TargetSeq: seq})
		if err != nil {
			return err
		}
		if retracted {
			return ErrConflict
		}
		entry, err = l.record(ctx, actor, task, sqlc.InsertWorkTaskEntryParams{Kind: KindTombstone, MetaJson: tombstoneMeta(seq), CreatedAt: now})
		return err
	})
	return entry, err
}

func (s *Service) LinkSession(ctx context.Context, actor Actor, origin Origin, taskID string, conversationID int64) error {
	return s.changeSessionLink(ctx, actor, origin, taskID, conversationID, false)
}

func (s *Service) PromotePrimarySession(ctx context.Context, actor Actor, origin Origin, taskID string, conversationID int64) error {
	return s.changeSessionLink(ctx, actor, origin, taskID, conversationID, true)
}

func (s *Service) changeSessionLink(ctx context.Context, actor Actor, origin Origin, taskID string, conversationID int64, promote bool) error {
	return s.write(ctx, func(l *Ledger, now int64) error {
		task, err := l.q.GetBoardTask(ctx, taskID)
		if err != nil {
			return err
		}
		if err := AuthorizeSessionLink(ctx, l.q, actor, origin, task, conversationID); err != nil {
			return err
		}
		if promote {
			if _, err := l.q.GetWorkTaskSession(ctx, sqlc.GetWorkTaskSessionParams{TaskID: task.ID, ConversationID: sql.NullInt64{Int64: conversationID, Valid: true}}); err != nil {
				return err
			}
		}
		return l.LinkSession(ctx, actor, task, conversationID, promote, now)
	})
}
