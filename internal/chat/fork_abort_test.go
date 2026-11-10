package chat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/engine"
)

func TestUserForkWaitsForPendingNativeAbort(t *testing.T) {
	r := startNative(t, "bash")
	starts := filepath.Join(r.dir, "starts")
	id, err := r.service.Post(t.Context(), "bot", MainKey, "", "stored", "echo start >> "+starts+"; while :; do sleep 1; done")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { data, err := os.ReadFile(starts); return err == nil && string(data) == "start\n" })
	source, err := r.service.Get(t.Context(), "bot", id)
	if err != nil {
		t.Fatal(err)
	}
	history, err := r.agents.History(t.Context(), engine.Ref{AgentID: "bot", ID: source.ExecutionID}, 1<<62, 100)
	if err != nil {
		t.Fatal(err)
	}
	var entry int64
	for _, item := range history.Items {
		if item.Kind == "user" && item.EntryID > 0 {
			entry = item.EntryID
			break
		}
	}
	if entry == 0 {
		t.Fatal("committed user entry missing")
	}
	r.service.cancel()
	r.service.wg.Wait()
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
	defer func() { releaseDiscovery(); provider.Close() }()
	discovery, err := next.Catalog.ConfigureAsync(t.Context(), []engine.ProviderSettings{{ID: "slow", API: "openai-completions", BaseURL: provider.URL}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("discovery did not start")
	}
	service := New(r.conn, next, time.Now, nil, nil)
	defer func() { service.cancel(); service.wg.Wait() }()
	// A previously committed fork receipt exists when Stop has to defer the native abort.
	source, err = service.Get(t.Context(), "bot", id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.createForkReceipt(t.Context(), "bot", source, entry, "pending-fork", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = service.createCompactReceipt(t.Context(), "bot", source, "pending-compact", ""); err != nil {
		t.Fatal(err)
	}
	if err = service.Stop(t.Context(), "bot", id); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Fork(t.Context(), "bot", id, entry, "direct-fork"); !errors.Is(err, ErrCancellationPending) {
		t.Fatalf("fork passed deferred abort: %v", err)
	}
	if _, err = r.q.GetForkOperation(t.Context(), "direct-fork"); err == nil {
		t.Fatal("fork saved intent before deferred abort")
	}
	releaseDiscovery()
	if _, err = discovery.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = r.conn.ExecContext(t.Context(), `CREATE TRIGGER hold_abort_ack BEFORE UPDATE OF abort_pending ON conversations WHEN NEW.abort_pending=0 BEGIN SELECT RAISE(FAIL, 'abort ack unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Fork(t.Context(), "bot", id, entry, "direct-fork"); err == nil {
		t.Fatal("fork passed failed abort acknowledgement")
	}
	toolSource := engine.TaskContext{AgentID: "bot", ConversationID: source.ExecutionID, SourceRequestID: "stored", Key: "tool-fork"}
	if _, err = service.ToolFork(t.Context(), toolSource, entry); err == nil {
		t.Fatal("tool fork passed failed abort acknowledgement")
	}
	if _, err = r.q.GetForkOperation(t.Context(), "tool-fork"); err == nil {
		t.Fatal("tool saved intent before abort")
	}
	if _, err = service.Compact(t.Context(), "bot", id, "direct-compact"); err == nil {
		t.Fatal("compact passed failed abort")
	}
	toolSource.Key = "tool-compact"
	if _, err = service.ToolCompact(t.Context(), toolSource); err == nil {
		t.Fatal("tool compact passed failed abort")
	}
	if _, err = r.q.GetCompactOperation(t.Context(), "direct-compact"); err == nil {
		t.Fatal("compact saved intent before abort")
	}
	if _, err = r.q.GetCompactOperation(t.Context(), "tool-compact"); err == nil {
		t.Fatal("tool compact saved intent before abort")
	}
	if _, err = service.CompactionStatus(t.Context(), "bot", id, "pending-compact"); err == nil {
		t.Fatal("compact status passed failed abort")
	}
	if err = service.reconcileAgent(t.Context(), "bot"); err == nil {
		t.Fatal("reconciliation hid abort failure")
	}
	op, err := r.q.GetForkOperation(t.Context(), "pending-fork")
	if err != nil || op.Status != "pending" {
		t.Fatalf("fork passed abort gate: %+v %v", op, err)
	}
	compact, err := r.q.GetCompactOperation(t.Context(), "pending-compact")
	if err != nil || compact.Status != "pending" {
		t.Fatalf("compact passed abort gate: %+v %v", compact, err)
	}
	if _, err = r.conn.ExecContext(t.Context(), `DROP TRIGGER hold_abort_ack`); err != nil {
		t.Fatal(err)
	}
	child, err := service.Fork(t.Context(), "bot", id, entry, "direct-fork")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.reconcileAgent(t.Context(), "bot"); err != nil {
		t.Fatal(err)
	}
	op, err = r.q.GetForkOperation(t.Context(), "pending-fork")
	if err != nil || op.Status != "done" {
		t.Fatalf("fork did not recover: %+v %v", op, err)
	}
	compact, err = r.q.GetCompactOperation(t.Context(), "pending-compact")
	if err != nil || compact.Status != "admitted" {
		t.Fatalf("compact did not recover: %+v %v", compact, err)
	}
	if err = next.CheckIdle(t.Context(), engine.Ref{AgentID: "bot", ID: child.ExecutionID}); err != nil {
		t.Fatalf("fork started work: %v", err)
	}
	data, err := os.ReadFile(starts)
	if err != nil || string(data) != "start\n" {
		t.Fatalf("cancelled work resumed: %q %v", data, err)
	}
	t.Logf("deferred and failed abort acknowledgement blocked direct and pending forks; child=%d", child.ID)
}
