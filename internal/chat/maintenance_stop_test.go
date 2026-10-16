package chat_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// maintenanceCall lets the test hold a real worker response before control receives it.
type maintenanceCall worker.CallFunc

func (call maintenanceCall) Call(ctx context.Context, method string, params, result any) error {
	return call(ctx, method, params, result)
}

func (call maintenanceCall) Bind() (worker.CallFunc, error) {
	return worker.CallFunc(call), nil
}

func TestUserStopsWhileMaintenanceCheckReportsBusy(t *testing.T) {
	// Given a real worker with an active tool and a delayed quiesce response.
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
	path, err := worker.Install(t.TempDir(), workerbin.Gzip)
	if err != nil {
		t.Fatal(err)
	}
	w := worker.New(worker.Config{Path: path, Dir: dir, Env: []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "DEX_DATA_DIR=" + dir, "DEX_FAKE_MODEL=1",
	}})
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()
	defer func() {
		stop()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	model := worker.ModelRef{Provider: "faux", ModelID: "bash"}
	var created worker.CreateConversationResult
	if err := w.Call(ctx, "createConversation", worker.CreateConversationParams{AgentID: "default", Model: model, ThinkingLevel: "off"}, &created); err != nil {
		t.Fatal(err)
	}
	conv, err := q.EnsureConversation(ctx, sqlc.EnsureConversationParams{AgentID: "default", ChannelKey: "telegram:7"})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.SetWorkerConversation(ctx, sqlc.SetWorkerConversationParams{ID: conv.ID, WorkerConversationID: sql.NullInt64{Int64: created.ConversationID, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	started, release := filepath.Join(dir, "started"), filepath.Join(dir, "release")
	defer func() { _ = os.WriteFile(release, nil, 0o600) }()
	command := fmt.Sprintf("touch '%s'; while [ ! -f '%s' ]; do sleep 0.02; done", started, release)
	if err := w.Call(ctx, "submit", worker.SubmitParams{
		AgentID: "default", ConversationID: created.ConversationID, Model: model, ThinkingLevel: "off",
		RequestID: uuid.NewV7().String(), Content: command, Source: "dex:chat/1", Attachments: []worker.Attachment{},
	}, &worker.SubmitResult{}); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(started); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("tool did not start:", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	checked, deliver := make(chan struct{}), make(chan struct{})
	releaseResponse := sync.OnceFunc(func() { close(deliver) })
	defer releaseResponse()
	aborted := make(chan struct{}, 1)
	call := maintenanceCall(func(ctx context.Context, method string, params, result any) error {
		err := w.Call(ctx, method, params, result)
		if method == "quiesce" && err == nil {
			close(checked)
			select {
			case <-deliver:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if method == "abort" && err == nil {
			aborted <- struct{}{}
		}
		return err
	})
	s := chat.New(conn, call, time.Now, nil, nil)
	m := channel.New(ctx, conn, s, "default")
	defer m.Close()
	acquired := make(chan error, 1)
	go func() { acquired <- s.AcquireMaintenance(ctx) }()
	select {
	case <-checked:
	case <-ctx.Done():
		t.Fatal("maintenance check did not return:", ctx.Err())
	}
	// When /stop arrives during the check, it must abort after the Busy result.
	stopped := make(chan error, 1)
	go func() {
		stopped <- m.Receive(ctx, "telegram:7", "stop-during-check", chat.Message{Content: "/stop"})
	}()
	// A stop that completes before the result must still have reached worker abort.
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-aborted:
		default:
			t.Fatal("/stop skipped the worker abort while quiesce was unconfirmed")
		}
	case <-time.After(time.Second):
		releaseResponse()
		if err := <-stopped; err != nil {
			t.Fatal(err)
		}
		select {
		case <-aborted:
		default:
			t.Fatal("/stop skipped the worker abort after Busy")
		}
	}
	// Then the delayed maintenance check still reports busy.
	releaseResponse()
	if err := <-acquired; !errors.Is(err, chat.ErrMaintenanceBusy) {
		t.Fatalf("maintenance result: %v", err)
	}
}
