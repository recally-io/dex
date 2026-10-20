package chat

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/mcptest"
)

func warmOtherAgent(t *testing.T, r *nativeTest) {
	t.Helper()
	if err := r.q.CreateAgentIfMissing(t.Context(), sqlc.CreateAgentIfMissingParams{ID: "other", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	if err := r.q.UpdateAgentSettings(t.Context(), sqlc.UpdateAgentSettingsParams{ID: "other", Name: "Other", ModelProvider: "faux", ModelID: "echo", ThinkingLevel: "off"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.service.Post(t.Context(), "other", MainKey, "", "warm", "ready"); err != nil {
		t.Fatal(err)
	}
	awaitReceipt(t, r, "warm")
}

func holdAgentMCP(t *testing.T, r *nativeTest) (<-chan struct{}, func()) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	enteredOnce := sync.OnceFunc(func() { close(entered) })
	releaseOnce := sync.OnceFunc(func() { close(release) })
	server := &mcptest.Server{Dir: t.TempDir()}
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		enteredOnce()
		select {
		case <-release:
			server.ServeHTTP(w, req)
		case <-req.Context().Done():
		}
	}))
	t.Cleanup(endpoint.Close)
	t.Cleanup(releaseOnce)
	config, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"slow": map[string]any{"type": "http", "url": endpoint.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.dir, "agents", "bot", ".agents", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, config, 0600); err != nil {
		t.Fatal(err)
	}
	return entered, releaseOnce
}

func TestUserWarmAgentSendCompletesDuringOtherAgentLoading(t *testing.T) {
	for _, reload := range []bool{false, true} {
		name := "cold open"
		if reload {
			name = "reload"
		}
		t.Run(name, func(t *testing.T) {
			// Given a warm agent and another agent held at a real MCP handshake.
			r := startNative(t, "echo")
			warmOtherAgent(t, r)
			entered, release := holdAgentMCP(t, r)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			loaded := make(chan error, 1)
			go func() {
				if reload {
					loaded <- r.service.ReloadExtensions(ctx, "bot")
				} else {
					_, err := r.service.Post(ctx, "bot", MainKey, "", "cold", "cold")
					loaded <- err
				}
			}()
			defer func() { release(); <-loaded }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("cold agent did not reach MCP")
			}
			// When the user sends to the warm agent before releasing MCP.
			sendCtx, stop := context.WithTimeout(t.Context(), 2*time.Second)
			defer stop()
			sent := make(chan error, 1)
			sendDone := make(chan struct{})
			go func() {
				defer close(sendDone)
				_, err := r.service.Post(sendCtx, "other", MainKey, "", "independent", "hello")
				sent <- err
			}()
			defer func() { release(); <-sendDone }()
			select {
			case err := <-sent:
				if err != nil {
					t.Fatalf("warm agent Send failed during %s: %v", name, err)
				}
			case <-sendCtx.Done():
				t.Fatalf("warm agent Send blocked by another agent's %s", name)
			}
			// Then Send has already durably admitted this input.
			if !receipt(t, r, "independent").AdmittedAt.Valid {
				t.Fatal("Send returned before native admission")
			}
		})
	}
}

func TestUserBackgroundRecoverySkipsOtherAgentLoading(t *testing.T) {
	// Given pending inputs for a cold agent and a warm agent.
	r := startNative(t, "echo")
	warmOtherAgent(t, r)
	if err := r.service.AcquireMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"bot", "other"} {
		if err := r.service.SubmitMessage(t.Context(), agent, "telegram:7", agent+"-pending", Message{Content: "recover"}); err != nil {
			t.Fatal(err)
		}
	}
	id, err := r.q.GetSubmissionBySource(t.Context(), sql.NullString{String: "other-pending", Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := holdAgentMCP(t, r)
	if err := r.service.ReleaseMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	// When recovery opens the cold agent and waits on MCP.
	runReconciler(t, r)
	defer release()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not reach MCP")
	}
	// Then the warm agent's durable work completes before the cold agent opens.
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for receipt(t, r, id).Status == "pending" {
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("warm agent pending recovery blocked by another agent's MCP")
		}
	}
	if sub := receipt(t, r, id); !sub.AdmittedAt.Valid || sub.Answer.String != "echo: recover" {
		t.Fatalf("recovered input: %+v", sub)
	}
}

func TestUserCancelledSameAgentWaitKeepsOrderedDurableInputs(t *testing.T) {
	// Given an agent whose reload is held at a real MCP handshake.
	r := startNative(t, "context")
	entered, release := holdAgentMCP(t, r)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	loaded := make(chan error, 1)
	go func() { loaded <- r.service.ReloadExtensions(ctx, "bot") }()
	defer func() {
		release()
		if err := <-loaded; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("reload did not reach MCP")
	}
	c, err := r.service.Main(t.Context(), "bot")
	if err != nil {
		t.Fatal(err)
	}
	// When two Send callers cancel while waiting, each keeps its committed input.
	for _, id := range []string{"first", "second"} {
		sendCtx, stop := context.WithTimeout(t.Context(), 100*time.Millisecond)
		sent := make(chan error, 1)
		go func() { _, err := r.service.Post(sendCtx, "bot", MainKey, "", id, id); sent <- err }()
		select {
		case err := <-sent:
			if err != nil {
				t.Fatalf("durable input %s rejected: %v", id, err)
			}
		case <-time.After(2 * time.Second):
			stop()
			t.Fatal("cancelled Send did not return")
		}
		stop()
		if sub := receipt(t, r, id); sub.Status != "pending" || sub.AdmittedAt.Valid {
			t.Fatalf("waiting input: %+v", sub)
		}
	}
	// Non-durable operations also stop waiting when their request is cancelled.
	for _, operation := range []func(context.Context) error{
		func(ctx context.Context) error { return r.service.Stop(ctx, "bot", c.ID) },
		func(ctx context.Context) error { return r.service.Clear(ctx, "bot", c.ID, "reset", "web") },
		func(ctx context.Context) error { return r.service.ReloadExtensions(ctx, "bot") },
	} {
		callCtx, stop := context.WithTimeout(t.Context(), 100*time.Millisecond)
		done := make(chan error, 1)
		go func() { done <- operation(callCtx) }()
		select {
		case err := <-done:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("cancelled operation: %v", err)
			}
		case <-time.After(2 * time.Second):
			stop()
			t.Fatal("cancelled operation did not return")
		}
		stop()
	}
	// Then recovery admits both inputs in receipt order after reload releases.
	release()
	runReconciler(t, r)
	awaitReceipt(t, r, "first")
	if sub := awaitReceipt(t, r, "second"); sub.Answer.String != `context: ["first","second"]` {
		t.Fatalf("input order: %+v", sub)
	}
}

func TestUserMaintenanceAndReloadRemainMutuallyExclusive(t *testing.T) {
	// Given an agent whose Reload is held at a real MCP handshake.
	r := startNative(t, "echo")
	entered, release := holdAgentMCP(t, r)
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	loaded := make(chan error, 1)
	go func() { loaded <- r.service.ReloadExtensions(ctx, "bot") }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("reload did not reach MCP")
	}
	// When Reload is active, maintenance must report busy without waiting for MCP.
	busyCtx, stopBusy := context.WithTimeout(ctx, 2*time.Second)
	defer stopBusy()
	maintenance := make(chan error, 1)
	go func() { maintenance <- r.service.AcquireMaintenance(busyCtx) }()
	select {
	case err := <-maintenance:
		if !errors.Is(err, ErrMaintenanceBusy) {
			t.Fatalf("maintenance during held Reload: %v", err)
		}
	case <-busyCtx.Done():
		t.Fatal("maintenance waited for Reload's MCP handshake")
	}
	release()
	select {
	case err := <-loaded:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("reload did not finish after MCP release")
	}
	if err := r.service.AcquireMaintenance(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.service.ReleaseMaintenance(t.Context()); err != nil {
			t.Error(err)
		}
	})
	// Then the confirmed lease blocks Reload until release.
	if err := r.service.ReloadExtensions(ctx, "bot"); !errors.Is(err, engine.ErrMaintenance) {
		t.Fatalf("reload under confirmed lease: %v", err)
	}
	if err := r.service.ReleaseMaintenance(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.service.ReloadExtensions(ctx, "bot"); err != nil {
		t.Fatalf("reload after maintenance release: %v", err)
	}
	t.Log("artifact: active Reload rejects maintenance; confirmed maintenance rejects Reload; release permits Reload")
}

func TestUserMaintenanceConfirmationOrdersWebReceiptCreation(t *testing.T) {
	// Given a web input committed before maintenance confirms its lease.
	r := startNative(t, "echo")
	committed, release := make(chan struct{}), make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	defer releaseOnce()
	var once sync.Once
	r.service.changed = func(int64) { once.Do(func() { close(committed); <-release }) }
	sent := make(chan error, 1)
	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		_, err := r.service.Post(t.Context(), "bot", MainKey, "", "before-lease", "before")
		sent <- err
	}()
	defer func() { releaseOnce(); <-sendDone }()
	select {
	case <-committed:
	case <-time.After(5 * time.Second):
		t.Fatal("receipt was not committed")
	}
	// When maintenance confirms while native admission has not started.
	if err := r.service.AcquireMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.service.Post(t.Context(), "bot", MainKey, "", "after-lease", "after"); !errors.Is(err, engine.ErrMaintenance) {
		t.Fatalf("confirmed maintenance: %v", err)
	}
	if _, err := r.q.GetSubmission(t.Context(), "after-lease"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rejected web receipt was inserted: %v", err)
	}
	releaseOnce()
	err := <-sent
	if err != nil {
		t.Fatal(err)
	}
	if sub := receipt(t, r, "before-lease"); sub.Status != "pending" || sub.AdmittedAt.Valid {
		t.Fatalf("pre-lease receipt: %+v", sub)
	}
	// Then channel input remains durable and both accepted inputs resume on release.
	if err := r.service.SubmitMessage(t.Context(), "bot", "telegram:7", "paused-channel", Message{Content: "channel"}); err != nil {
		t.Fatal(err)
	}
	if err := r.service.ReleaseMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	runReconciler(t, r)
	if sub := awaitReceipt(t, r, "before-lease"); sub.Answer.String != "echo: before" {
		t.Fatalf("accepted web input: %+v", sub)
	}
	id, err := r.q.GetSubmissionBySource(t.Context(), sql.NullString{String: "paused-channel", Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	if sub := awaitReceipt(t, r, id); sub.Answer.String != "echo: channel" {
		t.Fatalf("accepted channel input: %+v", sub)
	}
}

func TestUserReconcilerShutdownJoinsColdAgentWorker(t *testing.T) {
	// Given a durable pending input that needs a cold MCP connection.
	r := startNative(t, "echo")
	if err := r.service.AcquireMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := r.service.SubmitMessage(t.Context(), "bot", "telegram:7", "shutdown-pending", Message{Content: "recover later"}); err != nil {
		t.Fatal(err)
	}
	entered, release := holdAgentMCP(t, r)
	defer release()
	if err := r.service.ReleaseMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); r.service.Run(ctx) }()
	defer func() { release(); cancel(); <-done }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not reach MCP")
	}
	// When the process cancels reconciliation, Run joins its worker before returning.
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not cancel and join its cold worker")
	}
	// Then durable work remains available for a later service instance.
	id, err := r.q.GetSubmissionBySource(t.Context(), sql.NullString{String: "shutdown-pending", Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	if sub := receipt(t, r, id); sub.Status != "pending" || sub.AdmittedAt.Valid {
		t.Fatalf("shutdown lost pending input: %+v", sub)
	}
}

func TestUserTaskResultWakeRunsWhileOtherAgentLoads(t *testing.T) {
	// Given an idle warm parent and its task waiting for a report.
	r := startNative(t, "echo")
	warmOtherAgent(t, r)
	parent, err := r.service.Main(t.Context(), "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{ID: "source", ConversationID: parent.ID, Content: "delegate", Attachments: "null", Origin: "web", Kind: "message", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	marker, taskRelease := filepath.Join(r.dir, "task-started"), filepath.Join(r.dir, "task-release")
	defer func() { _ = os.WriteFile(taskRelease, nil, 0600) }()
	task, err := r.service.SpawnTask(t.Context(), engine.TaskContext{AgentID: "other", ConversationID: parent.ExecutionID, SourceRequestID: "source", Key: "other/result"}, engine.SpawnTaskArgs{
		Title: "Result", Goal: "touch " + marker + "; while [ ! -f " + taskRelease + " ]; do sleep 0.01; done; printf 'saved report'", Provider: "faux", ModelID: "bash",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.q.SettleSubmission(t.Context(), sqlc.SettleSubmissionParams{ID: "source", Status: "done"}); err != nil {
		t.Fatal(err)
	}
	if err := r.service.admit(t.Context(), taskInput(t, r, task.ID)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, err := os.Stat(marker); return err == nil })
	cold, err := r.service.Main(t.Context(), "bot")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{ID: "cold", ConversationID: cold.ID, Content: "cold", Attachments: "null", Origin: "web", Kind: "message", CreatedAt: 2}); err != nil {
		t.Fatal(err)
	}
	entered, release := holdAgentMCP(t, r)
	runReconciler(t, r)
	defer release()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("cold worker did not reach MCP")
	}
	// When the real task finishes, its report wakes the parent without ordinary input.
	if err := os.WriteFile(taskRelease, nil, 0600); err != nil {
		t.Fatal(err)
	}
	awaitReceipt(t, r, task.ID)
	resultID := "task:" + task.ID + ":result"
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for receipt(t, r, resultID).Status == "pending" {
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("task-result-only wake blocked by cold agent")
		}
	}
	// Then the parent receives the saved report before the other agent opens.
	if sub := receipt(t, r, resultID); !sub.AdmittedAt.Valid || !strings.Contains(sub.Answer.String, "saved report") {
		t.Fatalf("parent result: %+v", sub)
	}
}
