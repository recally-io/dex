package chat

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/engine"
)

func TestUserStopDuringModelDiscoveryKeepsCancellationDurable(t *testing.T) {
	// Given stored native work and a cold engine waiting for model discovery.
	r := startNative(t, "bash")
	starts := filepath.Join(r.dir, "starts")
	chatID, err := r.service.Post(t.Context(), "bot", MainKey, "", "stored", "echo start >> "+starts+"; while :; do sleep 1; done")
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
	t.Cleanup(func() {
		if err := next.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	releaseDiscovery := func() { releaseOnce.Do(func() { close(release) }) }
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		enteredOnce.Do(func() { close(entered) })
		select {
		case <-release:
			_, _ = w.Write([]byte(`{"data":[{"id":"test-model"}]}`))
		case <-req.Context().Done():
		}
	}))
	t.Cleanup(provider.Close)
	t.Cleanup(releaseDiscovery)
	discovery, err := next.Catalog.ConfigureAsync(t.Context(), []engine.ProviderSettings{{ID: "slow", API: "openai-completions", BaseURL: provider.URL}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("model discovery did not start")
	}
	service := New(r.conn, next, time.Now, nil, nil)
	t.Cleanup(func() {
		service.cancel()
		service.wg.Wait()
	})

	// When Stop runs before discovery completes, it saves cancellation and returns.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err = service.Stop(ctx, "bot", chatID); err != nil {
		t.Fatalf("durable Stop rejected while discovering models: %v", err)
	}
	saved, err := r.q.GetConversation(t.Context(), chatID)
	if err != nil || saved.AbortPending != 1 {
		t.Fatalf("deferred cancellation lost: %+v %v", saved, err)
	}
	if sub := receipt(t, r, "stored"); sub.Status != "unanswered" || sub.Reason.String != "stopped" {
		t.Fatalf("withdrawn receipt: %+v", sub)
	}
	if err = service.SubmitMessage(t.Context(), "bot", "telegram:7", "after-discovery-stop", Message{Content: "printf next"}); err != nil {
		t.Fatal(err)
	}
	laterID, err := r.q.GetSubmissionBySource(t.Context(), sql.NullString{String: "after-discovery-stop", Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	if sub := receipt(t, r, laterID); sub.Status != "pending" || sub.AdmittedAt.Valid {
		t.Fatalf("input admitted before discovery: %+v", sub)
	}

	// Then discovery lets reconciliation cancel stored work before later admission.
	releaseDiscovery()
	if _, err = discovery.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	runReconciler(t, &nativeTest{service: service})
	if sub := awaitReceipt(t, r, laterID); sub.Status != "done" || sub.Answer.String != "bash: next" {
		t.Fatalf("later input did not recover: %+v", sub)
	}
	saved, err = r.q.GetConversation(t.Context(), chatID)
	if err != nil || saved.AbortPending != 0 {
		t.Fatalf("completed cancellation still pending: %+v %v", saved, err)
	}
	data, err := os.ReadFile(starts)
	if err != nil || string(data) != "start\n" {
		t.Fatalf("discovery resumed cancelled work: %q %v", data, err)
	}
}
