package chat_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/worker"
	"github.com/recally-io/dex/internal/workerbin"
)

func TestUserDelegationRPCDeadlineReachesTheWorker(t *testing.T) {
	// Given a real task RPC whose SQLite connection is unavailable until the handler expires.
	dir := t.TempDir()
	conn, err := db.Open(t.Context(), filepath.Join(dir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	q := sqlc.New(conn)
	if err := q.CreateAgentIfMissing(t.Context(), sqlc.CreateAgentIfMissingParams{ID: "bot", Name: "Dex"}); err != nil {
		t.Fatal(err)
	}
	if err := q.UpdateAgentSettings(t.Context(), sqlc.UpdateAgentSettingsParams{ID: "bot", ModelProvider: "faux", ModelID: "spawn", ThinkingLevel: "off"}); err != nil {
		t.Fatal(err)
	}
	path, err := worker.Install(t.TempDir(), workerbin.Gzip)
	if err != nil {
		t.Fatal(err)
	}
	var service *chat.Service
	entered, handled := make(chan struct{}, 1), make(chan error, 1)
	settled := make(chan worker.Settled, 1)
	w := worker.New(worker.Config{Path: path, Dir: dir,
		Env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "DEX_DATA_DIR=" + dir, "DEX_FAKE_MODEL=1"},
		OnTask: func(ctx context.Context, request worker.TaskRequest) (any, error) {
			entered <- struct{}{}
			result, err := service.HandleTask(ctx, request)
			handled <- err
			return result, err
		},
		OnSettled: func(_ context.Context, event worker.Settled) { settled <- event },
	})
	service = chat.New(conn, w, time.Now, nil, nil)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	defer func() { cancel(); <-done }()
	var created worker.CreateConversationResult
	if err := w.Call(t.Context(), "createConversation", worker.CreateConversationParams{AgentID: "bot", Model: worker.ModelRef{Provider: "faux", ModelID: "spawn"}, ThinkingLevel: "off"}, &created); err != nil {
		t.Fatal(err)
	}
	id, err := service.Post(t.Context(), "bot", "web:rpc", "RPC", "source", "seed")
	if err != nil {
		t.Fatal(err)
	}
	if err := q.SetWorkerConversation(t.Context(), sqlc.SetWorkerConversationParams{ID: id, WorkerConversationID: sql.NullInt64{Int64: created.ConversationID, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	held, err := conn.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := w.Call(t.Context(), "submit", worker.SubmitParams{AgentID: "bot", ConversationID: created.ConversationID,
		Model: worker.ModelRef{Provider: "faux", ModelID: "spawn"}, ThinkingLevel: "off", RequestID: "source",
		Content: `{"title":"Deadline","goal":"must not start"}`, Attachments: []worker.Attachment{}, Source: "dex:chat/rpc"}, &worker.SubmitResult{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("task RPC did not start")
	}
	// When the control handler reaches its real 30-second deadline, it still sends the error.
	select {
	case err := <-handled:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("handler deadline: %v", err)
		}
	case <-time.After(35 * time.Second):
		t.Fatal("control deadline did not expire")
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	// Then the tool receives the control error before the worker's separate response timeout.
	select {
	case event := <-settled:
		if event.Status != "done" || !strings.Contains(event.Answer, "context deadline exceeded") {
			t.Fatalf("deadline was not returned to the calling tool: %+v", event)
		}
		t.Logf("RPC deadline reached worker: %s", event.Answer)
	case <-time.After(3 * time.Second):
		t.Fatal("worker kept waiting after the control handler expired")
	}
	if err := w.Call(t.Context(), "ping", struct{}{}, &struct{}{}); err != nil {
		t.Fatal(err)
	}
}
