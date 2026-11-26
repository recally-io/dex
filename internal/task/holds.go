package task

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// ListHeld returns the newest 100 events that wait for the owner.
func (s *Service) ListHeld(ctx context.Context) ([]sqlc.ListHeldTaskSourceEventsRow, error) {
	return s.q.ListHeldTaskSourceEvents(ctx)
}

// AttachHeld adds a held event to the timeline of an open Task and links its free objects there,
// so later events of those objects route to the Task. An object linked to another Task keeps its link.
// The owner decision does not resolve a wait or alert Main, because the event arrived before it.
func (s *Service) AttachHeld(ctx context.Context, eventID, botID, taskID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	ledger := NewLedger(q)
	now := time.Now().UnixMilli()
	// The decision is the first write, so the transaction takes the SQLite writer before it reads.
	if err := decideHold(ctx, q, eventID, "attached", now); err != nil {
		return err
	}
	event, err := q.GetTaskSourceEvent(ctx, eventID)
	if err != nil {
		return err
	}
	task, err := q.GetAssignedTask(ctx, sqlc.GetAssignedTaskParams{ID: taskID, BotID: botID})
	if err != nil {
		return err
	}
	if !IsOpenStatus(task.Status) {
		return ErrInvalid
	}
	var objects []Object
	if err := json.Unmarshal([]byte(event.Objects), &objects); err != nil {
		return err
	}
	for _, object := range objects {
		if err := linkObject(ctx, q, botID, taskID, event.Namespace, object); err != nil && !errors.Is(err, ErrObjectConflict) {
			return err
		}
	}
	if err := q.SetTaskSourceEventTask(ctx, sqlc.SetTaskSourceEventTaskParams{TaskID: sql.NullString{String: taskID, Valid: true}, ID: eventID}); err != nil {
		return err
	}
	if err := ledger.recordExternal(ctx, task, event.Content, eventID, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.Notify(ledger)
	return nil
}

// DismissHeld removes a held event from Board. The event stays recorded.
func (s *Service) DismissHeld(ctx context.Context, eventID string) error {
	return decideHold(ctx, s.q, eventID, "dismissed", time.Now().UnixMilli())
}

func decideHold(ctx context.Context, q *sqlc.Queries, eventID, decision string, now int64) error {
	n, err := q.DecideTaskSourceHold(ctx, sqlc.DecideTaskSourceHoldParams{
		EventID: eventID, Decision: sql.NullString{String: decision, Valid: true}, DecidedAt: sql.NullInt64{Int64: now, Valid: true},
	})
	if err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return err
}
