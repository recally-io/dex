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
	notify func(ctx context.Context, channelKey string, reply Reply)

	// Serializes worker conversation creation, so two first messages of one chat create one worker
	// conversation. Global lock; per conversation if throughput matters.
	createMu sync.Mutex

	mu      sync.Mutex
	waiters map[string]chan struct{} // submission ID → closed when settled
}

// New returns a Service. Connect the worker's settled events to HandleSettled and its start to
// Replay. notify, if not nil, receives each reply once, when its submission settles. It runs on the
// goroutine that reads the worker, so it must not block for long.
func New(q *sqlc.Queries, w Caller, now func() time.Time, notify func(ctx context.Context, channelKey string, reply Reply)) *Service {
	return &Service{q: q, worker: w, now: now, notify: notify, waiters: map[string]chan struct{}{}}
}

// Send stores content as a message to agentID on channelKey and waits for the answer until ctx is
// done. When the worker exits before it accepts the message, Send keeps waiting, because Replay
// sends the message again after the restart.
func (s *Service) Send(ctx context.Context, agentID, channelKey, content string) (Reply, error) {
	id := uuid.NewV7().String()
	// Register before submitting, so an early settled event is not lost.
	settled := s.wait(id)
	defer s.forget(id)
	if err := s.enqueue(ctx, id, agentID, channelKey, "", content); err != nil {
		return Reply{}, err
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

// Submit stores content as a message to agentID on channelKey and returns without waiting; the
// reply goes to notify. sourceKey identifies the message on its channel: when a message with the
// same sourceKey exists, Submit does nothing, so a channel that delivers a message twice gets one
// answer.
func (s *Service) Submit(ctx context.Context, agentID, channelKey, sourceKey, content string) error {
	return s.enqueue(ctx, uuid.NewV7().String(), agentID, channelKey, sourceKey, content)
}

// errNoModel means that the agent has no model yet; the user chooses one in the settings.
var errNoModel = errors.New("the agent has no model; choose one in Settings")

func (s *Service) enqueue(ctx context.Context, id, agentID, channelKey, sourceKey, content string) error {
	conv, err := s.q.EnsureConversation(ctx, sqlc.EnsureConversationParams{
		AgentID: agentID, ChannelKey: channelKey, CreatedAt: s.now().UnixMilli(),
	})
	if err != nil {
		return err
	}
	created, err := s.q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{
		ID:             id,
		ConversationID: conv.ID,
		Content:        content,
		SourceKey:      sql.NullString{String: sourceKey, Valid: sourceKey != ""},
		CreatedAt:      s.now().UnixMilli(),
	})
	if err != nil {
		return err
	}
	if created == 0 {
		return nil // the channel delivered this message before
	}
	s.deliver(ctx, sqlc.ListPendingSubmissionsRow{ID: id, Content: content, ConversationID: conv.ID, AgentID: agentID})
	return nil
}

// deliver sends a stored submission to the worker. When the worker exits first, the submission
// stays pending for Replay. Any other failure settles it as unanswered.
func (s *Service) deliver(ctx context.Context, p sqlc.ListPendingSubmissionsRow) {
	err := s.submit(ctx, p)
	switch {
	case errors.Is(err, worker.ErrUnavailable):
		slog.Warn("worker exited; the message waits for replay", "submission", p.ID)
	case err != nil:
		s.settle(ctx, worker.Settled{RequestID: p.ID, Status: "unanswered", Reason: err.Error()})
	}
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
		s.deliver(ctx, p)
	}
}

func (s *Service) submit(ctx context.Context, p sqlc.ListPendingSubmissionsRow) error {
	agent, err := s.q.GetAgent(ctx, p.AgentID)
	if err != nil {
		return fmt.Errorf("agent %s: %w", p.AgentID, err)
	}
	if agent.ModelID == "" {
		return errNoModel
	}
	model := worker.ModelRef{Provider: agent.ModelProvider, ModelID: agent.ModelID}
	conversation, err := s.workerConversation(ctx, p.ConversationID, agent, model)
	if err != nil {
		return err
	}
	var result worker.SubmitResult
	return s.worker.Call(ctx, "submit", worker.SubmitParams{
		AgentID:        p.AgentID,
		ConversationID: conversation,
		Model:          model,
		Instructions:   agent.Instructions,
		RequestID:      p.ID,
		Content:        p.Content,
	}, &result)
}

func (s *Service) settle(ctx context.Context, event worker.Settled) {
	// The worker sends settled again when Replay resends a submission it already settled; only the
	// first event changes the row.
	changed, err := s.q.SettleSubmission(ctx, sqlc.SettleSubmissionParams{
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
	if ch, ok := s.waiters[event.RequestID]; ok {
		close(ch)
		delete(s.waiters, event.RequestID)
	}
	s.mu.Unlock()

	if changed == 0 || s.notify == nil {
		return
	}
	row, err := s.q.GetSubmissionReply(ctx, event.RequestID)
	if err != nil {
		slog.Error("read settled submission", "submission", event.RequestID, "err", err)
		return
	}
	s.notify(ctx, row.ChannelKey, Reply{Status: row.Status, Answer: row.Answer.String, Reason: row.Reason.String})
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

// workerConversation returns the worker's ID of the conversation, and creates the worker's
// conversation on first use.
func (s *Service) workerConversation(ctx context.Context, id int64, agent sqlc.Agent, model worker.ModelRef) (int64, error) {
	s.createMu.Lock()
	defer s.createMu.Unlock()
	conv, err := s.q.GetConversation(ctx, id)
	if err != nil {
		return 0, err
	}
	if conv.WorkerConversationID.Valid {
		return conv.WorkerConversationID.Int64, nil
	}
	// A crash between this call and the update below leaves an empty worker conversation. That is
	// harmless; the next message creates another one.
	var created worker.CreateConversationResult
	err = s.worker.Call(ctx, "createConversation", worker.CreateConversationParams{
		AgentID: agent.ID, Model: model, Instructions: agent.Instructions,
	}, &created)
	if err != nil {
		return 0, fmt.Errorf("create worker conversation: %w", err)
	}
	err = s.q.SetWorkerConversation(ctx, sqlc.SetWorkerConversationParams{
		WorkerConversationID: sql.NullInt64{Int64: created.ConversationID, Valid: true},
		ID:                   id,
	})
	return created.ConversationID, err
}
