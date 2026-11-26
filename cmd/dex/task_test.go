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
	tasksvc "github.com/recally-io/dex/internal/task"
)

func TestUserRunsTaskCLIWithScopedToken(t *testing.T) {
	const owner = "cli-test-owner-token"
	conn, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), `INSERT INTO bots (id,name,model_provider,model_id,created_at) VALUES ('test','Test','faux','echo',1)`); err != nil {
		t.Fatal(err)
	}
	svc := tasksvc.New(conn, nil)
	task, err := svc.CreateTask(t.Context(), tasksvc.Owner, tasksvc.OriginUser, tasksvc.CreateTaskParams{
		AssigneeBotID: "test", Title: "Build from the CLI",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(server.New(server.Config{Token: owner, Tasks: svc}))
	defer srv.Close()
	t.Setenv("DEX_TOKEN", owner)
	t.Setenv("DEX_TASK_TOKEN", "")
	t.Setenv("DEX_TASK_SERVER", srv.URL)
	run := func(out any, args ...string) {
		t.Helper()
		var stdout bytes.Buffer
		if err := runTask(t.Context(), args, &stdout); err != nil {
			t.Fatalf("%s: %v", args[0], err)
		}
		if out != nil {
			if err := json.Unmarshal(stdout.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
	}
	var issued api.TaskTokenIssue
	run(&issued, "attach", "--task", tasksvc.FormatKey(task.Number))
	if issued.TaskToken == "" || issued.Execution.Harness == nil || *issued.Execution.Harness != "custom" {
		t.Fatal("attach did not issue a custom execution and token")
	}
	if err := runTask(t.Context(), []string{"read"}, new(bytes.Buffer)); err == nil || !strings.Contains(err.Error(), "DEX_TASK_TOKEN") {
		t.Fatalf("read without scoped token: %v", err)
	}
	t.Setenv("DEX_TASK_TOKEN", issued.TaskToken)
	t.Setenv("DEX_TOKEN", "")
	var read api.TaskExternalTask
	run(&read, "read")
	if read.Task.Title != task.Title {
		t.Fatalf("wrong Task: %+v", read.Task)
	}
	run(nil, "post", "--content", "from CLI")
	run(nil, "heartbeat", "--branch", "topic")
	t.Setenv("DEX_TOKEN", owner)
	var resume api.TaskTokenIssue
	run(&resume, "attach", "--resume", issued.Execution.Id)
	if err := runTask(t.Context(), []string{"heartbeat"}, new(bytes.Buffer)); err == nil || !strings.Contains(err.Error(), "claim_lost") {
		t.Fatalf("old token did not lose claim: %v", err)
	}
	t.Setenv("DEX_TASK_TOKEN", resume.TaskToken)
	run(nil, "finish", "--outcome", "completed")
}
