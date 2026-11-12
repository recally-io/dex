package chat

import (
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
	case event.Status == "done":
		return "done", true
	case event.Reason == "stopped", event.Reason == "aborted", event.Reason == "withdrawn":
		return "", false
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
	content, err := json.Marshal(struct {
		Result sessionResult `json:"task-result"`
	}{body})
	if err != nil {
		return 0, err
	}
	created, err := q.CreateTurnResult(ctx, sqlc.CreateTurnResultParams{ID: "result:" + turn, TurnID: turn, Content: string(content), CreatedAt: s.now().UnixMilli(), NativeAnswerID: sql.NullInt64{Int64: event.AnswerID, Valid: outcome == "done"}})
	if err != nil || created == 0 {
		return 0, err
	}
	return link.ParentID.Int64, nil
}

// resultLabel reads the turn and the session title from a result, to label the transcript entry.
func resultLabel(content string) (turn, title string) {
	var body struct {
		Result sessionResult `json:"task-result"`
	}
	if json.Unmarshal([]byte(content), &body) != nil {
		return "", ""
	}
	return body.Result.Turn, body.Result.Title
}
