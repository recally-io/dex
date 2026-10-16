package chat_test

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/worker"
	"github.com/recally-io/dex/internal/workerbin"
)

// Delay only RPC dispatch or response delivery. All operations use the real worker.
func TestUserStopsAtSuccessfulQuiesceBoundary(t *testing.T) {
	for _, mode := range []string{"submit_before_lease_store", "abort_before_gate_close"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			dir := t.TempDir()
			conn, err := db.Open(ctx, filepath.Join(dir, "control.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			q := sqlc.New(conn)
			if err := q.CreateAgentIfMissing(ctx, sqlc.CreateAgentIfMissingParams{ID: "default", Name: "Dex"}); err != nil {
				t.Fatal(err)
			}
			if err := q.UpdateAgentSettings(ctx, sqlc.UpdateAgentSettingsParams{
				ID: "default", ModelProvider: "faux", ModelID: "echo", ThinkingLevel: "off",
			}); err != nil {
				t.Fatal(err)
			}
			path, err := worker.Install(t.TempDir(), workerbin.Gzip)
			if err != nil {
				t.Fatal(err)
			}
			settled := make(chan worker.Settled, 4)
			w := worker.New(worker.Config{OnSettled: func(_ context.Context, event worker.Settled) { settled <- event }, Path: path, Dir: dir, Env: []string{
				"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "DEX_DATA_DIR=" + dir, "DEX_FAKE_MODEL=1",
			}})
			runCtx, stop := context.WithCancel(ctx)
			workerDone := make(chan error, 1)
			go func() { workerDone <- w.Run(runCtx) }()
			defer func() {
				stop()
				if err := <-workerDone; err != nil {
					t.Error(err)
				}
			}()
			var created worker.CreateConversationResult
			if err := w.Call(ctx, "createConversation", worker.CreateConversationParams{
				AgentID: "default", Model: worker.ModelRef{Provider: "faux", ModelID: "echo"}, ThinkingLevel: "off",
			}, &created); err != nil {
				t.Fatal(err)
			}
			conv, err := q.EnsureConversation(ctx, sqlc.EnsureConversationParams{AgentID: "default", ChannelKey: "telegram:7"})
			if err != nil {
				t.Fatal(err)
			}
			if err := q.SetWorkerConversation(ctx, sqlc.SetWorkerConversationParams{
				ID: conv.ID, WorkerConversationID: sql.NullInt64{Int64: created.ConversationID, Valid: true},
			}); err != nil {
				t.Fatal(err)
			}

			quiesced, deliverQuiesce := make(chan struct{}), make(chan struct{})
			submitEntered := make(chan struct{}, 4)
			abortEntered, dispatchAbort := make(chan struct{}), make(chan struct{})
			releaseQuiesce := sync.OnceFunc(func() { close(deliverQuiesce) })
			releaseAbort := sync.OnceFunc(func() { close(dispatchAbort) })
			defer releaseQuiesce()
			defer releaseAbort()
			var firstQuiesce sync.Once
			call := maintenanceCall(func(ctx context.Context, method string, params, result any) error {
				if method == "submit" {
					submitEntered <- struct{}{}
				}
				if method == "abort" && mode == "abort_before_gate_close" {
					close(abortEntered)
					select {
					case <-dispatchAbort:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				err := w.Call(ctx, method, params, result)
				if method == "quiesce" && err == nil && mode == "submit_before_lease_store" {
					firstQuiesce.Do(func() {
						close(quiesced)
						select {
						case <-deliverQuiesce:
						case <-ctx.Done():
						}
					})
				}
				return err
			})
			s := chat.New(conn, call, time.Now, nil, nil)
			chatCtx, stopChat := context.WithCancel(ctx)
			chatDone := make(chan struct{})
			go func() { defer close(chatDone); s.Run(chatCtx) }()
			defer func() { stopChat(); <-chatDone }()
			m := channel.New(ctx, conn, s, "default")
			defer m.Close()
			wait := func(ch <-chan struct{}, step string) {
				t.Helper()
				select {
				case <-ch:
				case <-ctx.Done():
					t.Fatal(step, ctx.Err())
				}
			}
			stopped := make(chan error, 1)
			startStop := func() {
				go func() { stopped <- m.Receive(ctx, "telegram:7", "review-stop", chat.Message{Content: "/stop"}) }()
			}

			// When quiesce overlaps either queued submission or an abort dispatch.
			if mode == "submit_before_lease_store" {
				acquired := make(chan error, 1)
				go func() { acquired <- s.AcquireMaintenance(ctx) }()
				wait(quiesced, "real quiesce response")
				if err := m.Receive(ctx, "telegram:7", "review-input", chat.Message{Content: "must not execute"}); err != nil {
					t.Fatal(err)
				}
				// The old implementation dispatches here. With ordered admission, submit
				// waits for the quiesce response instead.
				select {
				case <-submitEntered:
				case <-time.After(time.Second):
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				releaseQuiesce()
				if err := <-acquired; err != nil {
					t.Fatal("successful maintenance required:", err)
				}
				startStop()
			} else {
				startStop()
				wait(abortEntered, "abort dispatch after control checked its lease")
				acquired := make(chan error, 1)
				go func() { acquired <- s.AcquireMaintenance(ctx) }()
				// The old implementation closes the gate before abort dispatch.
				// With ordered admission, quiesce waits for abort to return.
				select {
				case err := <-acquired:
					if err != nil {
						t.Fatal(err)
					}
					releaseAbort()
				case <-time.After(time.Second):
					releaseAbort()
					if err := <-acquired; err != nil {
						t.Fatal("successful maintenance required:", err)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}

			// Stop must complete while the lease is held. A deadline observes the result.
			observe, endObservation := context.WithTimeout(ctx, time.Second)
			defer endObservation()
			select {
			case err := <-stopped:
				if err != nil {
					t.Fatal(err)
				}
			case <-observe.Done():
				var stacks bytes.Buffer
				if err := pprof.Lookup("goroutine").WriteTo(&stacks, 2); err != nil {
					t.Fatal(err)
				}
				stackPath := filepath.Join(t.TempDir(), mode+".stacks.txt")
				if err := os.WriteFile(stackPath, stacks.Bytes(), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := s.AcquireMaintenance(ctx); err != nil {
					t.Fatal("renewal failed while stop was blocked:", err)
				}
				receipt, err := q.GetChannelCommand(ctx, "review-stop")
				if err != nil {
					t.Fatal(err)
				}
				t.Errorf("/stop did not complete under a confirmed lease; lease renewal succeeded; command completed=%d; stack=%s", receipt.Completed, stackPath)
				if err := s.ReleaseMaintenance(ctx); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-stopped:
					if err != nil {
						t.Fatal("stop after release:", err)
					}
					t.Log("/stop completed only after maintenance release")
				case <-ctx.Done():
					t.Fatal("stop still blocked after release:", ctx.Err())
				}
			}
			if err := s.AcquireMaintenance(ctx); err != nil {
				t.Fatal("renew confirmed maintenance:", err)
			}
			receipt, err := q.GetChannelCommand(ctx, "review-stop")
			if err != nil || receipt.Completed != 1 {
				t.Fatalf("stop receipt: %+v, %v", receipt, err)
			}
			if mode == "submit_before_lease_store" {
				pending, err := q.ListPendingSubmissions(ctx)
				if err != nil || len(pending) != 0 {
					t.Fatalf("stop left queued work: %+v, %v", pending, err)
				}
			}
			if err := s.ReleaseMaintenance(ctx); err != nil {
				t.Fatal(err)
			}
			// Then the next message completes after release. The withdrawn message
			// must not execute when submit rechecks its status.
			if mode == "submit_before_lease_store" {
				id := uuid.NewV7().String()
				if _, err := s.Post(ctx, "default", "telegram:7", "", id, "after release"); err != nil {
					t.Fatal(err)
				}
				select {
				case event := <-settled:
					if event.RequestID != id || event.Status != "done" || event.Answer != "echo: after release" {
						t.Fatalf("unexpected worker execution after release: %+v", event)
					}
				case <-ctx.Done():
					t.Fatal("next message did not settle:", ctx.Err())
				}
			}
		})
	}
}
