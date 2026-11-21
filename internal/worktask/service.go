// Package worktask records work independently of execution sessions.
package worktask

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// ErrInvalid means a Task command has invalid fields.
var ErrInvalid = errors.New("invalid task command")

// Service stores Tasks in the control database.
type Service struct {
	db *sql.DB
	q  *sqlc.Queries
}

// New opens a Task service on the existing control database.
func New(conn *sql.DB) *Service { return &Service{db: conn, q: sqlc.New(conn)} }

// Detail holds the current Task and its most recent timeline entries.
type Detail struct {
	Task    sqlc.WorkTask
	Entries []sqlc.WorkTaskEntry
}

// Create starts tracking work without starting a session.
func (s *Service) Create(ctx context.Context, agentID, title string) (sqlc.WorkTask, error) {
	title = strings.TrimSpace(title)
	if title == "" || utf8.RuneCountInString(title) > 160 {
		return sqlc.WorkTask{}, ErrInvalid
	}
	return s.q.CreateWorkTask(ctx, sqlc.CreateWorkTaskParams{ID: uuid.NewV7().String(), AgentID: agentID, Title: title, At: time.Now().UnixMilli()})
}

// List returns the owner's Tasks across active agents.
func (s *Service) List(ctx context.Context, includeClosed bool) ([]sqlc.WorkTask, error) {
	return s.q.ListWorkTasks(ctx, includeClosed)
}

// Detail reads the Task and timeline in one snapshot.
func (s *Service) Detail(ctx context.Context, agentID, id string) (Detail, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Detail{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	task, err := q.GetWorkTask(ctx, sqlc.GetWorkTaskParams{ID: id, AgentID: agentID})
	if err != nil {
		return Detail{}, err
	}
	entries, err := q.ListWorkTaskEntries(ctx, task.ID)
	if err != nil {
		return Detail{}, err
	}
	return Detail{Task: task, Entries: entries}, tx.Commit()
}

// SetLifecycle applies an explicit owner decision without changing run state.
func (s *Service) SetLifecycle(ctx context.Context, agentID, id, lifecycle string) (sqlc.WorkTask, error) {
	switch lifecycle {
	case "open", "completed", "cancelled":
	default:
		return sqlc.WorkTask{}, ErrInvalid
	}
	return s.q.SetWorkTaskLifecycle(ctx, sqlc.SetWorkTaskLifecycleParams{ID: id, AgentID: agentID, Lifecycle: lifecycle, UpdatedAt: time.Now().UnixMilli()})
}

// Append records an owner note without starting a run.
func (s *Service) Append(ctx context.Context, agentID, id, content string) (sqlc.WorkTaskEntry, error) {
	content = strings.TrimSpace(content)
	if content == "" || utf8.RuneCountInString(content) > 32000 {
		return sqlc.WorkTaskEntry{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return sqlc.WorkTaskEntry{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	now := time.Now().UnixMilli()
	entry, err := q.InsertWorkTaskEntry(ctx, sqlc.InsertWorkTaskEntryParams{TaskID: id, AgentID: agentID, Content: content, CreatedAt: now})
	if err != nil {
		return sqlc.WorkTaskEntry{}, err
	}
	if err := q.TouchWorkTask(ctx, sqlc.TouchWorkTaskParams{ID: id, At: now}); err != nil {
		return sqlc.WorkTaskEntry{}, err
	}
	return entry, tx.Commit()
}
