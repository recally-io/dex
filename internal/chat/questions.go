package chat

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

var (
	// ErrQuestionClosed means the question is not open any more. The answer is not stored.
	ErrQuestionClosed = errors.New("the question is closed")
	// ErrQuestionInvalid means the question or its options do not fit the limits.
	ErrQuestionInvalid = errors.New("invalid question")
	// ErrQuestionOpen means the conversation has a question that is not settled yet.
	ErrQuestionOpen = errors.New("a question is already open")
)

const (
	maxQuestion = 1000
	maxOption   = 200
)

// Question is the question that waits for the user in a chat.
type Question struct {
	ID      string
	Text    string
	Options []string
	// Status is "open", or "answered" while the answer waits in the queue.
	Status string
	// Answer is the text of an answered question.
	Answer string
}

const answerPrefix = "Answer to: "

func questionOf(row sqlc.Question) (Question, error) {
	q := Question{ID: row.ID, Text: row.Text, Status: row.Status}
	return q, json.Unmarshal([]byte(row.OptionsJson), &q.Options)
}

// currentQuestion returns the open or answered question of a chat, or nil.
func (s *Service) currentQuestion(ctx context.Context, chatID int64) (*Question, error) {
	row, err := s.q.GetCurrentQuestion(ctx, chatID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.questionView(ctx, row)
}

func (s *Service) questionView(ctx context.Context, row sqlc.Question) (*Question, error) {
	q, err := questionOf(row)
	if err != nil {
		return nil, err
	}
	if row.Status == "answered" {
		sub, err := s.q.GetSubmission(ctx, row.AnswerSubmissionID.String)
		if err != nil {
			return nil, err
		}
		q.Answer = strings.TrimPrefix(sub.Content, answerPrefix+row.Text+"\n")
	}
	return &q, nil
}

// ToolAskUser saves a question for the user. A replay of the same tool call returns the same
// question. The source input must still run, the chat must not be archived, and the chat must
// have no other question that waits.
func (s *Service) ToolAskUser(ctx context.Context, r engine.TaskContext, a engine.AskUserArgs) (engine.QuestionInfo, error) {
	// One line, so the answer wrapper "Answer to: <question>" has an unambiguous first line.
	text := strings.Join(strings.Fields(a.Question), " ")
	if text == "" || utf8.RuneCountInString(text) > maxQuestion || len(a.Options) < 2 || len(a.Options) > 4 {
		return engine.QuestionInfo{}, fmt.Errorf("%w: a question of 1 to %d characters and 2 to 4 options", ErrQuestionInvalid, maxQuestion)
	}
	options := make([]string, len(a.Options))
	for i, option := range a.Options {
		options[i] = strings.TrimSpace(option)
		if options[i] == "" || utf8.RuneCountInString(options[i]) > maxOption {
			return engine.QuestionInfo{}, fmt.Errorf("%w: each option has 1 to %d characters", ErrQuestionInvalid, maxOption)
		}
	}
	encoded, err := json.Marshal(options)
	if err != nil {
		return engine.QuestionInfo{}, err
	}
	// Stop, Clear and archive take the same writer before they read.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return engine.QuestionInfo{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if prior, err := q.GetQuestionByOperation(ctx, r.Key); err == nil {
		stored, err := questionOf(prior)
		if err != nil {
			return engine.QuestionInfo{}, err
		}
		return engine.QuestionInfo{ID: stored.ID, Text: stored.Text, Options: stored.Options}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return engine.QuestionInfo{}, err
	}
	conv, err := q.GetConversationByExecution(ctx, sqlc.GetConversationByExecutionParams{AgentID: r.AgentID, ExecutionID: sql.NullInt64{Int64: r.ConversationID, Valid: true}})
	if err != nil {
		return engine.QuestionInfo{}, err
	}
	if !Writable(conv.ChannelKey) || conv.ParentID.Valid {
		return engine.QuestionInfo{}, fmt.Errorf("%w: only Main and Chat sessions ask the user", ErrQuestionInvalid)
	}
	if conv.ArchivedAt.Valid {
		return engine.QuestionInfo{}, fmt.Errorf("%w: the chat is archived", ErrQuestionClosed)
	}
	source, err := liveSource(ctx, q, r)
	if err != nil {
		return engine.QuestionInfo{}, err
	}
	switch current, err := q.GetCurrentQuestion(ctx, conv.ID); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return engine.QuestionInfo{}, err
	case current.Status == "answered" && current.AnswerSubmissionID.String == source.ID:
		// The turn that runs the answer may ask the next question.
		if err := q.ConsumeQuestion(ctx, sqlc.ConsumeQuestionParams{SubmissionID: sql.NullString{String: source.ID, Valid: true}, Now: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}}); err != nil {
			return engine.QuestionInfo{}, err
		}
	default:
		return engine.QuestionInfo{}, ErrQuestionOpen
	}
	id := uuid.NewV7().String()
	if err := q.CreateQuestion(ctx, sqlc.CreateQuestionParams{ID: id, AgentID: r.AgentID, ConversationID: conv.ID, SourceSubmissionID: source.ID, ToolOperationKey: r.Key, Text: text, OptionsJson: string(encoded), CreatedAt: s.now().UnixMilli()}); err != nil {
		return engine.QuestionInfo{}, err
	}
	if err := tx.Commit(); err != nil {
		return engine.QuestionInfo{}, err
	}
	s.signal(conv.ID)
	return engine.QuestionInfo{ID: id, Text: text, Options: options}, nil
}

// answerContent loads the open question for an answer inside the receipt transaction and returns
// the content that the model reads.
func answerContent(ctx context.Context, q *sqlc.Queries, agentID string, chatID int64, questionID, answer string) (string, error) {
	row, err := q.GetQuestion(ctx, questionID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (row.AgentID != agentID || row.ConversationID != chatID)) {
		return "", ErrQuestionClosed
	}
	if err != nil {
		return "", err
	}
	if row.Status != "open" {
		return "", ErrQuestionClosed
	}
	return answerPrefix + row.Text + "\n" + answer, nil
}

// reopenQuestion opens the question again when its answer was withdrawn before it ran.
func reopenQuestion(ctx context.Context, q *sqlc.Queries, submissionID string) error {
	_, err := q.ReopenQuestion(ctx, sql.NullString{String: submissionID, Valid: true})
	return err
}

// renderQuestion is the channel text of a question. A platform that quotes (Telegram and Feishu)
// tells the user to reply to the message; the others point to the web.
func renderQuestion(q sqlc.Question, options []string, agentID string, main, quotable bool) string {
	var b strings.Builder
	b.WriteString(q.Text)
	b.WriteString("\n")
	for i, option := range options {
		fmt.Fprintf(&b, "\n%c. %s", rune('A'+i), option)
	}
	path := fmt.Sprintf("/a/%s/c/%d", agentID, q.ConversationID)
	if main {
		path = "/a/" + agentID
	}
	if quotable {
		b.WriteString("\n\nReply to this message to answer, or answer in the dex web app: " + path)
	} else {
		b.WriteString("\n\nThis question is open. Answer it in the dex web app: " + path)
	}
	return b.String()
}

// withQuestion adds the question that this input asked to its answer. The channel reply outbox
// sends the answer, so a channel user sees the question without a second delivery path.
func (s *Service) withQuestion(ctx context.Context, id string, event engine.Outcome) (engine.Outcome, error) {
	row, err := s.q.GetQuestionBySource(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && row.Status == "dismissed") || event.Status != "done" {
		return event, nil
	}
	if err != nil {
		return event, err
	}
	q, err := questionOf(row)
	if err != nil {
		return event, err
	}
	conv, err := s.q.GetConversation(ctx, row.ConversationID)
	if err != nil {
		return event, err
	}
	reply, err := s.q.GetSubmissionReply(ctx, id)
	if err != nil {
		return event, err
	}
	rendered := renderQuestion(row, q.Options, row.AgentID, conv.ChannelKey == MainKey, quotes(reply.Origin))
	if event.Answer != "" {
		rendered = event.Answer + "\n\n" + rendered
	}
	event.Answer = rendered
	return event, nil
}

// quotes reports whether the platform of a channel peer returns the ID of a quoted message.
func quotes(origin string) bool {
	return strings.HasPrefix(origin, "telegram:") || strings.HasPrefix(origin, "feishu:")
}
