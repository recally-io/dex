package main

import (
	"bytes"
	"encoding/json/v2"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/server"
	"github.com/recally-io/dex/internal/worktask"
)

func TestUserRunsBoardCLIWithScopedToken(t *testing.T) {
	const owner = "cli-test-owner-token"
	conn, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), `INSERT INTO agents (id,name,model_provider,model_id,created_at) VALUES ('test','Test','faux','echo',1)`); err != nil {
		t.Fatal(err)
	}
	svc := worktask.New(conn, nil)
	task, err := svc.Create(t.Context(), "test", "Build from the CLI")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(server.New(server.Config{Token: owner, WorkTasks: svc}))
	defer srv.Close()
	t.Setenv("DEX_TOKEN", owner)
	t.Setenv("DEX_BOARD_TOKEN", "")
	t.Setenv("DEX_BOARD_SERVER", srv.URL)
	run := func(out any, args ...string) {
		t.Helper()
		var stdout bytes.Buffer
		if err := runBoard(t.Context(), args, &stdout); err != nil {
			t.Fatalf("%s: %v", args[0], err)
		}
		if out != nil {
			if err := json.Unmarshal(stdout.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
	}
	var issued api.BoardTokenIssue
	run(&issued, "attach", "--task", worktask.FormatKey(task.Number))
	if issued.BoardToken == "" || issued.Execution.Harness == nil || *issued.Execution.Harness != "custom" {
		t.Fatal("attach did not issue a custom execution and token")
	}
	if err := runBoard(t.Context(), []string{"read"}, new(bytes.Buffer)); err == nil || !strings.Contains(err.Error(), "DEX_BOARD_TOKEN") {
		t.Fatalf("read without scoped token: %v", err)
	}
	t.Setenv("DEX_BOARD_TOKEN", issued.BoardToken)
	t.Setenv("DEX_TOKEN", "")
	var read api.BoardExternalTask
	run(&read, "read")
	if read.Task.Title != task.Title {
		t.Fatalf("wrong Task: %+v", read.Task)
	}
	run(nil, "post", "--content", "from CLI")
	run(nil, "heartbeat", "--branch", "topic")
	t.Setenv("DEX_TOKEN", owner)
	var resume api.BoardTokenIssue
	run(&resume, "attach", "--resume", issued.Execution.Id)
	if err := runBoard(t.Context(), []string{"heartbeat"}, new(bytes.Buffer)); err == nil || !strings.Contains(err.Error(), "claim_lost") {
		t.Fatalf("old token did not lose claim: %v", err)
	}
	t.Setenv("DEX_BOARD_TOKEN", resume.BoardToken)
	run(nil, "finish", "--outcome", "completed")
}
