package app_test

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vaayne/dex/internal/app"
	"github.com/vaayne/dex/internal/db/sqlc"
	"github.com/vaayne/dex/internal/worker"
)

// These tests run the real embedded worker with pi-ai's faux provider, which answers
// "echo: <message>". They do not prove that a real provider works.

func start(t *testing.T, dataDir, cacheDir string) *app.App {
	t.Helper()
	a, err := app.Start(t.Context(), app.Config{
		DataDir:   dataDir,
		CacheDir:  cacheDir,
		Model:     worker.ModelRef{Provider: "faux", ModelID: "echo"},
		WorkerEnv: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "DEX_FAKE_MODEL=1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func chat(t *testing.T, srv *httptest.Server, message string) map[string]string {
	t.Helper()
	body := strings.NewReader(`{"message":` + quote(message) + `}`)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/api/chat", body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var reply map[string]string
	if err := json.UnmarshalRead(resp.Body, &reply); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/chat: %d %v", resp.StatusCode, reply)
	}
	return reply
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestUserGetsAnswerFromAgent(t *testing.T) {
	// Given a running dex
	a := start(t, t.TempDir(), t.TempDir())
	t.Cleanup(func() { _ = a.Close() })
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)

	// When the user sends two messages in the web chat
	first := chat(t, srv, "hello")
	second := chat(t, srv, "again")

	// Then each message gets the agent's answer
	if first["status"] != "done" || first["answer"] != "echo: hello" {
		t.Fatalf("first reply = %v", first)
	}
	if second["status"] != "done" || second["answer"] != "echo: again" {
		t.Fatalf("second reply = %v", second)
	}
}

func TestUserMessageLeftPendingIsAnsweredAfterRestart(t *testing.T) {
	dataDir, cacheDir := t.TempDir(), t.TempDir()

	// Given a chat that exists, and a message that control stored but the worker never
	// answered, as after a crash
	a := start(t, dataDir, cacheDir)
	srv := httptest.NewServer(a.Handler())
	chat(t, srv, "hello")
	srv.Close()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("sqlite", filepath.Join(dataDir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)
	const pendingID = "0199e1c2-0000-7000-8000-000000000001"
	err = q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{
		ID: pendingID, ConversationID: 1, Content: "are you there", CreatedAt: time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// When dex starts again
	a = start(t, dataDir, cacheDir)
	t.Cleanup(func() { _ = a.Close() })

	// Then the worker answers the pending message
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var last sqlc.Submission
	for {
		last, err = q.GetSubmission(ctx, pendingID)
		if err != nil {
			t.Fatal(err)
		}
		if last.Status != "pending" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("submission still pending: %+v", last)
		case <-time.After(50 * time.Millisecond):
		}
	}
	if last.Status != "done" || last.Answer.String != "echo: are you there" {
		t.Fatalf("submission = %+v", last)
	}
}
