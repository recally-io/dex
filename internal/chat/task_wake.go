package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/worktask"
)

// snapshotTaskWake runs only before a new native admission. A replay must keep
// the snapshot of the first successful native commit, even after a restart.
func (s *Service) snapshotTaskWake(ctx context.Context, id string) (string, error) {
	content, err := s.q.SnapshotTaskWakeAdmission(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		if err := s.checkAdmission(ctx, id); err != nil {
			return "", err
		}
		return "", fmt.Errorf("pending Task wake has no admission snapshot: %w", err)
	}
	return content, err
}

// markTaskWakeAdmitted frees the pending slot and retains any update that arrived
// after the snapshot. It does not hold the control writer across a native call.
func (s *Service) markTaskWakeAdmitted(ctx context.Context, id string) error {
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	latest, err := q.GetSubmission(ctx, id)
	if err != nil || latest.Status != "pending" || latest.AdmittedAt.Valid {
		return err
	}
	snapshot, err := q.TakeTaskWakeAdmissionSnapshot(ctx, id)
	if err != nil {
		return fmt.Errorf("read Task wake admission snapshot: %w", err)
	}
	now := s.now().UnixMilli()
	if err := q.MarkTaskWakeAdmitted(ctx, sqlc.MarkTaskWakeAdmittedParams{
		ID: id, AdmittedAt: sql.NullInt64{Int64: now, Valid: true}, Content: snapshot.Content,
	}); err != nil {
		return err
	}
	changed := latest.Content != snapshot.Content
	if changed {
		// The later cursor can include more events for an existing key and reason.
		// Requeue the full pointer set so no reason is lost at the boundary.
		if err := worktask.RequeueTaskWake(ctx, q, latest, now); err != nil {
			return err
		}
		if err := q.ClearTaskWakeAssociation(ctx, id); err != nil {
			return err
		}
		if snapshot.TaskID.Valid {
			if err := q.AssociateWorkTaskInput(ctx, sqlc.AssociateWorkTaskInputParams{
				SubmissionID: id, TaskID: snapshot.TaskID.String,
			}); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if changed {
		s.Wake()
	}
	return nil
}
