package chat

import (
	"context"
	"log/slog"
)

// Each agent has one delivery owner. Receipts stay in SQLite while callers wait.
type deliveryGate chan struct{}

func (s *Service) agentGate(id string) deliveryGate {
	s.coordinationMu.Lock()
	defer s.coordinationMu.Unlock()
	gate := s.delivery[id]
	if gate == nil {
		gate = make(deliveryGate, 1)
		s.delivery[id] = gate
	}
	return gate
}

func (s *Service) lockAgent(ctx context.Context, id string) (func(), error) {
	gate := s.agentGate(id)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case gate <- struct{}{}:
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
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
	pending, err := s.q.ListPendingSubmissions(ctx)
	if err != nil {
		return err
	}
	agents := make(map[string]struct{})
	for _, c := range aborts {
		agents[c.AgentID] = struct{}{}
	}
	for _, p := range pending {
		if _, waiting := s.waiting.Load(p.ID); !waiting {
			agents[p.AgentID] = struct{}{}
		}
	}
	for id := range agents {
		gate := s.agentGate(id)
		select {
		case gate <- struct{}{}:
			if !s.startWorker(func() {
				defer func() { <-gate }()
				if err := s.reconcileAgent(s.ctx, id); err != nil && s.ctx.Err() == nil {
					slog.Warn("reconcile agent receipts", "agent", id, "err", err)
					s.scheduleRetry()
				}
			}) {
				<-gate
			}
		default:
			// A wake can arrive while this agent's worker is still running.
			s.scheduleRetry()
		}
	}
	return nil
}
