package chat

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// The settle worker must wait for an in-flight withdrawal. Withdraw holds the agent delivery lock from
// the native abort to the receipt write, so the test holds the lock as Withdraw does.
func TestUserWithdrawnMessageIsNotSettledByTheWorkerBeforeTheWithdrawal(t *testing.T) {
	// Given a running gated command and a second message that waits in the native queue.
	r := startNative(t, "bash")
	release := filepath.Join(t.TempDir(), "release")
	started := filepath.Join(t.TempDir(), "started")
	main, err := r.service.Main(t.Context(), "bot")
	if err != nil {
		t.Fatal(err)
	}
	command := fmt.Sprintf("touch '%s'; while [ ! -f '%s' ]; do sleep 0.02; done; echo done", started, release)
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	if _, err := r.service.Send(t.Context(), "bot", MainKey, Message{ID: "first", Content: command}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, err := os.Stat(started); return err == nil })
	if _, err := r.service.Send(t.Context(), "bot", MainKey, Message{ID: "second", Content: "echo second"}); err != nil {
		t.Fatal(err)
	}
	chat, err := r.service.Get(t.Context(), "bot", main.ID)
	if err != nil {
		t.Fatal(err)
	}

	// When the native abort has happened but the receipt write has not, then the receipt stays pending.
	unlock, err := r.service.lockAgent(t.Context(), "bot")
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := r.agents.Withdraw(t.Context(), engine.Ref{AgentID: "bot", ID: chat.ExecutionID}, "second")
	if err != nil || outcome != engine.Withdrawn {
		unlock()
		t.Fatalf("native withdraw: %v %v", outcome, err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		sub, err := r.q.GetSubmission(t.Context(), "second")
		if err != nil || sub.Status != "pending" {
			unlock()
			t.Fatalf("the worker settled a withdrawn message before the withdrawal ended: %+v %v", sub, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	unlock()

	// Then the withdrawal finishes, the receipt is withdrawn, and no reply is left to send.
	if _, err := r.service.Withdraw(t.Context(), "bot", main.ID, "second"); err != nil {
		t.Fatal(err)
	}
	sub, err := r.q.GetSubmission(t.Context(), "second")
	if err != nil || sub.Status != "unanswered" || sub.Reason.String != "withdrawn" || !sub.DeliveredAt.Valid {
		t.Fatalf("receipt: %+v %v", sub, err)
	}
}
