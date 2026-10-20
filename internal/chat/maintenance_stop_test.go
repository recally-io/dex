package chat

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserStopCancelsActiveRunAfterClientDisconnect(t *testing.T) {
	r := startNative(t, "bash")
	marker := filepath.Join(r.dir, "started")
	id, err := r.service.Post(t.Context(), "bot", MainKey, "", "running", "touch "+marker+"; sleep 60")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, err := os.Stat(marker); return err == nil })
	// Cancel the request after the withdrawal transaction signals its update.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r.service.changed = func(int64) { cancel() }
	if err = r.service.Stop(ctx, "bot", id); err != nil {
		t.Fatal(err)
	}
	sub := receipt(t, r, "running")
	if sub.Status != "unanswered" || sub.Reason.String != "stopped" {
		t.Fatalf("stopped receipt: %+v", sub)
	}
	r.service.changed = nil
	c, err := r.service.Get(t.Context(), "bot", id)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return r.agents.CheckIdle(context.Background(), executionRef(c)) == nil })
}

func TestUserMaintenanceStopDefersColdCancellationUntilRelease(t *testing.T) {
	r := startNative(t, "bash")
	c, err := r.service.Main(t.Context(), "bot")
	if err != nil {
		t.Fatal(err)
	}
	options := engine.Options{Model: engine.ModelRef{Provider: "faux", ModelID: "bash"}, ThinkingLevel: "off"}
	ref, err := r.service.execution(t.Context(), c.ID, "bot", options)
	if err != nil {
		t.Fatal(err)
	}
	// Given stored native work whose next start would repeat an external action.
	_, err = r.q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{ID: "cold", ConversationID: c.ID, Content: "stored work", Attachments: "null", Origin: "web", Kind: "message", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	starts := filepath.Join(r.dir, "starts")
	_, err = r.agents.Submit(t.Context(), ref, engine.Input{RequestID: "cold", Content: "echo start >> " + starts + "; while :; do sleep 1; done", Kind: "message", Origin: "web", Options: options})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { data, err := os.ReadFile(starts); return err == nil && string(data) == "start\n" })
	if err = r.agents.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	next, err := engine.New(engine.Config{DataDir: r.dir, Home: t.TempDir(), BaseEnv: map[string]string{"PATH": os.Getenv("PATH")}, FakeModel: true})
	if err != nil {
		t.Fatal(err)
	}
	service := New(r.conn, next, time.Now, nil, nil)
	t.Cleanup(func() {
		_ = service.ReleaseMaintenance(context.Background())
		service.cancel()
		service.wg.Wait()
		if err := next.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if err = service.AcquireMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	// When Stop withdraws the input during maintenance, cancellation stays durable.
	if err = service.Stop(t.Context(), "bot", c.ID); err != nil {
		t.Fatalf("durable Stop rejected during maintenance: %v", err)
	}
	saved, err := r.q.GetConversation(t.Context(), c.ID)
	if err != nil || saved.AbortPending != 1 {
		t.Fatalf("deferred cancellation lost: %+v %v", saved, err)
	}
	if sub := receipt(t, r, "cold"); sub.Reason.String != "stopped" {
		t.Fatalf("withdrawn receipt: %+v", sub)
	}
	if err = service.SubmitMessage(t.Context(), "bot", "telegram:7", "after-stop", Message{Content: "printf next"}); err != nil {
		t.Fatal(err)
	}
	service.deliveryMu.Lock()
	err = service.reconcile(t.Context())
	service.deliveryMu.Unlock()
	if err != nil {
		t.Fatalf("maintenance deferred work reported as failure: %v", err)
	}
	// Then maintenance has not resumed the old execution or cleared its cancel intent.
	saved, err = r.q.GetConversation(t.Context(), c.ID)
	if err != nil || saved.AbortPending != 1 {
		t.Fatalf("premature cancellation acknowledgement: %+v %v", saved, err)
	}
	data, err := os.ReadFile(starts)
	if err != nil || string(data) != "start\n" {
		t.Fatalf("maintenance resumed stored work: %q %v", data, err)
	}
	if err = service.ReleaseMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	runReconciler(t, &nativeTest{service: service})
	id, err := r.q.GetSubmissionBySource(t.Context(), sql.NullString{String: "after-stop", Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	if sub := awaitReceipt(t, r, id); sub.Answer.String != "bash: next" {
		t.Fatalf("later work blocked: %+v", sub)
	}
	saved, err = r.q.GetConversation(t.Context(), c.ID)
	if err != nil || saved.AbortPending != 0 {
		t.Fatalf("released cancellation still pending: %+v %v", saved, err)
	}
	data, err = os.ReadFile(starts)
	if err != nil || string(data) != "start\n" {
		t.Fatalf("release resumed withdrawn work: %q %v", data, err)
	}
}
