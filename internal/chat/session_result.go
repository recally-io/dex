package chat

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strconv"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

// sessionResult is the body of a result that a child session sends to its parent. The key
// "task-result" and the kind task_result keep their old names: saved transcripts and a CHECK
// constraint use them.
type sessionResult struct {
	Session string `json:"session"`
	Title   string `json:"title"`
	Turn    string `json:"turn"`
	// Outcome is done, failed, or interrupted.
	Outcome string `json:"outcome"`
	Answer  string `json:"answer,omitempty"`
	Reason  string `json:"reason,omitempty"`
	// Withdrawn counts the queued inputs that a restart took out of the child session.
	Withdrawn int `json:"withdrawn,omitzero"`
}

// resultOutcome maps the outcome of a native input to the outcome of a result. A stopped run sends
// no result: the caller of Stop knows.
func resultOutcome(event engine.Outcome) (string, bool) {
	switch {
	case event.Reason == "stopped", event.Reason == "aborted", event.Reason == "withdrawn":
		return "", false
	case event.Status == "done":
		return "done", true
	case event.Reason == "interrupted":
		return "interrupted", true
	}
	return "failed", true
}

// sendResult writes the result of a parent turn to the parent session in the transaction of q. It
// returns the parent session, or zero when the turn sends no result.
func (s *Service) sendResult(ctx context.Context, q *sqlc.Queries, turn string, event engine.Outcome, withdrawn int) (int64, error) {
	link, err := q.GetTurnParent(ctx, turn)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	// An archived bot takes no new input, and a result is input.
	if archived, err := q.ConversationAgentArchived(ctx, link.ParentID.Int64); err != nil || archived != 0 {
		return 0, err
	}
	outcome, send := resultOutcome(event)
	if !send {
		return 0, nil
	}
	if outcome == "done" && event.AnswerID == 0 {
		return 0, errors.New("completed native input has no answer ID")
	}
	body := sessionResult{Session: strconv.FormatInt(link.ChildID, 10), Title: link.Title, Turn: turn, Outcome: outcome, Reason: event.Reason, Withdrawn: withdrawn}
	if outcome == "done" {
		var cut bool
		if body.Answer, cut = cutRunes(event.Answer, textLimit); cut {
			body.Answer += fmt.Sprintf("\n[The answer is cut. Read the rest with session_read: id %d, turn %s, offset %d.]", link.ChildID, turn, textLimit)
		}
	}
	f, err := q.GetChannelFollowUpByDelegation(ctx, turn)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	var guidance string
	var revoke bool
	var consume bool
	if f.DelegationID != "" {
		child, err := q.GetConversation(ctx, link.ChildID)
		if err != nil {
			return 0, err
		}
		input, err := q.GetSubmission(ctx, turn)
		if err != nil {
			return 0, err
		}
		original, err := followUpParent(ctx, q, f, child, input)
		if err != nil {
			return 0, err
		}
		active, err := q.ChannelBindingActive(ctx, original.BindingID)
		if err != nil {
			return 0, err
		}
		revoke = f.State == "cancelled" || !active || original.ChannelRevoked != 0
		if f.State == "open" && !revoke {
			guidance = followUpPrompt
			consume = outcome == "done" && f.StagedReport.Valid && event.ActivationID != 0 && f.NativeActivationID.Int64 == event.ActivationID
			if consume {
				body.Answer = f.StagedReport.String
			}
		}
	}
	content, err := json.Marshal(struct {
		Result   sessionResult `json:"task-result"`
		FollowUp string        `json:"channel-follow-up,omitempty"`
	}{body, guidance})
	if err != nil {
		return 0, err
	}
	created, err := q.CreateTurnResult(ctx, sqlc.CreateTurnResultParams{ID: "result:" + turn, TurnID: turn, Content: string(content), CreatedAt: s.now().UnixMilli(), NativeAnswerID: sql.NullInt64{Int64: event.AnswerID, Valid: outcome == "done"}, RevokeRoute: boolInt(revoke)})
	if err != nil || created == 0 {
		return 0, err
	}
	if consume {
		if err := q.ConsumeChannelFollowUpReport(ctx, turn); err != nil {
			return 0, err
		}
	}
	return link.ParentID.Int64, nil
}

// resultLabel reads the turn and the session title from a result, to label the transcript entry. The
// wake-up of a background command has no turn: its label is the title.
func resultLabel(content string) (turn, title string) {
	var body struct {
		Result     sessionResult `json:"task-result"`
		Background struct {
			Label string `json:"label"`
		} `json:"background-result"`
	}
	if json.Unmarshal([]byte(content), &body) != nil {
		return "", ""
	}
	return body.Result.Turn, cmp.Or(body.Result.Title, body.Background.Label)
}
