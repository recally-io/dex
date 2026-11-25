package chat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"time"

	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/runner/outbound"
)

// The gate orders native operations for one Session. SQLite orders receipt insertion.
type deliveryGate struct {
	token chan struct{}
	// Retry fields are protected by coordinationMu.
	timer *time.Timer
	delay time.Duration
}

func (s *Service) sessionGate(id int64) *deliveryGate {
	s.coordinationMu.Lock()
	defer s.coordinationMu.Unlock()
	gate := s.delivery[id]
	if gate == nil {
		gate = &deliveryGate{token: make(chan struct{}, 1)}
		s.delivery[id] = gate
	}
	return gate
}

func (s *Service) lockSession(ctx context.Context, id int64) (func(), error) {
	gate := s.sessionGate(id)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case gate.token <- struct{}{}:
		return func() { <-gate.token; s.Wake() }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Service) retrySession(ctx context.Context, id int64) {
	gate := s.sessionGate(id)
	s.coordinationMu.Lock()
	if s.ctx.Err() != nil || gate.timer != nil {
		s.coordinationMu.Unlock()
		return
	}
	if gate.delay == 0 {
		gate.delay = 100 * time.Millisecond
	}
	timer := time.AfterFunc(gate.delay, func() {
		s.coordinationMu.Lock()
		gate.timer = nil
		s.coordinationMu.Unlock()
		s.Wake()
	})
	gate.timer = timer
	delay := gate.delay
	gate.delay = min(delay*2, 30*time.Second)
	s.coordinationMu.Unlock()
	level := slog.LevelDebug
	if delay == 100*time.Millisecond {
		level = slog.LevelInfo
	}
	slog.Log(ctx, level, "session retry scheduled", "session", id, "delay_ms", delay.Milliseconds())
}

// Registration and cancellation share a boundary so Wait cannot miss a worker.
func (s *Service) startWorker(work func()) bool {
	s.coordinationMu.Lock()
	defer s.coordinationMu.Unlock()
	if s.ctx.Err() != nil {
		return false
	}
	s.wg.Go(work)
	return true
}

func (s *Service) dispatch(ctx context.Context) error {
	if s.leaseUntil.Load() != 0 {
		return nil
	}
	aborts, err := s.q.ListPendingExecutionAborts(ctx)
	if err != nil {
		return err
	}
	pending, err := s.q.ListPendingSubmissions(ctx, 0)
	if err != nil {
		return err
	}
	sessions := make(map[int64]struct{})
	for _, c := range aborts {
		sessions[c.ID] = struct{}{}
	}
	stops, err := s.q.ListStopPendingSessions(ctx)
	if err != nil {
		return err
	}
	for _, id := range stops {
		sessions[id] = struct{}{}
	}
	for _, p := range pending {
		if _, waiting := s.waiting.Load(p.ID); !waiting {
			sessions[p.ConversationID] = struct{}{}
		}
	}
	for id := range sessions {
		gate := s.sessionGate(id)
		s.coordinationMu.Lock()
		retrying := gate.timer != nil
		s.coordinationMu.Unlock()
		if retrying {
			continue
		}
		select {
		case gate.token <- struct{}{}:
			if !s.startWorker(func() {
				defer func() { <-gate.token; s.Wake() }()
				s.deliverSession(s.ctx, id, "")
			}) {
				<-gate.token
			}
		default:
			// The current owner wakes dispatch after release.
		}
	}
	return nil
}

func (s *Service) deliverSession(ctx context.Context, id int64, until string) {
	if err := s.reconcileSession(ctx, id, until); err != nil {
		if ctx.Err() == nil {
			slog.DebugContext(ctx, "session reconciliation deferred", "session", id, "reason", admissionRetryReason(err))
		}
		s.retrySession(ctx, id)
	}
}

func receiptTrace(id int64, request string) *slog.Logger {
	digest := sha256.Sum256([]byte(request)) // A Web caller can supply an arbitrary request ID.
	return slog.With("session", id, "request_hash", hex.EncodeToString(digest[:16]))
}

func settlementTraceReason(out engine.Outcome) string {
	if out.Status == "done" {
		return "done"
	}
	switch out.Reason {
	case "aborted", "withdrawn", "stopped", "interrupted", "no_model", "model_error", "nothing_to_answer":
		return out.Reason
	case rejectedBusy:
		return "session_busy"
	default:
		return "unknown"
	}
}

func admissionRetryReason(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, outbound.ErrOffline):
		return "environment_offline"
	case errors.Is(err, engine.ErrBusy), errors.Is(err, engine.ErrOriginBusy):
		return "session_busy"
	case errors.Is(err, engine.ErrStaleRunTarget):
		return "stale_target"
	case errors.Is(err, engine.ErrMaintenance):
		return "maintenance"
	case errors.Is(err, engine.ErrModelsPending):
		return "model_discovery"
	case errors.Is(err, errStopPending):
		return "stop_pending"
	default:
		return "unknown"
	}
}
