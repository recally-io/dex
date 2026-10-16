package chat_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/worker"
	"github.com/recally-io/dex/internal/workerbin"
)

func TestUserKeepsInputAdmittedAfterClear(t *testing.T) {
	for _, name := range []string{"live", "restart_before_delivery", "restart_during_reset"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			dir := t.TempDir()
			conn, err := db.Open(ctx, filepath.Join(dir, "control.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			q := sqlc.New(conn)
			if err := q.CreateAgentIfMissing(ctx, sqlc.CreateAgentIfMissingParams{ID: "bot", Name: "Bot"}); err != nil {
				t.Fatal(err)
			}
			if err := q.UpdateAgentSettings(ctx, sqlc.UpdateAgentSettingsParams{ID: "bot", Name: "Bot", ModelProvider: "faux", ModelID: "context", ThinkingLevel: "off"}); err != nil {
				t.Fatal(err)
			}
			c, err := q.EnsureConversation(ctx, sqlc.EnsureConversationParams{AgentID: "bot", ChannelKey: "main"})
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			entered, resume := make(chan struct{}), make(chan struct{})
			release := sync.OnceFunc(func() { close(resume) })
			defer release()
			now := func() time.Time {
				n := calls.Add(1)
				if n == 2 {
					// Sample the input time before Clear, but admit the input after Clear.
					close(entered)
					select {
					case <-resume:
					case <-ctx.Done():
					}
					return time.UnixMilli(1000)
				}
				return time.UnixMilli(2000)
			}
			path, err := worker.Install(t.TempDir(), workerbin.Gzip)
			if err != nil {
				t.Fatal(err)
			}
			var service *chat.Service
			var restarted atomic.Bool
			start := func(deliver bool) func() {
				runCtx, stop := context.WithCancel(ctx)
				var s *chat.Service
				w := worker.New(worker.Config{
					Path: path, Dir: dir,
					Env:       []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "DEX_FAKE_MODEL=1", "DEX_DATA_DIR=" + dir},
					OnSettled: func(ctx context.Context, event worker.Settled) { s.HandleSettled(ctx, event) },
					OnStart:   func(ctx context.Context) { s.Replay(ctx) },
				})
				call := maintenanceCall(func(ctx context.Context, method string, params, result any) error {
					if name == "restart_during_reset" && method == "reset" && restarted.CompareAndSwap(false, true) {
						// Lose the old process before dispatch. The bound RPC returns its real exit error.
						old, err := w.Bind()
						if err != nil {
							return err
						}
						if err := w.Restart(ctx); err != nil {
							return err
						}
						return old(ctx, method, params, result)
					}
					return w.Call(ctx, method, params, result)
				})
				s = chat.New(conn, call, now, nil, nil)
				service = s
				var running sync.WaitGroup
				running.Go(func() { _ = w.Run(runCtx) })
				if deliver {
					s.Replay(ctx)
					running.Go(func() { s.Run(runCtx) })
				}
				return sync.OnceFunc(func() { stop(); running.Wait() })
			}
			stop := start(false)
			defer func() { stop() }()

			// Given an input paused before its insert, Clear is admitted first.
			sent := make(chan error, 1)
			go func() {
				_, err := service.Send(ctx, "bot", "main", chat.Message{ID: "later-input", Content: "must survive clear"})
				sent <- err
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("input did not sample its time")
			}
			err = service.Clear(ctx, "bot", c.ID, "reset-first", "web")
			release()
			if err != nil {
				t.Fatal(err)
			}
			if err := <-sent; err != nil {
				t.Fatal(err)
			}
			waitDone := func(id string) sqlc.Submission {
				t.Helper()
				var row sqlc.Submission
				taskEventually(t, func() (bool, any) {
					row, err = q.GetSubmission(ctx, id)
					return err == nil && row.Status != "pending", row
				})
				if row.Status != "done" {
					t.Fatalf("input did not complete: %+v", row)
				}
				return row
			}
			reopen := func() {
				t.Helper()
				if err := conn.Close(); err != nil {
					t.Fatal(err)
				}
				conn, err = db.Open(ctx, filepath.Join(dir, "control.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				q = sqlc.New(conn)
			}

			// When control delivers, either now or after a full runtime restart.
			var stopDelivery func()
			if name == "restart_before_delivery" {
				stop()
				reopen()
				stop = start(true)
			} else {
				deliveryCtx, cancelDelivery := context.WithCancel(ctx)
				done := make(chan struct{})
				s := service
				go func() { defer close(done); s.Run(deliveryCtx) }()
				stopDelivery = sync.OnceFunc(func() { cancelDelivery(); <-done })
				defer stopDelivery()
			}
			waitDone("reset-first")
			waitDone("later-input")
			if _, err := service.Send(ctx, "bot", "main", chat.Message{ID: "next-input", Content: "next"}); err != nil {
				t.Fatal(err)
			}
			// Then the model still has the input admitted after Clear.
			next := waitDone("next-input")
			if want := `context: ["must survive clear","next"]`; next.Answer.String != want {
				t.Fatalf("Clear discarded a later admitted input: got %s, want %s", next.Answer.String, want)
			}
			if stopDelivery != nil {
				stopDelivery()
			}
			stop()

			// Replay lost receipts after both worker writes have committed.
			if _, err := conn.ExecContext(ctx, "UPDATE submissions SET status='pending', settled_at=NULL WHERE id IN ('reset-first', 'later-input')"); err != nil {
				t.Fatal(err)
			}
			reopen()
			stop = start(true)
			waitDone("reset-first")
			waitDone("later-input")
			if _, err := service.Send(ctx, "bot", "main", chat.Message{ID: "after-replay", Content: "after replay"}); err != nil {
				t.Fatal(err)
			}
			after := waitDone("after-replay")
			if want := `context: ["must survive clear","next","after replay"]`; after.Answer.String != want {
				t.Fatalf("replay discarded model context: got %s, want %s", after.Answer.String, want)
			}
			t.Logf("model context before restart=%s; after replay=%s", next.Answer.String, after.Answer.String)
		})
	}
}
