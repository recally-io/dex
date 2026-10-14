// Package chat turns a user message on a channel into a durable submission and waits for the
// worker's answer.
package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
	"uuid"

	"github.com/vaayne/dex/internal/db/sqlc"
	"github.com/vaayne/dex/internal/worker"
)

// Caller sends requests to the worker. *worker.Worker implements it.
type Caller interface {
	Call(ctx context.Context, method string, params, result any) error
}

// Reply is the outcome of one message.
type Reply struct {
	Status string `json:"status"` // "done" or "unanswered"
	Answer string `json:"answer,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Service sends messages to agents. A message is stored as a pending submission before the worker
// sees it, so it survives a worker crash: Replay sends it again after the restart, and pi-durable
// deduplicates on the submission ID.
type Service struct {
	q      *sqlc.Queries
	worker Caller
	now    func() time.Time

	// Serializes conversation creation, so two first messages of one chat create one worker
	// conversation. Global lock; per conversation if throughput matters.
	createMu sync.Mutex

	mu      sync.Mutex
	waiters map[string]chan struct{} // submission ID → closed when settled
}

// New returns a Service. Connect the worker's settled events to HandleSettled and its start to
// Replay.
func New(q *sqlc.Queries, w Caller, now func() time.Time) *Service {
	return &Service{q: q, worker: w, now: now, waiters: map[string]chan struct{}{}}
}

// Send stores content as a message to agentID on channelKey and waits for the answer until ctx is
// done. When the worker exits before it accepts the message, Send keeps waiting, because Replay
// sends the message again after the restart.
func (s *Service) Send(ctx context.Context, agentID, channelKey, content string) (Reply, error) {
	conversation, workerConversation, err := s.conversation(ctx, agentID, channelKey)
	if err != nil {
		return Reply{}, err
	}
	id := uuid.NewV7().String()
	err = s.q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{
		ID: id, ConversationID: conversation, Content: content, CreatedAt: s.now().UnixMilli(),
	})
	if err != nil {
		return Reply{}, err
	}

	// Register before submitting, so an early settled event is not lost.
	settled := s.wait(id)
	defer s.forget(id)
	err = s.submit(ctx, agentID, workerConversation, id, content)
	switch {
	case errors.Is(err, worker.ErrUnavailable):
		slog.Warn("worker exited; the message waits for replay", "submission", id)
	case err != nil:
		s.settle(ctx, worker.Settled{RequestID: id, Status: "unanswered", Reason: err.Error()})
		return Reply{Status: "unanswered", Reason: err.Error()}, nil
	}

	select {
	case <-ctx.Done():
		return Reply{}, ctx.Err()
	case <-settled:
	}
	sub, err := s.q.GetSubmission(ctx, id)
	if err != nil {
		return Reply{}, err
	}
	return Reply{Status: sub.Status, Answer: sub.Answer.String, Reason: sub.Reason.String}, nil
}

// HandleSettled records a settled event from the worker and wakes the waiting Send. When the
// record fails, the submission stays pending and the next worker start replays it.
func (s *Service) HandleSettled(ctx context.Context, event worker.Settled) {
	s.settle(ctx, event)
}

// Replay sends every pending submission to the worker again. Run it after each worker start.
func (s *Service) Replay(ctx context.Context) {
	pending, err := s.q.ListPendingSubmissions(ctx)
	if err != nil {
		slog.Error("list pending submissions", "err", err)
		return
	}
	for _, p := range pending {
		if err := s.submit(ctx, p.AgentID, p.WorkerConversationID.Int64, p.ID, p.Content); err != nil {
			slog.Error("replay submission", "submission", p.ID, "err", err)
		}
	}
}

func (s *Service) submit(ctx context.Context, agentID string, conversation int64, id, content string) error {
	var result worker.SubmitResult
	return s.worker.Call(ctx, "submit", worker.SubmitParams{
		AgentID: agentID, ConversationID: conversation, RequestID: id, Content: content,
	}, &result)
}

func (s *Service) settle(ctx context.Context, event worker.Settled) {
	err := s.q.SettleSubmission(ctx, sqlc.SettleSubmissionParams{
		ID:        event.RequestID,
		Status:    event.Status,
		Answer:    sql.NullString{String: event.Answer, Valid: event.Status == "done"},
		Reason:    sql.NullString{String: event.Reason, Valid: event.Reason != ""},
		SettledAt: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true},
	})
	if err != nil {
		slog.Error("settle submission", "submission", event.RequestID, "err", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.waiters[event.RequestID]; ok {
		close(ch)
		delete(s.waiters, event.RequestID)
	}
}

func (s *Service) wait(id string) <-chan struct{} {
	ch := make(chan struct{})
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waiters[id] = ch
	return ch
}

func (s *Service) forget(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.waiters, id)
}

// conversation returns the control and worker IDs of the conversation for channelKey, and creates
// them on first use.
func (s *Service) conversation(ctx context.Context, agentID, channelKey string) (int64, int64, error) {
	s.createMu.Lock()
	defer s.createMu.Unlock()
	conv, err := s.q.EnsureConversation(ctx, sqlc.EnsureConversationParams{
		AgentID: agentID, ChannelKey: channelKey, CreatedAt: s.now().UnixMilli(),
	})
	if err != nil {
		return 0, 0, err
	}
	if conv.WorkerConversationID.Valid {
		return conv.ID, conv.WorkerConversationID.Int64, nil
	}
	agent, err := s.q.GetAgent(ctx, agentID)
	if err != nil {
		return 0, 0, fmt.Errorf("agent %s: %w", agentID, err)
	}
	// A crash between this call and the update below leaves an empty worker conversation. That is
	// harmless; the next message creates another one.
	var created worker.CreateConversationResult
	err = s.worker.Call(ctx, "createConversation", worker.CreateConversationParams{
		AgentID:      agentID,
		Model:        worker.ModelRef{Provider: agent.ModelProvider, ModelID: agent.ModelID},
		Instructions: agent.Instructions,
	}, &created)
	if err != nil {
		return 0, 0, fmt.Errorf("create worker conversation: %w", err)
	}
	err = s.q.SetWorkerConversation(ctx, sqlc.SetWorkerConversationParams{
		WorkerConversationID: sql.NullInt64{Int64: created.ConversationID, Valid: true},
		ID:                   conv.ID,
	})
	return conv.ID, created.ConversationID, err
}
