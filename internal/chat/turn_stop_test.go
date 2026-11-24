package chat

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserMainStopIntentRecoversWithoutNewInput(t *testing.T) {
	// Given Main is running, all its pending inputs already have native waiters.
	r := startNative(t, "bash")
	marker := filepath.Join(r.dir, "started")
	id, err := r.service.Post(t.Context(), "bot", MainKey, "", "active", "touch '"+marker+"'; exec sleep 60")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, err := os.Stat(marker); return err == nil })
	c, err := r.service.Get(t.Context(), "bot", id)
	if err != nil {
		t.Fatal(err)
	}
	watch, err := r.agents.Watch(t.Context(), executionRef(c))
	if err != nil {
		t.Fatal(err)
	}
	target := watch.Snapshot.RunTarget
	watch.Close()
	if target == "" {
		t.Fatal("running Main has no target")
	}

	// When an intent survives an interruption before native cancellation, dispatch
	// finds it without waiting for new work or a second Stop request.
	if err := r.q.RecordMainTurnStop(t.Context(), sqlc.RecordMainTurnStopParams{ConversationID: id, Target: target}); err != nil {
		t.Fatal(err)
	}
	runReconciler(t, r)
	if sub := awaitReceipt(t, r, "active"); sub.Status != "unanswered" || sub.Reason.String != "aborted" {
		t.Fatalf("recorded Stop did not cancel its target: %+v", sub)
	}
	if stops, err := r.q.ListAgentMainTurnStops(t.Context(), "bot"); err != nil || len(stops) != 0 {
		t.Fatalf("Stop intent was not cleared: %v, %v", stops, err)
	}
}

func TestUserStaleMainStopIntentDoesNotBlockFollowingInput(t *testing.T) {
	// Given Main has a persisted Stop intent whose target can no longer match its execution.
	r := startNative(t, "echo")
	id, err := r.service.Post(t.Context(), "bot", MainKey, "", "first", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if sub := awaitReceipt(t, r, "first"); sub.Status != "done" {
		t.Fatalf("first input did not finish: %+v", sub)
	}
	if err := r.q.RecordMainTurnStop(t.Context(), sqlc.RecordMainTurnStopParams{ConversationID: id, Target: "bot/999999/1"}); err != nil {
		t.Fatal(err)
	}

	// When reconciliation runs and the user sends another message, the stale intent clears
	// and does not block subsequent Main admission.
	runReconciler(t, r)
	waitFor(t, func() bool {
		stops, err := r.q.ListAgentMainTurnStops(t.Context(), "bot")
		return err == nil && len(stops) == 0
	})
	if _, err := r.service.Post(t.Context(), "bot", MainKey, "", "after", "next"); err != nil {
		t.Fatal(err)
	}
	if sub := awaitReceipt(t, r, "after"); sub.Status != "done" || sub.Answer.String != "echo: next" {
		t.Fatalf("following input was blocked by stale Stop intent: %+v", sub)
	}
}
