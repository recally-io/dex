package chat

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"strings"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

// ErrAlreadyStarted means the input entered native execution, or it finished. It keeps running.
var ErrAlreadyStarted = errors.New("the message already started")

// WithdrawnMessage is the content of an input that did not run. The caller can send it again.
type WithdrawnMessage struct {
	ID          string
	Content     string
	Attachments []engine.Attachment
}

// Withdraw removes one queued message of a writable chat. Native execution decides whether the
// input is still queued; the receipt then settles as withdrawn and keeps its request ID and
// source key. A repeated call returns the same message.
func (s *Service) Withdraw(ctx context.Context, agentID string, chatID int64, id string) (WithdrawnMessage, error) {
	c, err := s.Get(ctx, agentID, chatID)
	if err != nil {
		return WithdrawnMessage{}, err
	}
	if !c.Writable {
		return WithdrawnMessage{}, ErrNotFound
	}
	unlock, err := s.lockAgent(ctx, agentID)
	if err != nil {
		return WithdrawnMessage{}, err
	}
	defer func() { unlock(); s.Wake() }()
	// Lease confirmation waits for this read lock, so maintenance cannot start mid-withdrawal.
	s.admissionMu.RLock()
	defer s.admissionMu.RUnlock()
	if s.leaseUntil.Load() != 0 {
		return WithdrawnMessage{}, engine.ErrMaintenance
	}
	sub, err := s.q.GetSubmission(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (sub.ConversationID != chatID || sub.Kind != "message")) {
		return WithdrawnMessage{}, ErrNotFound
	}
	if err != nil {
		return WithdrawnMessage{}, err
	}
	switch {
	case sub.Status == "unanswered" && sub.Reason.String == "withdrawn":
		return s.withdrawnMessage(ctx, sub)
	case sub.Status != "pending" && (sub.Status != "unanswered" || sub.Reason.String != "aborted"):
		return WithdrawnMessage{}, ErrAlreadyStarted
	}
	// A decided action question already ran its action or refused it. Withdrawal cannot undo that.
	switch decided, err := s.q.GetQuestionByAnswer(ctx, sql.NullString{String: id, Valid: true}); {
	case err == nil && decided.Outcome.Valid:
		return WithdrawnMessage{}, ErrAlreadyStarted
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return WithdrawnMessage{}, err
	}
	// Without a native conversation the input cannot have been admitted. The delivery lock keeps
	// admission from starting meanwhile.
	if c.ExecutionID != 0 {
		outcome, err := s.engine.Withdraw(ctx, engine.Ref{AgentID: agentID, ID: c.ExecutionID}, id)
		if err != nil {
			return WithdrawnMessage{}, err
		}
		if outcome == engine.Placed {
			return WithdrawnMessage{}, ErrAlreadyStarted
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WithdrawnMessage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	now := s.now().UnixMilli()
	changed, err := q.WithdrawSubmission(ctx, sqlc.WithdrawSubmissionParams{ID: id, Now: sql.NullInt64{Int64: now, Valid: true}})
	if err != nil {
		return WithdrawnMessage{}, err
	}
	if changed == 0 {
		// A settlement that raced with the withdrawal ran the input.
		return WithdrawnMessage{}, ErrAlreadyStarted
	}
	if err := reopenQuestion(ctx, q, id); err != nil {
		return WithdrawnMessage{}, err
	}
	if err := tx.Commit(); err != nil {
		return WithdrawnMessage{}, err
	}
	s.signal(chatID)
	return s.withdrawnMessage(ctx, sub)
}

// withdrawnMessage returns what the user typed. An answer drops the question wrapper that the
// model reads.
func (s *Service) withdrawnMessage(ctx context.Context, sub sqlc.Submission) (WithdrawnMessage, error) {
	out := WithdrawnMessage{ID: sub.ID, Content: sub.Content}
	switch question, err := s.q.GetQuestionByAnswer(ctx, sql.NullString{String: sub.ID, Valid: true}); {
	case err == nil:
		out.Content = strings.TrimPrefix(sub.Content, answerPrefix+question.Text+"\n")
	case !errors.Is(err, sql.ErrNoRows):
		return WithdrawnMessage{}, err
	}
	if err := json.Unmarshal([]byte(sub.Attachments), &out.Attachments); err != nil {
		return WithdrawnMessage{}, err
	}
	return out, nil
}
