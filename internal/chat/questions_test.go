package chat

import (
	"errors"
	"testing"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

var askArgs = engine.AskUserArgs{Question: "Which one?", Options: []string{"first", "second"}}

func TestUserQuestionReplayReturnsTheStoredQuestion(t *testing.T) {
	// Given a question that the tool saved.
	r := startNative(t, "echo")
	_, source := taskParent(t, r)
	first, err := r.service.ToolAskUser(t.Context(), source, askArgs)
	if err != nil {
		t.Fatal(err)
	}

	// When PiG replays the same tool call with different arguments, then it gets the saved question.
	replay, err := r.service.ToolAskUser(t.Context(), source, engine.AskUserArgs{Question: "Changed?", Options: []string{"x", "y", "z"}})
	if err != nil || replay.ID != first.ID || replay.Text != "Which one?" || len(replay.Options) != 2 {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	var count int
	if err := r.conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM questions").Scan(&count); err != nil || count != 1 {
		t.Fatalf("questions: %d %v", count, err)
	}
}

func TestUserQuestionIsRefusedWhenItCannotBeAnswered(t *testing.T) {
	r := startNative(t, "echo")
	chatID, source := taskParent(t, r)

	// Given a second tool call of the same turn, when a question is open, then it is refused.
	if _, err := r.service.ToolAskUser(t.Context(), source, askArgs); err != nil {
		t.Fatal(err)
	}
	source.Key = "bot/spawn-2"
	if _, err := r.service.ToolAskUser(t.Context(), source, askArgs); !errors.Is(err, ErrQuestionOpen) {
		t.Fatalf("second question: %v", err)
	}

	// Given invalid options, then the tool reports the limits.
	source.Key = "bot/spawn-3"
	if _, err := r.service.ToolAskUser(t.Context(), source, engine.AskUserArgs{Question: "One option?", Options: []string{"only"}}); !errors.Is(err, ErrQuestionInvalid) {
		t.Fatalf("one option: %v", err)
	}

	// Given an archived chat, then the tool is refused too.
	if _, err := r.conn.ExecContext(t.Context(), "UPDATE conversations SET archived_at=1 WHERE id=?", chatID); err != nil {
		t.Fatal(err)
	}
	source.Key = "bot/spawn-5"
	if _, err := r.service.ToolAskUser(t.Context(), source, askArgs); !errors.Is(err, ErrQuestionClosed) {
		t.Fatalf("archived chat: %v", err)
	}

	if _, err := r.conn.ExecContext(t.Context(), "UPDATE conversations SET archived_at=NULL WHERE id=?", chatID); err != nil {
		t.Fatal(err)
	}

	// Given a source input that ended, then the tool is refused.
	if _, err := r.conn.ExecContext(t.Context(), "UPDATE questions SET status='dismissed'"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.q.SettleSubmission(t.Context(), sqlc.SettleSubmissionParams{ID: "source", Status: "unanswered"}); err != nil {
		t.Fatal(err)
	}
	source.Key = "bot/spawn-4"
	if _, err := r.service.ToolAskUser(t.Context(), source, askArgs); err == nil {
		t.Fatal("a question was opened for a source that ended")
	}
}

func TestUserAnswerThatNeverRanOpensItsQuestionAgain(t *testing.T) {
	// Given a question whose answer waits as a pending input.
	r := startNative(t, "echo")
	chatID, source := taskParent(t, r)
	question, err := r.service.ToolAskUser(t.Context(), source, askArgs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{ID: "answer", ConversationID: chatID, Content: "Answer to: Which one?\nfirst", Attachments: "null", Origin: "web", Kind: "message", CreatedAt: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.conn.ExecContext(t.Context(), "UPDATE questions SET status='answered', answer_submission_id='answer'"); err != nil {
		t.Fatal(err)
	}
	status := func() string {
		var s string
		if err := r.conn.QueryRowContext(t.Context(), "SELECT status FROM questions WHERE id=?", question.ID).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	// When the input ends without a reply, then the question is open again.
	if err := r.service.settle(t.Context(), "answer", engine.Outcome{Status: "unanswered", Reason: "no model"}); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != "open" {
		t.Fatalf("status after a failed answer: %s", got)
	}

	// When a later answer runs and ends with a reply, then the question is consumed.
	if _, err := r.q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{ID: "answer-2", ConversationID: chatID, Content: "Answer to: Which one?\nsecond", Attachments: "null", Origin: "web", Kind: "message", CreatedAt: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.conn.ExecContext(t.Context(), "UPDATE questions SET status='answered', answer_submission_id='answer-2'"); err != nil {
		t.Fatal(err)
	}
	if err := r.service.settle(t.Context(), "answer-2", engine.Outcome{Status: "done", Answer: "ok"}); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != "consumed" {
		t.Fatalf("status after a finished answer: %s", got)
	}
}
