// Package chat owns business receipts and native execution admission.
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
	"github.com/recally-io/dex/internal/engine"
)

// Reply is the outcome of one message.
type Reply struct {
	Status string `json:"status"` // "done" or "unanswered"
	Answer string `json:"answer,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Chat is one conversation on one channel.
type Chat struct {
	Cwd        string
	ID         int64
	ChannelKey string
	Title      string
	CreatedAt  int64
	UpdatedAt  int64 // latest conversation, input, or task activity
	Busy       bool  // a message has no answer yet
	ArchivedAt int64 // zero when the Chat session is not archived
	Settings   Settings
	// ExecutionID is zero until native execution creates the conversation.
	ExecutionID int64
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
	Attachments []engine.Attachment
	Settings    Settings
}

// ErrNoModel means that the bot has no model yet; the user chooses one in the settings.
var ErrNoModel = engine.ErrNoModel

// ErrNotFound means that the chat does not exist.
var ErrNotFound = errors.New("chat not found")

// ErrRejected identifies a saved input that native execution did not admit.
var ErrRejected = errors.New("input was rejected")

// The chat list shows titles of at most this many characters.
const titleLength = 60

// Service stores receipts and projects native execution outcomes.
type Service struct {
	q       *sqlc.Queries
	engine  *engine.Agents
	now     func() time.Time
	notify  func(origin string)
	changed func(chatID int64)

	// The database holds work. A wake can coalesce without losing a submission.
	wake              chan struct{}
	waiting           sync.Map // deduplicates Wait goroutines; PiG owns execution state
	ctx               context.Context
	cancel            context.CancelFunc
	wg                sync.WaitGroup
	db                *sql.DB
	taskMu            sync.Mutex // task creation and settlement; per agent if throughput matters
	checkTaskSettings func(context.Context, Settings) error

	admissionMu    sync.RWMutex // receipt commits and maintenance lease confirmation only
	delivery       map[string]deliveryGate
	coordinationMu sync.Mutex // delivery registry, worker registration, and Run shutdown

	retryMu    sync.Mutex
	retryTimer *time.Timer
	retryDelay time.Duration

	maintenanceMu    sync.Mutex
	maintenanceTimer *time.Timer
	leaseUntil       atomic.Int64
}

// New connects durable business receipts to native execution.
func New(db *sql.DB, agents *engine.Agents, now func() time.Time, notify func(string), changed func(int64)) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{db: db, q: sqlc.New(db), engine: agents, now: now, notify: notify, changed: changed, wake: make(chan struct{}, 1), ctx: ctx, cancel: cancel, delivery: make(map[string]deliveryGate)}
}

// Run reconciles receipts at startup and after a failed cross-store write.
func (s *Service) Run(ctx context.Context) {
	defer func() {
		s.coordinationMu.Lock()
		s.cancel()
		s.coordinationMu.Unlock()
		s.retryMu.Lock()
		if s.retryTimer != nil {
			s.retryTimer.Stop()
		}
		s.retryMu.Unlock()
		s.maintenanceMu.Lock()
		if s.maintenanceTimer != nil {
			s.maintenanceTimer.Stop()
		}
		s.maintenanceMu.Unlock()
		s.wg.Wait()
	}()
	for {
		err := s.dispatch(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Warn("reconcile business receipts", "err", err)
			s.scheduleRetry()
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		}
	}
}

func (s *Service) scheduleRetry() {
	s.retryMu.Lock()
	defer s.retryMu.Unlock()
	if s.ctx.Err() != nil || s.retryTimer != nil {
		return
	}
	if s.retryDelay == 0 {
		s.retryDelay = 100 * time.Millisecond
	}
	s.retryTimer = time.AfterFunc(s.retryDelay, func() { s.retryMu.Lock(); s.retryTimer = nil; s.retryMu.Unlock(); s.Wake() })
	s.retryDelay = min(s.retryDelay*2, 30*time.Second)
}

func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// reconcileAgent runs with the agent delivery gate held.
func (s *Service) reconcileAgent(ctx context.Context, agentID string) error {
	aborts, err := s.q.ListPendingExecutionAborts(ctx)
	if err != nil {
		return err
	}
	blocked := make(map[int64]bool)
	var errs []error
	for _, c := range aborts {
		if c.AgentID != agentID {
			continue
		}
		completed, err := s.abort(ctx, engine.Ref{AgentID: c.AgentID, ID: c.ExecutionID.Int64})
		blocked[c.ID] = !completed
		if err != nil {
			errs = append(errs, err)
		}
	}
	pending, err := s.q.ListPendingSubmissions(ctx)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, p := range pending {
		if p.AgentID != agentID {
			continue
		}
		if blocked[p.ConversationID] {
			continue
		}
		if _, ok := s.waiting.Load(p.ID); ok {
			continue
		}
		if err := s.admit(ctx, p); err != nil {
			blocked[p.ConversationID] = true
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
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
	return s.enqueue(ctx, agentID, channelKey, "", inputOrigin(channelKey), m)
}

// SubmitMessage stores a channel update with its attachments and source identity.
func (s *Service) SubmitMessage(ctx context.Context, agentID, peer, sourceKey string, m Message) error {
	if _, err := s.Main(ctx, agentID); err != nil {
		return err
	}
	m.ID = uuid.NewV7().String()
	m.Settings = Settings{}
	_, err := s.enqueue(ctx, agentID, MainKey, sourceKey, peer, m)
	return err
}

func (s *Service) enqueue(ctx context.Context, agentID, channelKey, sourceKey, origin string, m Message) (int64, error) {
	complete := false
	chatID, err := func() (int64, error) {
		s.admissionMu.RLock()
		defer s.admissionMu.RUnlock()
		// Known maintenance rejection must not create a web receipt that executes later.
		if origin == "web" && s.leaseUntil.Load() != 0 {
			return 0, engine.ErrMaintenance
		}
		// SQLite row order defines input order, including callers waiting for delivery.
		createdAt, submittedAt := s.now().UnixMilli(), s.now().UnixMilli()
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return 0, err
		}
		defer func() { _ = tx.Rollback() }()
		q := s.q.WithTx(tx)
		chatTitle := cmp.Or(m.Title, title(m.Content))
		if chatTitle == "" && len(m.Attachments) > 0 {
			chatTitle = title(m.Attachments[0].Name)
		}
		conv, err := q.EnsureConversation(ctx, sqlc.EnsureConversationParams{
			AgentID: agentID, ChannelKey: channelKey, Title: chatTitle,
			ModelProvider: m.Settings.Provider, ModelID: m.Settings.ModelID, ThinkingLevel: m.Settings.ThinkingLevel,
			CreatedAt: createdAt,
		})
		if err != nil {
			return 0, err
		}
		attachments, err := json.Marshal(m.Attachments)
		if err != nil {
			return 0, err
		}
		created, err := q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{
			ID:             m.ID,
			ConversationID: conv.ID,
			Content:        m.Content,
			Attachments:    string(attachments),
			SourceKey:      sql.NullString{String: sourceKey, Valid: sourceKey != ""},
			Origin:         origin,
			Kind:           "message",
			CreatedAt:      submittedAt,
		})
		if err != nil {
			return 0, err
		}
		if created > 0 && strings.HasPrefix(channelKey, ChatPrefix) {
			if err := q.SetFirstChatTitle(ctx, sqlc.SetFirstChatTitleParams{ID: conv.ID, Title: chatTitle}); err != nil {
				return 0, err
			}
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		if created == 0 {
			id := m.ID
			if sourceKey != "" {
				id, err = s.q.GetSubmissionBySource(ctx, sql.NullString{String: sourceKey, Valid: true})
				if err != nil {
					return 0, err
				}
			}
			prior, err := s.q.GetSubmission(ctx, id)
			if err != nil {
				return 0, err
			}
			conv.ID = prior.ConversationID
			m.ID = prior.ID
			if prior.Status != "pending" || prior.AdmittedAt.Valid {
				complete = true
				return conv.ID, receiptError(prior)
			}
		}
		return conv.ID, nil
	}()
	if err != nil || complete {
		return chatID, err
	}

	s.signal(chatID)
	unlock, err := s.lockAgent(ctx, agentID)
	if err != nil {
		s.scheduleRetry()
		return chatID, nil
	}
	defer func() { unlock(); s.Wake() }()
	admissionErr := s.reconcileAgent(ctx, agentID)
	if admissionErr != nil {
		s.scheduleRetry()
	}
	sub, err := s.q.GetSubmission(ctx, m.ID)
	if err != nil {
		// The committed intent remains ours to reconcile if this read fails.
		s.scheduleRetry()
		return chatID, nil
	}
	return chatID, receiptError(sub)
}

func receiptError(sub sqlc.Submission) error {
	if !sub.AdmittedAt.Valid && sub.Status == "unanswered" && sub.Reason.String != "stopped" && sub.Reason.String != "interrupted" {
		return fmt.Errorf("%w: %s", ErrRejected, sub.Reason.String)
	}
	return nil
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
			Cwd: r.Cwd, ID: r.ID, ChannelKey: r.ChannelKey, Title: r.Title, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
			Busy: r.Pending > 0, ArchivedAt: r.ArchivedAt.Int64, Settings: Settings{Provider: r.ModelProvider, ModelID: r.ModelID, ThinkingLevel: r.ThinkingLevel},
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
		Cwd: r.Cwd, ID: r.ID, ChannelKey: r.ChannelKey, Title: r.Title, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		Busy: r.Pending > 0, ArchivedAt: r.ArchivedAt.Int64, Settings: Settings{Provider: r.ModelProvider, ModelID: r.ModelID, ThinkingLevel: r.ThinkingLevel},
		ExecutionID: r.ExecutionID.Int64,
	}, nil
}

// admit crosses the durable admission boundary with a stable request ID.
func (s *Service) admit(ctx context.Context, p sqlc.ListPendingSubmissionsRow) error {
	if s.leaseUntil.Load() != 0 {
		return nil
	}
	agent, err := s.q.GetAgent(ctx, p.AgentID)
	if err != nil {
		return err
	}
	conv, err := s.q.GetConversation(ctx, p.ConversationID)
	if err != nil {
		return err
	}
	options := engine.Options{Cwd: conv.Cwd, Model: engine.ModelRef{Provider: agent.ModelProvider, ModelID: agent.ModelID}, ThinkingLevel: cmp.Or(conv.ThinkingLevel, agent.ThinkingLevel), Instructions: agent.Instructions}
	if conv.ModelID != "" {
		options.Model = engine.ModelRef{Provider: conv.ModelProvider, ModelID: conv.ModelID}
	}
	if strings.HasPrefix(p.ChannelKey, dream.Channel) {
		options.Instructions = dream.Instructions(p.AgentID, options.Instructions)
	}
	background := strings.HasPrefix(p.ChannelKey, "task:")
	if background {
		options.Instructions = taskInstructions(options.Instructions)
	}
	ref, err := s.execution(ctx, conv.ID, p.AgentID, options)
	if err != nil {
		return err
	}
	current, err := s.q.GetSubmission(ctx, p.ID)
	if err != nil {
		return err
	}
	if current.Status != "pending" {
		return nil
	}
	if background {
		s.taskMu.Lock()
		task, e := s.q.GetTask(ctx, p.ID)
		if e == nil && task.Status == "queued" {
			_, e = s.q.StartTask(ctx, sqlc.StartTaskParams{ID: p.ID, Now: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}})
		}
		s.taskMu.Unlock()
		if e != nil {
			return e
		}
		if task.Status != "queued" && task.Status != "running" {
			return nil
		}
	}
	if p.Kind == "context_reset" {
		if err = s.engine.Reset(ctx, ref, p.ID); err != nil {
			return s.admissionError(ctx, p.ID, err)
		}
		if err = s.markAdmitted(ctx, p.ID); err != nil {
			return err
		}
		return s.settle(ctx, p.ID, engine.Outcome{Status: "done"})
	}
	var attachments []engine.Attachment
	if err = json.Unmarshal([]byte(p.Attachments), &attachments); err != nil {
		return s.settle(ctx, p.ID, engine.Outcome{Status: "unanswered", Reason: err.Error()})
	}
	input := engine.Input{RequestID: p.ID, Content: p.Content, Kind: p.Kind, Origin: p.Origin, Source: source(p), Attachments: attachments, Options: options, Background: background}
	if p.Kind == "task_result" {
		label, e := s.q.GetTaskResultLabel(ctx, sqlc.GetTaskResultLabelParams{ResultID: p.ID, ChatID: p.ConversationID})
		if e != nil {
			return e
		}
		input.TaskID, input.TaskTitle = label.ID, label.Title
	}
	sub, err := s.engine.Submit(ctx, ref, input)
	if err != nil {
		return s.admissionError(ctx, p.ID, err)
	}
	if err = s.markAdmitted(ctx, p.ID); err != nil {
		return err
	}
	if _, loaded := s.waiting.LoadOrStore(p.ID, struct{}{}); loaded {
		return nil
	}
	if !s.startWorker(func() {
		outcome, err := sub.Wait(s.ctx)
		if err == nil {
			err = s.settle(s.ctx, p.ID, outcome)
		}
		s.waiting.Delete(p.ID)
		if err != nil && s.ctx.Err() == nil {
			slog.Error("reconcile execution receipt", "submission", p.ID, "err", err)
			s.scheduleRetry()
		}
	}) {
		s.waiting.Delete(p.ID)
	}
	return nil
}

func (s *Service) markAdmitted(ctx context.Context, id string) error {
	return s.q.AdmitSubmission(ctx, sqlc.AdmitSubmissionParams{ID: id, AdmittedAt: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}})
}
func (s *Service) admissionError(ctx context.Context, id string, err error) error {
	if errors.Is(err, engine.ErrInvalidInput) || errors.Is(err, engine.ErrNotFound) || errors.Is(err, ErrNoModel) {
		return s.settle(ctx, id, engine.Outcome{Status: "unanswered", Reason: err.Error()})
	}
	return err
}

// source names a submission's chat in memory: dex:<agent ID>/chat/<chat ID>, or
// dex:<agent ID>/dream/<dream ID> for a
// dream, whose entries cite the chats that it read.
func source(p sqlc.ListPendingSubmissionsRow) string {
	if id, ok := strings.CutPrefix(p.ChannelKey, dream.Channel); ok {
		return "dex:" + p.AgentID + "/dream/" + id
	}
	return fmt.Sprintf("dex:%s/chat/%d", p.AgentID, p.ConversationID)
}

func (s *Service) settle(ctx context.Context, id string, event engine.Outcome) error {
	handled, err := s.settleTask(ctx, id, event)
	if err != nil {
		return err
	}
	if handled {
		return nil
	}
	changed, err := s.q.SettleSubmission(ctx, sqlc.SettleSubmissionParams{ID: id, Status: event.Status, Answer: sql.NullString{String: event.Answer, Valid: event.Status == "done"}, Reason: sql.NullString{String: event.Reason, Valid: event.Reason != ""}, SettledAt: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}})
	if err != nil || changed == 0 {
		return err
	}
	reply, err := s.q.GetSubmissionReply(ctx, id)
	if err != nil {
		return err
	}
	s.retryMu.Lock()
	s.retryDelay = 0
	s.retryMu.Unlock()
	s.signal(reply.ConversationID)
	if s.notify != nil {
		s.notify(reply.Origin)
	}
	return nil
}

func (s *Service) signal(chatID int64) {
	if s.changed != nil {
		s.changed(chatID)
	}
}

// execution creates the native conversation once for the business record.
func (s *Service) execution(ctx context.Context, id int64, agentID string, options engine.Options) (engine.Ref, error) {
	conv, err := s.q.GetConversation(ctx, id)
	if err != nil {
		return engine.Ref{}, err
	}
	if conv.ExecutionID.Valid {
		return engine.Ref{AgentID: agentID, ID: conv.ExecutionID.Int64}, nil
	}
	ref, err := s.engine.Create(ctx, agentID, options)
	if err != nil {
		return engine.Ref{}, err
	}
	err = s.q.SetExecution(ctx, sqlc.SetExecutionParams{ID: id, ExecutionID: sql.NullInt64{Int64: ref.ID, Valid: true}})
	if err == nil {
		s.signal(id)
	}
	return ref, err
}
