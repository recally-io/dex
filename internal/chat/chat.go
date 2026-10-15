// Package chat stores user messages as durable submissions and delivers them to the worker.
package chat

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/dream"
	"github.com/recally-io/dex/internal/worker"
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

// Chat is one conversation on one channel.
type Chat struct {
	ID         int64
	ChannelKey string
	Title      string
	CreatedAt  int64
	UpdatedAt  int64 // time of the last message
	Busy       bool  // a message has no answer yet
	Settings   Settings
	// WorkerConversationID is 0 until the worker has the conversation.
	WorkerConversationID int64
}

// Settings are the settings that one chat overrides. An empty field follows the agent's setting.
// Provider and ModelID are both empty or both set; internal/settings validates them.
type Settings struct {
	Provider      string `json:"provider,omitempty"`
	ModelID       string `json:"modelId,omitempty"`
	ThinkingLevel string `json:"thinkingLevel,omitempty"`
}

// Message is a message to post. Title is the title of a new chat; empty uses the start of Content,
// or the name of the first attachment. Settings apply only when the message creates the chat.
type Message struct {
	ID          string
	Title       string
	Content     string
	Attachments []worker.Attachment
	Settings    Settings
}

// ErrNoModel means that the agent has no model yet; the user chooses one in the settings.
var ErrNoModel = errors.New("the agent has no model; choose one in Settings")

// ErrNotFound means that the chat does not exist.
var ErrNotFound = errors.New("chat not found")

// The chat list shows titles of at most this many characters.
const titleLength = 60

// Service sends messages to agents. A message is stored as a pending submission before the worker
// sees it, so it survives a worker crash: Replay sends it again after the restart, and pi-durable
// deduplicates on the submission ID.
type Service struct {
	q       *sqlc.Queries
	worker  Caller
	now     func() time.Time
	notify  func(channelKey string)
	changed func(chatID int64)

	// Stored submissions wait here for Run, which delivers them in order.
	queue chan sqlc.ListPendingSubmissionsRow

	// Serializes worker conversation creation, so two first messages of one chat create one worker
	// conversation. Global lock; per conversation if throughput matters.
	createMu   sync.Mutex
	deliveryMu sync.Mutex // orders quiesce, confirmed leases, submit, and abort

	maintenanceMu sync.Mutex
	leaseUntil    atomic.Int64
	reloading     bool // an extension reload holds the lease; see AcquireReload

}

// New returns a Service. Connect the worker's settled events to HandleSettled and its start to
// Replay, and run Run. notify, if not nil, receives the channel key when a submission settles.
// changed, if not nil, learns that a chat has a new message, a settled message, or a worker
// conversation. Both run on the goroutine that reads the worker, so they must not block.
func New(q *sqlc.Queries, w Caller, now func() time.Time, notify func(channelKey string), changed func(chatID int64)) *Service {
	return &Service{
		q: q, worker: w, now: now, notify: notify, changed: changed,
		queue: make(chan sqlc.ListPendingSubmissionsRow, 1024),
	}
}

// Run delivers stored submissions to the worker, one at a time and in order, until ctx is done.
// One queue for all chats; one per chat if a slow delivery delays other chats.
func (s *Service) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case p := <-s.queue:
			s.deliver(ctx, p)
		}
	}
}

// Post stores the message id with content on channelKey and returns the chat ID. It does not wait
// for the answer. A new chat gets the title title, or the start of content when title is empty.
// When the agent has no model, it stores nothing and returns ErrNoModel. A message with an ID that
// exists already is not stored again.
func (s *Service) Post(ctx context.Context, agentID, channelKey, title, id, content string) (int64, error) {
	return s.Send(ctx, agentID, channelKey, Message{ID: id, Title: title, Content: content})
}

// Send is Post for a message with attachments, or for a new chat with its own settings. When
// neither the chat nor the agent has a model, it stores nothing and returns ErrNoModel.
func (s *Service) Send(ctx context.Context, agentID, channelKey string, m Message) (int64, error) {
	agent, err := s.q.GetAgent(ctx, agentID)
	if err != nil {
		return 0, err
	}
	model := agent.ModelID
	switch c, err := s.q.GetConversationByKey(ctx, sqlc.GetConversationByKeyParams{AgentID: agentID, ChannelKey: channelKey}); {
	case errors.Is(err, sql.ErrNoRows):
		model = cmp.Or(m.Settings.ModelID, model)
	case err != nil:
		return 0, err
	default:
		model = cmp.Or(c.ModelID, model)
	}
	if model == "" {
		return 0, ErrNoModel
	}
	return s.enqueue(ctx, agentID, channelKey, "", m)
}

// SubmitMessage stores a channel update with its attachments and source identity.
func (s *Service) SubmitMessage(ctx context.Context, agentID, channelKey, sourceKey string, m Message) error {
	m.ID = uuid.NewV7().String()
	_, err := s.enqueue(ctx, agentID, channelKey, sourceKey, m)
	return err
}

func (s *Service) enqueue(ctx context.Context, agentID, channelKey, sourceKey string, m Message) (int64, error) {
	chatTitle := cmp.Or(m.Title, title(m.Content))
	if chatTitle == "" && len(m.Attachments) > 0 {
		chatTitle = title(m.Attachments[0].Name)
	}
	conv, err := s.q.EnsureConversation(ctx, sqlc.EnsureConversationParams{
		AgentID: agentID, ChannelKey: channelKey, Title: chatTitle,
		ModelProvider: m.Settings.Provider, ModelID: m.Settings.ModelID, ThinkingLevel: m.Settings.ThinkingLevel,
		CreatedAt: s.now().UnixMilli(),
	})
	if err != nil {
		return 0, err
	}
	attachments, err := json.Marshal(m.Attachments)
	if err != nil {
		return 0, err
	}
	created, err := s.q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{
		ID:             m.ID,
		ConversationID: conv.ID,
		Content:        m.Content,
		Attachments:    string(attachments),
		SourceKey:      sql.NullString{String: sourceKey, Valid: sourceKey != ""},
		CreatedAt:      s.now().UnixMilli(),
	})
	if err != nil {
		return 0, err
	}
	if created == 0 {
		return conv.ID, nil // the channel delivered this message before
	}
	s.signal(conv.ID)
	select {
	case s.queue <- sqlc.ListPendingSubmissionsRow{
		ID: m.ID, Content: m.Content, Attachments: string(attachments), ConversationID: conv.ID, AgentID: agentID,
		ChannelKey: channelKey,
	}:
	case <-ctx.Done():
		// The message is stored; the next Replay delivers it.
		return conv.ID, nil
	}
	return conv.ID, nil
}

func title(content string) string {
	t := strings.Join(strings.Fields(content), " ")
	if utf8.RuneCountInString(t) <= titleLength {
		return t
	}
	return string([]rune(t)[:titleLength-1]) + "…"
}

// List returns the chats of agentID, newest activity first.
func (s *Service) List(ctx context.Context, agentID string) ([]Chat, error) {
	rows, err := s.q.ListChats(ctx, agentID)
	if err != nil {
		return nil, err
	}
	chats := make([]Chat, len(rows))
	for i, r := range rows {
		chats[i] = Chat{
			ID: r.ID, ChannelKey: r.ChannelKey, Title: r.Title, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
			Busy: r.Pending > 0, Settings: Settings{Provider: r.ModelProvider, ModelID: r.ModelID, ThinkingLevel: r.ThinkingLevel},
		}
	}
	return chats, nil
}

// Get returns chat id of agentID, or ErrNotFound.
func (s *Service) Get(ctx context.Context, agentID string, id int64) (Chat, error) {
	r, err := s.q.GetChat(ctx, sqlc.GetChatParams{ID: id, AgentID: agentID})
	if errors.Is(err, sql.ErrNoRows) {
		return Chat{}, ErrNotFound
	}
	if err != nil {
		return Chat{}, err
	}
	return Chat{
		ID: r.ID, ChannelKey: r.ChannelKey, Title: r.Title, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		Busy: r.Pending > 0, Settings: Settings{Provider: r.ModelProvider, ModelID: r.ModelID, ThinkingLevel: r.ThinkingLevel},
		WorkerConversationID: r.WorkerConversationID.Int64,
	}, nil
}

// Abort settles pending messages as unanswered and aborts the worker run.
// Queued messages cannot enter the worker after this change.
func (s *Service) Abort(ctx context.Context, agentID string, id int64) error {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	c, err := s.Get(ctx, agentID, id)
	if err != nil {
		return err
	}
	count, err := s.q.StopChatSubmissions(ctx, sqlc.StopChatSubmissionsParams{ConversationID: id, SettledAt: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}})
	if err != nil {
		return err
	}
	if count > 0 {
		s.signal(id)
		if s.notify != nil {
			s.notify(c.ChannelKey)
		}
	}
	// A confirmed maintenance lease proves the worker has no active run. Its abort call also waits
	// for admission, so only control's queue needs to be withdrawn while the lease is held.
	if c.WorkerConversationID == 0 || s.leaseUntil.Load() > s.now().UnixMilli() {
		return nil
	}
	return s.worker.Call(ctx, "abort", worker.AbortParams{AgentID: agentID, ConversationID: c.WorkerConversationID}, &struct{}{})
}

// deliver sends a stored submission to the worker. When the worker exits first, the submission
// stays pending for Replay. Any other failure settles it as unanswered.
func (s *Service) deliver(ctx context.Context, p sqlc.ListPendingSubmissionsRow) {
	err := s.submit(ctx, p)
	switch {
	case errors.Is(err, worker.ErrUnavailable), ctx.Err() != nil:
		slog.Warn("worker exited; the message waits for replay", "submission", p.ID)
	case err != nil:
		s.settle(ctx, worker.Settled{RequestID: p.ID, Status: "unanswered", Reason: err.Error()})
	}
}

// HandleSettled records a settled event from the worker. When the record fails, the submission
// stays pending and the next worker start replays it.
func (s *Service) HandleSettled(ctx context.Context, event worker.Settled) {
	s.settle(ctx, event)
}

// Replay queues every pending submission for delivery again. Run it after each worker start.
func (s *Service) Replay(ctx context.Context) {
	pending, err := s.q.ListPendingSubmissions(ctx)
	if err != nil {
		slog.Error("list pending submissions", "err", err)
		return
	}
	for _, p := range pending {
		select {
		case s.queue <- p:
		case <-ctx.Done():
			return
		}
	}
}

func (s *Service) submit(ctx context.Context, p sqlc.ListPendingSubmissionsRow) error {
	agent, err := s.q.GetAgent(ctx, p.AgentID)
	if err != nil {
		return fmt.Errorf("agent %s: %w", p.AgentID, err)
	}
	conv, err := s.q.GetConversation(ctx, p.ConversationID)
	if err != nil {
		return err
	}
	// The chat's settings override the agent's; read both now, so a change applies from the next
	// message.
	model := worker.ModelRef{Provider: agent.ModelProvider, ModelID: agent.ModelID}
	if conv.ModelID != "" {
		model = worker.ModelRef{Provider: conv.ModelProvider, ModelID: conv.ModelID}
	}
	if model.ModelID == "" {
		return ErrNoModel
	}
	settings := worker.CreateConversationParams{
		AgentID: agent.ID, Model: model, ThinkingLevel: cmp.Or(conv.ThinkingLevel, agent.ThinkingLevel),
		Instructions: agent.Instructions,
	}
	if strings.HasPrefix(p.ChannelKey, dream.Channel) {
		settings.Instructions = dream.Instructions(settings.Instructions)
	}
	var attachments []worker.Attachment
	if err := json.Unmarshal([]byte(p.Attachments), &attachments); err != nil {
		return fmt.Errorf("submission %s attachments: %w", p.ID, err)
	}
	conversation, err := s.workerConversation(ctx, p.ConversationID, settings)
	if err != nil {
		return err
	}
	for {
		s.deliveryMu.Lock()
		if s.leaseUntil.Load() <= s.now().UnixMilli() {
			break
		}
		// Abort and lease renewal must remain available while this submission waits.
		s.deliveryMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	defer s.deliveryMu.Unlock()
	current, err := s.q.GetSubmission(ctx, p.ID)
	if err != nil {
		return err
	}
	if current.Status != "pending" {
		return nil
	}
	var result worker.SubmitResult
	return s.worker.Call(ctx, "submit", worker.SubmitParams{
		AgentID:        p.AgentID,
		ConversationID: conversation,
		Model:          model,
		ThinkingLevel:  settings.ThinkingLevel,
		Instructions:   settings.Instructions,
		RequestID:      p.ID,
		Content:        p.Content,
		Attachments:    attachments,
		Source:         source(p),
	}, &result)
}

// source names a submission's chat in the memory: dex:chat/<chat ID>, or dex:dream/<dream ID> for a
// dream, whose entries cite the chats that it read.
func source(p sqlc.ListPendingSubmissionsRow) string {
	if id, ok := strings.CutPrefix(p.ChannelKey, dream.Channel); ok {
		return "dex:dream/" + id
	}
	return fmt.Sprintf("dex:chat/%d", p.ConversationID)
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
	if changed == 0 {
		return
	}
	row, err := s.q.GetSubmissionReply(ctx, event.RequestID)
	if err != nil {
		slog.Error("read settled submission", "submission", event.RequestID, "err", err)
		return
	}
	s.signal(row.ConversationID)
	if s.notify != nil {
		s.notify(row.ChannelKey)
	}
}

func (s *Service) signal(chatID int64) {
	if s.changed != nil {
		s.changed(chatID)
	}
}

// workerConversation returns the worker's ID of the conversation, and creates the worker's
// conversation with settings on first use.
func (s *Service) workerConversation(ctx context.Context, id int64, settings worker.CreateConversationParams) (int64, error) {
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
	err = s.worker.Call(ctx, "createConversation", settings, &created)
	if err != nil {
		return 0, fmt.Errorf("create worker conversation: %w", err)
	}
	err = s.q.SetWorkerConversation(ctx, sqlc.SetWorkerConversationParams{
		WorkerConversationID: sql.NullInt64{Int64: created.ConversationID, Valid: true},
		ID:                   id,
	})
	if err == nil {
		s.signal(id)
	}
	return created.ConversationID, err
}
