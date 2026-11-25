package chat

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/runner"
)

// FireDueMonitors uses the scheduler's single job. A persisted script claim with no
// live job means execution may have started before a restart. Do not replay it.
func (s *Service) FireDueMonitors(ctx context.Context) error {
	if !s.monitorFireMu.TryLock() {
		return nil
	}
	defer s.monitorFireMu.Unlock()
	if s.leaseUntil.Load() != 0 {
		return nil
	}
	due, err := s.q.ListDueMonitors(ctx, s.now().UnixMilli())
	if err != nil {
		return err
	}
	var errs []error
	for _, row := range due {
		if ctx.Err() != nil {
			break
		}
		if err := s.fireMonitor(ctx, row.ID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type monitorResult struct {
	hash     string
	terminal bool
	err      error
}

func (s *Service) fireMonitor(ctx context.Context, id string) error {
	row, target, err := s.claimMonitor(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if target.ID == 0 {
		return nil
	} // already settled in the writer

	var env map[string]string
	if err := json.Unmarshal([]byte(row.CheckEnv), &env); err != nil {
		return s.finishMonitor(ctx, row, monitorResult{err: errors.New("stored monitor env is invalid")})
	}
	runFor := engine.MonitorTimeout
	if row.ExpiresAt > 0 {
		runFor = min(runFor, time.UnixMilli(row.ExpiresAt).Sub(s.now()))
	}
	checkCtx, cancel := context.WithTimeout(ctx, runFor)
	defer cancel()
	verify := func(ctx context.Context) error {
		current, err := s.q.GetMonitor(ctx, row.ID)
		if err != nil {
			return err
		}
		if current.State != "scheduled" || current.Runs != row.Runs {
			return errMonitorTarget
		}
		if s.leaseUntil.Load() != 0 || (row.ExpiresAt > 0 && s.now().UnixMilli() >= row.ExpiresAt) {
			return errMonitorTarget
		}
		_, err = monitorTarget(ctx, s.q, current)
		return err
	}
	hash, terminal, checkErr := s.engine.MonitorCheck(checkCtx, engine.Ref{AgentID: row.AgentID, ID: target.ExecutionID.Int64}, target.Cwd, row.CheckCommand, env, verify)

	if ctx.Err() != nil {
		// Leave the durable claim for startup to settle as interrupted. Never replay an
		// ambiguous shell operation, and do not dispatch a late event during shutdown.
		return ctx.Err()
	}
	return s.finishMonitor(ctx, row, monitorResult{hash: hash, terminal: terminal, err: checkErr})
}

func (s *Service) claimMonitor(ctx context.Context, id string) (sqlc.Monitor, sqlc.Conversation, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return sqlc.Monitor{}, sqlc.Conversation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := q.GetMonitor(ctx, id)
	if err != nil || row.State != "scheduled" {
		return row, sqlc.Conversation{}, err
	}
	now := s.now()
	params := monitorResultParams(row, now)
	target, err := monitorTarget(ctx, q, row)
	switch {
	case errors.Is(err, errMonitorTarget):
		params.State, params.Outcome = "cancelled", errMonitorTarget.Error()
	case err != nil:
		return row, sqlc.Conversation{}, err
	case row.Running != 0:
		params.State, params.Outcome = "cancelled", "check interrupted before a result was saved"
	case row.ExpiresAt != 0 && now.UnixMilli() >= row.ExpiresAt:
		params.State, params.Outcome = "expired", "time limit reached"
	case row.DueAt > now.UnixMilli() || s.leaseUntil.Load() != 0:
		return row, sqlc.Conversation{}, nil
	default:
		if row.CheckCommand != "" {
			n, err := q.ClaimMonitor(ctx, sqlc.ClaimMonitorParams{ID: row.ID, Now: now.UnixMilli()})
			if err != nil || n != 1 {
				return row, sqlc.Conversation{}, err
			}
			row.Runs++
			row.Running = 1
		}
		return row, target, tx.Commit()
	}
	if _, err := q.FinishMonitor(ctx, params); err != nil {
		return row, sqlc.Conversation{}, err
	}
	if err := tx.Commit(); err != nil {
		return row, sqlc.Conversation{}, err
	}
	s.signal(row.ConversationID)
	return row, sqlc.Conversation{}, nil
}

func monitorResultParams(row sqlc.Monitor, now time.Time) sqlc.FinishMonitorParams {
	return sqlc.FinishMonitorParams{ID: row.ID, Runs: row.Runs, NewRuns: row.Runs, State: row.State, DueAt: row.DueAt, OutputHash: row.OutputHash, UpdatedAt: now.UnixMilli()}
}

func (s *Service) finishMonitor(ctx context.Context, row sqlc.Monitor, result monitorResult) error {
	// Order event insertion with admission and native withdrawal. The shell never holds this gate.
	unlock, err := s.lockSession(ctx, row.ConversationID)
	if err != nil {
		return err
	}
	defer unlock()
	s.admissionMu.RLock()
	defer s.admissionMu.RUnlock()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := q.GetMonitor(ctx, row.ID)
	if err != nil || current.State != "scheduled" || current.Runs != row.Runs {
		return err
	}
	now := s.now()
	params := monitorResultParams(row, now)
	_, err = monitorTarget(ctx, q, current)
	var content string
	switch {
	case errors.Is(err, errMonitorTarget):
		params.State, params.Outcome = "cancelled", errMonitorTarget.Error()
	case err != nil:
		return err
	case row.ExpiresAt > 0 && now.UnixMilli() >= row.ExpiresAt:
		params.State, params.Outcome = "expired", "time limit reached"
	case s.leaseUntil.Load() != 0:
		if row.Running == 0 {
			return nil
		}
		params.State, params.Outcome = "cancelled", "check interrupted by maintenance"
	case result.err != nil:
		params.State, params.Outcome = "cancelled", monitorFailure(result.err)
	default:
		if row.Running == 0 {
			params.NewRuns++
		}
		switch {
		case row.ExpiresAt == 0 && row.MaxRuns == 1 && now.Sub(time.UnixMilli(row.DueAt)) > monitorFireGraceWindow:
			params.State, params.Outcome = "expired", "missed the 1h grace window"
			content = fmt.Sprintf("[Monitor expired past 1h grace window: %s]\nOriginal prompt: %s", row.ID, row.Prompt)
		case row.CheckCommand == "":
			params.Outcome = "prompt queued"
			content = fmt.Sprintf("[Monitor due: %s]\n%s", row.ID, row.Prompt)
		case result.terminal:
			params.State, params.Outcome = "fired", "terminal check"
			content = fmt.Sprintf("[Monitor terminal check: %s]\n%s", row.ID, row.Prompt)
		case row.OutputHash == "":
			params.Outcome = "baseline recorded"
		case result.hash != row.OutputHash:
			params.Outcome = "check changed"
			content = fmt.Sprintf("[Monitor check changed: %s]\n%s", row.ID, row.Prompt)
		default:
			params.Outcome = "no change"
		}
		params.OutputHash = result.hash
		if params.State == "scheduled" {
			if params.NewRuns >= row.MaxRuns {
				params.State = "fired"
			} else {
				params.DueAt = now.Add(time.Duration(row.IntervalSeconds) * time.Second).UnixMilli()
			}
		}
	}
	if content != "" {
		params.FireSubmissionID = sql.NullString{String: fmt.Sprintf("monitor:%s:%d", row.ID, params.NewRuns), Valid: true}
		if _, err := q.CreateMonitorSubmission(ctx, sqlc.CreateMonitorSubmissionParams{ID: params.FireSubmissionID.String, MonitorID: row.ID, Content: content, CreatedAt: now.UnixMilli()}); err != nil {
			return err
		}
	}
	if _, err := q.FinishMonitor(ctx, params); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.signal(row.ConversationID)
	if content != "" {
		s.Wake()
	}
	return nil
}

func monitorFailure(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "check failed: timed out"
	case errors.Is(err, runner.ErrUnavailable):
		return "check failed: target environment unavailable or offline"
	case errors.Is(err, context.Canceled):
		return "check failed: interrupted"
	case errors.Is(err, errMonitorTarget), errors.Is(err, engine.ErrNotFound):
		return errMonitorTarget.Error()
	default:
		return "check failed: " + err.Error()
	}
}
