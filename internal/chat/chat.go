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

	"github.com/recally-io/dex/internal/background"
	"github.com/recally-io/dex/internal/db"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/dream"
	"github.com/recally-io/dex/internal/engine"
)

// Reply is the outcome of one message.
type Reply struct {
	LegacyDelivery bool   `json:"-"`
	Status         string `json:"status"` // "done" or "unanswered"
	Answer         string `json:"answer,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

// Chat is one conversation on one channel.
type Chat struct {
	Cwd        string
	ID         int64
	ChannelKey string
	Title      string
	CreatedAt  int64
	UpdatedAt  int64 // latest conversation or input activity
	Busy       bool  // a message has no answer yet
	ArchivedAt int64 // zero when the Chat session is not archived
	Settings   Settings
	// Writable is true when the web UI can send input to the session now. Main, Chat and child
	// sessions are always writable.
	Writable bool
	// ParentID is the session that created this child session. It is zero for any other session.
	ParentID int64
	// StoppedByParent is set by List: Stop on the parent would act on this child.
	StoppedByParent bool
	// AgentID and AgentName name the bot of the session. Only a child session in another bot than
	// the bot that was asked for has them: the list of a parent shows it.
	AgentID, AgentName string
	// ParentAgentID and ParentAgentName name the bot of the parent, when it is not the bot that was
	// asked for.
	ParentAgentID, ParentAgentName string
	// ExecutionID is zero until native execution creates the conversation.
	ExecutionID int64
	// Question is the question that waits for the user, or whose answer waits in the queue.
	Question *Question
	// ForkedFrom is the source of a fork. Get sets it; list rows leave it nil.
	ForkedFrom *Origin
}

// Settings are the settings that one chat overrides. An empty field follows the agent's setting.
// Provider and ModelID are both empty or both set; internal/settings validates them.
type Settings struct {
	Provider      string `json:"provider,omitempty"`
	ModelID       string `json:"modelId,omitempty"`
	ThinkingLevel string `json:"thinkingLevel,omitempty"`
}

// ChannelContext records the platform identity that authorized channel input.
type ChannelContext struct {
	AccountID  string `json:"accountId,omitempty"`
	SenderID   string `json:"senderId,omitempty"`
	MessageID  string `json:"messageId,omitempty"`
	ReplyTo    string `json:"replyTo,omitempty"`
	FileID     string `json:"fileId,omitempty"`
	MediaKind  string `json:"mediaKind,omitempty"`
	MediaGroup string `json:"mediaGroup,omitempty"`
}

// Message is a message to post. Title is the title of a new chat; empty uses the start of Content,
// or the name of the first attachment. Settings apply only when the message creates the chat.
type Message struct {
	ChannelContext ChannelContext
	ChannelNote    string
	AlbumSource    string
	ID             string
	Title          string
	Content        string
	Attachments    []engine.Attachment
	Settings       Settings
	// AnswersQuestion is the ID of the open question that this message answers. A message with
	// this ID is an answer to that question; any other message leaves a question open.
	AnswersQuestion string
	// ReplyTo is the remote ID of the channel message that this message quotes. A quote of a message
	// that carries an open question answers it.
	ReplyTo string
	// WhenBusy is engine.Input.WhenBusy. Empty means followUp.
	WhenBusy string
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
	// OnBotArchived runs after a transaction that archived a bot has committed. Optional.
	OnBotArchived func(agentID string)

	// The database holds work. A wake can coalesce without losing a submission.
	wake          chan struct{}
	waiting       sync.Map // deduplicates Wait goroutines; PiG owns execution state
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	db            *sql.DB
	writeMu       sync.Mutex // writer-first control transactions; per agent if throughput matters
	checkSettings func(context.Context, Settings) error
	background    *background.Manager

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

// ErrMessageID means a message ID was reused for a different request.
var ErrMessageID = errors.New("the message ID belongs to another input")

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
	if m.ReplyTo != "" {
		switch id, err := s.q.GetQuestionByMessage(ctx, sqlc.GetQuestionByMessageParams{AgentID: agentID, Peer: peer, RemoteID: m.ReplyTo}); {
		case err == nil:
			m.AnswersQuestion = id
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
	}
	content := m.Content
	addContext := func() {
		if m.ChannelNote != "" {
			m.Content = strings.TrimSpace(content + "\n\n" + m.ChannelNote)
		}
	}
	if m.AnswersQuestion == "" {
		addContext()
	}
	_, err := s.enqueue(ctx, agentID, MainKey, sourceKey, peer, m)
	if errors.Is(err, ErrQuestionClosed) && m.AnswersQuestion != "" {
		// The question closed while the user wrote. The words still reach the agent as a plain message.
		m.AnswersQuestion = ""
		addContext()
		_, err = s.enqueue(ctx, agentID, MainKey, sourceKey, peer, m)
	}
	return err
}

func (s *Service) enqueue(ctx context.Context, agentID, channelKey, sourceKey, origin string, m Message) (int64, error) {
	complete, archived := false, ""
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
		// The writer is held now. Input first makes an archive fail as busy; archive first ends here.
		if err := db.RequireActive(ctx, q, agentID); err != nil {
			return 0, err
		}
		if m.AlbumSource != "" {
			sealed, err := q.IsTelegramAlbumSealed(ctx, sqlc.IsTelegramAlbumSealedParams{AgentID: agentID, BatchSource: m.AlbumSource})
			if err != nil {
				return 0, err
			}
			if !sealed {
				return 0, fmt.Errorf("%w: album was discarded before admission", ErrSessionInvalid)
			}
		}
		attachments, err := json.Marshal(m.Attachments)
		if err != nil {
			return 0, err
		}
		content, answers := m.Content, false
		var decision answerDecision
		if m.AnswersQuestion != "" {
			// A retry of an answer that is stored already must not bind or wrap it again.
			switch _, err := q.GetSubmission(ctx, m.ID); {
			case errors.Is(err, sql.ErrNoRows):
				if decision, err = answerContent(ctx, q, agentID, conv.ID, m.AnswersQuestion, m.Content, origin); err != nil {
					return 0, err
				}
				content = decision.Content
				answers = true
			case err != nil:
				return 0, err
			}
		}
		channelContext, err := json.Marshal(m.ChannelContext)
		if err != nil {
			return 0, err
		}
		created, err := q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{
			ChannelContext: string(channelContext),
			ID:             m.ID,
			ConversationID: conv.ID,
			Content:        content,
			Attachments:    string(attachments),
			SourceKey:      sql.NullString{String: sourceKey, Valid: sourceKey != ""},
			Origin:         origin,
			Kind:           "message",
			WhenBusy:       m.WhenBusy,
			CreatedAt:      submittedAt,
		})
		if err != nil {
			return 0, err
		}
		if created == 0 {
			prior, priorErr := q.GetSubmission(ctx, m.ID)
			if priorErr != nil && !errors.Is(priorErr, sql.ErrNoRows) {
				return 0, priorErr
			}
			if priorErr == nil {
				original, err := originalMessageContent(ctx, q, prior, m.AnswersQuestion)
				if err != nil {
					return 0, err
				}
				if prior.ConversationID != conv.ID || prior.Kind != "message" || original != m.Content || prior.Attachments != string(attachments) || cmp.Or(prior.WhenBusy, "followUp") != cmp.Or(m.WhenBusy, "followUp") {
					return 0, ErrMessageID
				}
			}
		}
		if answers && created > 0 {
			bound, err := q.AnswerQuestion(ctx, sqlc.AnswerQuestionParams{ID: m.AnswersQuestion, AgentID: agentID, ConversationID: conv.ID, SubmissionID: sql.NullString{String: m.ID, Valid: true}, Outcome: decision.Outcome})
			if err != nil {
				return 0, err
			}
			if bound == 0 {
				return 0, ErrQuestionClosed
			}
			if err := decision.apply(ctx, q, s.now()); err != nil {
				return 0, err
			}
		}
		if created > 0 && strings.HasPrefix(channelKey, ChatPrefix) {
			if err := q.SetFirstChatTitle(ctx, sqlc.SetFirstChatTitleParams{ID: conv.ID, Title: chatTitle}); err != nil {
				return 0, err
			}
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		if created > 0 {
			archived = decision.archive
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
	// Channel runtime changes run outside the receipt gate, so a busy bot cannot block the gate.
	s.botArchived(archived)
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

// originalMessageContent removes the wrapper stored around an answer to a question.
func originalMessageContent(ctx context.Context, q *sqlc.Queries, prior sqlc.Submission, questionID string) (string, error) {
	question, err := q.GetQuestionByAnswer(ctx, sql.NullString{String: prior.ID, Valid: true})
	if errors.Is(err, sql.ErrNoRows) && questionID != "" {
		question, err = q.GetQuestion(ctx, questionID)
		if err == nil && question.ConversationID != prior.ConversationID {
			err = sql.ErrNoRows
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		if questionID != "" {
			return "", ErrMessageID
		}
		return prior.Content, nil
	}
	if err != nil {
		return "", err
	}
	content, wasAnswer := strings.CutPrefix(prior.Content, answerPrefix+question.Text+"\n")
	if wasAnswer != (questionID != "") || (wasAnswer && question.ID != questionID) {
		return "", ErrMessageID
	}
	if question.Outcome.Valid && wasAnswer {
		content = strings.TrimSuffix(content, outcomeSeparator+question.Outcome.String)
	}
	return content, nil
}

// rejectedBusy is the receipt reason of a reject input that arrived while the chat was busy.
const rejectedBusy = "busy"

func receiptError(sub sqlc.Submission) error {
	if !sub.AdmittedAt.Valid && sub.Status == "unanswered" && sub.Reason.String == rejectedBusy {
		return fmt.Errorf("%w: the message was not run", engine.ErrBusy)
	}
	if !sub.AdmittedAt.Valid && sub.Status == "unanswered" && sub.Reason.String != "stopped" && sub.Reason.String != "interrupted" && sub.Reason.String != "withdrawn" {
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
	questions, err := s.q.ListCurrentQuestions(ctx, agentID)
	if err != nil {
		return nil, err
	}
	byChat := make(map[int64]*Question, len(questions))
	for _, row := range questions {
		if byChat[row.ConversationID], err = s.questionView(ctx, row); err != nil {
			return nil, err
		}
	}
	stopped, err := s.q.ListChildrenStoppedByParent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	stoppedByParent := make(map[int64]bool, len(stopped))
	for _, id := range stopped {
		reached, err := s.childStoppedByParent(ctx, id)
		if err != nil {
			return nil, err
		}
		stoppedByParent[id] = reached
	}
	chats := make([]Chat, len(rows))
	for i, r := range rows {
		chats[i] = Chat{
			Cwd: r.Cwd, ID: r.ID, ChannelKey: r.ChannelKey, Title: r.Title, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
			Busy: r.Pending > 0, ArchivedAt: r.ArchivedAt.Int64, Settings: Settings{Provider: r.ModelProvider, ModelID: r.ModelID, ThinkingLevel: r.ThinkingLevel},
			Writable: acceptsInput(r.ChannelKey, r.ParentID), ParentID: r.ParentID.Int64, Question: byChat[r.ID], StoppedByParent: stoppedByParent[r.ID],
		}
		if r.AgentID != agentID {
			chats[i].AgentID, chats[i].AgentName = r.AgentID, r.AgentName
		}
		chats[i].ParentAgentID, chats[i].ParentAgentName = otherBot(agentID, r.ParentAgentID, r.ParentAgentName)
	}
	return chats, nil
}

// otherBot returns the bot of a parent when it is not the bot that was asked for.
func otherBot(agentID string, id, name sql.NullString) (string, string) {
	if !id.Valid || id.String == agentID {
		return "", ""
	}
	return id.String, name.String
}

// childStoppedByParent refines the SQL candidate for a child with mixed pending input. The native
// queue belongs to the bot of the child, which can differ from the bot that lists it.
func (s *Service) childStoppedByParent(ctx context.Context, id int64) (bool, error) {
	pending, err := s.q.ListChildPending(ctx, id)
	if err != nil {
		return false, err
	}
	user := false
	parent := false
	for _, p := range pending {
		user = user || !p.FromParent
		parent = parent || p.FromParent
	}
	if !parent || !user {
		return parent, nil
	}
	c, err := s.q.GetConversation(ctx, id)
	if err != nil {
		return false, err
	}
	if !c.ExecutionID.Valid {
		return true, nil
	}
	for _, p := range pending {
		if !p.FromParent {
			continue
		}
		outcome, err := s.engine.PeekWithdrawal(ctx, engine.Ref{AgentID: c.AgentID, ID: c.ExecutionID.Int64}, p.ID)
		if errors.Is(err, engine.ErrNotFound) || errors.Is(err, engine.ErrModelsPending) || errors.Is(err, engine.ErrMaintenance) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if outcome != engine.Placed {
			return true, nil
		}
	}
	return false, nil
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
	question, err := s.currentQuestion(ctx, id)
	if err != nil {
		return Chat{}, err
	}
	c := Chat{
		Cwd: r.Cwd, ID: r.ID, ChannelKey: r.ChannelKey, Title: r.Title, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		Busy: r.Pending > 0, ArchivedAt: r.ArchivedAt.Int64, Settings: Settings{Provider: r.ModelProvider, ModelID: r.ModelID, ThinkingLevel: r.ThinkingLevel},
		Writable: acceptsInput(r.ChannelKey, r.ParentID), ParentID: r.ParentID.Int64, ExecutionID: r.ExecutionID.Int64, Question: question,
		ForkedFrom: forkOrigin(r),
	}
	c.ParentAgentID, c.ParentAgentName = otherBot(agentID, r.ParentAgentID, r.ParentAgentName)
	return c, nil
}

func forkOrigin(r sqlc.GetChatRow) *Origin {
	if !r.ForkedFrom.Valid {
		return nil
	}
	return &Origin{ID: r.ForkedFrom.Int64, Title: r.ForkedFromTitle.String, Main: r.ForkedFromKey.String == MainKey}
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
	options := chatOptions(agent, conv)
	if strings.HasPrefix(p.ChannelKey, dream.Channel) {
		options.Instructions = dream.Instructions(p.AgentID, options.Instructions)
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
	if p.Kind == "context_reset" {
		if err = s.engine.Reset(ctx, ref, p.ID, p.Content); err != nil {
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
	input := engine.Input{RequestID: p.ID, Content: p.Content, Kind: p.Kind, Origin: p.Origin, Source: source(p), Attachments: attachments, Options: options, Child: conv.ParentID.Valid, Automation: strings.HasPrefix(p.ChannelKey, "automation:"), Main: p.ChannelKey == MainKey, WhenBusy: p.WhenBusy}
	if input.Child {
		input.Changed = func() { s.signal(conv.ID) }
	}
	if p.Kind == "task_result" {
		input.TaskID, input.TaskTitle = resultLabel(p.Content)
	}
	sub, err := s.engine.Submit(ctx, ref, input)
	if err != nil {
		if p.WhenBusy == "reject" && errors.Is(err, engine.ErrBusy) {
			// A rejected input is final. Retrying it would run it after the current run.
			return s.settle(ctx, p.ID, engine.Outcome{Status: "unanswered", Reason: rejectedBusy})
		}
		return s.admissionError(ctx, p.ID, err)
	}
	if err = s.markAdmitted(ctx, p.ID); err != nil {
		return err
	}
	if _, loaded := s.waiting.LoadOrStore(p.ID, struct{}{}); loaded {
		return nil
	}
	started := s.startWorker(func() {
		outcome, err := sub.Wait(s.ctx)
		if err == nil {
			err = s.settleNative(p.AgentID, p.ID, outcome)
		}
		s.waiting.Delete(p.ID)
		if err != nil && s.ctx.Err() == nil {
			slog.Error("reconcile execution receipt", "submission", p.ID, "err", err)
			s.scheduleRetry()
		}
	})
	if !started {
		slog.Warn("input worker did not start", "submission", p.ID)
		s.waiting.Delete(p.ID)
	}
	return nil
}

// chatOptions are the run settings of a chat: its own model and thinking level, else the agent's.
func chatOptions(agent sqlc.Agent, conv sqlc.Conversation) engine.Options {
	options := engine.Options{Cwd: conv.Cwd, Model: engine.ModelRef{Provider: agent.ModelProvider, ModelID: agent.ModelID}, ThinkingLevel: cmp.Or(conv.ThinkingLevel, agent.ThinkingLevel), Instructions: agent.Instructions}
	if conv.ModelID != "" {
		options.Model = engine.ModelRef{Provider: conv.ModelProvider, ModelID: conv.ModelID}
	}
	return options
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

// settleNative records a native outcome. A native abort comes from Withdraw (Stop settles the receipt
// first). Withdraw holds the agent delivery lock from the abort to the receipt write, so waiting for
// the lock lets it finish first. The settlement then finds a withdrawn receipt and changes nothing,
// so no "No answer" reply goes to the channel.
func (s *Service) settleNative(agentID, id string, event engine.Outcome) error {
	if event.Status == "unanswered" && event.Reason == "aborted" {
		unlock, err := s.lockAgent(s.ctx, agentID)
		if err != nil {
			return err
		}
		defer unlock()
	}
	return s.settle(s.ctx, id, event)
}

func (s *Service) settle(ctx context.Context, id string, event engine.Outcome) error {
	event, err := s.withQuestion(ctx, id, event)
	if err != nil {
		return err
	}
	changed, parent, err := s.settleInput(ctx, id, event)
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
	if parent != 0 {
		// The result is a new input of the parent session. Admission takes it from the database.
		s.signal(parent)
		s.Wake()
	}
	// A parent turn has no channel to answer: its result goes to the parent session.
	if s.notify != nil && reply.Origin != "task" {
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
