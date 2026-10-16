package chat

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"sync"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/worker"
)

// StopResult confirms controlled execution only. Earlier effects are not undone.
type StopResult struct {
	Confirmed bool
}

// Stop withdraws this conversation's input and stops its controlled executions.
func (s *Service) Stop(ctx context.Context, agentID string, id int64) (StopResult, error) {
	if _, err := s.Get(ctx, agentID, id); err != nil {
		return StopResult{}, err
	}
	return s.stop(ctx, agentID, id, "")
}

// StopPeer includes old contexts that still send results to the same visible peer.
func (s *Service) StopPeer(ctx context.Context, agentID, peer string) (StopResult, error) {
	if !strings.Contains(peer, ":") || strings.Contains(peer, "/") {
		return StopResult{}, ErrNotFound
	}
	return s.stop(ctx, agentID, 0, peer)
}

func (s *Service) stop(ctx context.Context, agentID string, id int64, peer string) (StopResult, error) {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	conversations, call, err := s.withdraw(ctx, agentID, id, peer)
	if err != nil {
		return StopResult{}, err
	}
	result := StopResult{Confirmed: true}
	for _, c := range conversations {
		s.signal(c.ID)
		if s.notify != nil {
			s.notify(c.ChannelKey)
		}
		// A confirmed lease has already joined all worker work and blocks admission.
		if !c.WorkerConversationID.Valid || s.leaseUntil.Load() > s.now().UnixMilli() {
			continue
		}
		if call == nil {
			result.Confirmed = false
			continue
		}
		var stopped worker.AbortResult
		callCtx, cancel := stopContext(ctx)
		err := call(callCtx, "abort", worker.AbortParams{AgentID: agentID, ConversationID: c.WorkerConversationID.Int64}, &stopped)
		cancel()
		if err != nil || !stopped.ExecutionConfirmed {
			result.Confirmed = false
		}
		if err != nil {
			slog.Warn("stop main execution", "chat", c.ID, "err", err)
		}
	}
	tasks, err := s.q.ListCancellingTasks(ctx)
	if err != nil {
		return StopResult{}, err
	}
	parents := make(map[int64]bool, len(conversations))
	for _, c := range conversations {
		parents[c.ID] = true
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, task := range tasks {
		if !parents[task.ParentConversationID] {
			continue
		}
		wg.Go(func() {
			if !s.stopTask(ctx, task) {
				mu.Lock()
				result.Confirmed = false
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	s.Wake()
	return result, nil
}

// Withdraw and spawn take the writer before reading. Whichever commits first defines Stop's scope.
func (s *Service) withdraw(ctx context.Context, agentID string, id int64, peer string) ([]sqlc.Conversation, worker.CallFunc, error) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	tx, q, err := s.taskTransaction(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	conversations, err := q.ListStopConversations(ctx, sqlc.ListStopConversationsParams{AgentID: agentID, ChatID: id, Peer: peer})
	if err != nil {
		return nil, nil, err
	}
	now := sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}
	for _, c := range conversations {
		if _, err := q.StopConversationSubmissions(ctx, sqlc.StopConversationSubmissionsParams{ChatID: c.ID, Now: now}); err != nil {
			return nil, nil, err
		}
		if err := q.StopConversationTasks(ctx, sqlc.StopConversationTasksParams{ParentID: c.ID, Now: now}); err != nil {
			return nil, nil, err
		}
	}
	// Bind before recovery can publish the next worker. Never abort across process generations.
	call, err := s.worker.Bind()
	if err != nil {
		call = nil
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return conversations, call, nil
}
