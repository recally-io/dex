package app_test

import (
	"encoding/json/v2"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserExternalBoardTokenOnlyActsOnItsExecution(t *testing.T) {
	t.Parallel()
	const owner = "owner-test-access-token"
	dir := t.TempDir()
	d := startDexWith(t, app.Config{DataDir: dir, Token: owner})
	request := func(method, path, bearer, body string, want int, out any) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, d.srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := d.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != want {
			t.Fatalf("%s %s: got %d, want %d", method, path, res.StatusCode, want)
		}
		if out != nil {
			if err := json.Unmarshal(raw, out); err != nil {
				t.Fatal(err)
			}
		}
	}
	var first, second api.BoardTaskSummary
	request("POST", "/api/board/tasks", owner, `{"title":"First","assigneeBotId":"default","status":"in_progress","note":"Task brief"}`, 201, &first)
	request("POST", "/api/board/tasks", owner, `{"title":"Second","assigneeBotId":"default"}`, 201, &second)
	conn, err := db.Open(t.Context(), filepath.Join(dir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), `INSERT INTO work_task_entries (task_id,seq,kind,trust,actor_kind,content,created_at) VALUES (?,1000,'external','external','external','Hidden source body',1)`, first.Id); err != nil {
		t.Fatal(err)
	}
	firstPath, secondPath := "/api/board/tasks/"+first.Key, "/api/board/tasks/"+second.Key
	var initial, resumed api.BoardTokenIssue
	request("POST", firstPath+"/executions/attach", owner, `{"harness":"codex","hostMeta":{"hostname":"reported-host"},"workspacePath":"/workspace"}`, 201, &initial)
	if !strings.HasPrefix(initial.BoardToken, "dex_bt_") {
		t.Fatal("attach did not issue a Board Token")
	}
	const external = "/api/board/external/"
	request("GET", external+"task", owner, "", 401, nil)
	request("GET", external+"task", "dex_bt_invalid", "", 401, nil)
	request("PATCH", firstPath, initial.BoardToken, `{"status":"done"}`, 401, nil)
	request("GET", secondPath, initial.BoardToken, "", 401, nil)
	request("POST", secondPath+"/executions/attach", initial.BoardToken, `{"harness":"custom"}`, 401, nil)
	var view api.BoardExternalTask
	request("GET", external+"task?limit=1", initial.BoardToken, "", 200, &view)
	if view.Task.Key != first.Key || len(view.Entries) != 1 || view.Entries[0].Content != "Task brief" || view.NextAfterSeq != 1 {
		t.Fatalf("first page: %+v", view)
	}
	request("GET", external+"task?afterSeq=1", initial.BoardToken, "", 200, &view)
	if len(view.Entries) != 1 || view.Entries[0].Kind != "execution" {
		t.Fatalf("external read did not filter the source entry: %+v", view.Entries)
	}
	request("POST", external+"entries", initial.BoardToken, `{"kind":"status","content":"done"}`, 400, nil)
	request("POST", external+"entries", initial.BoardToken, `{"kind":"artifact","content":"Build output"}`, 201, nil)
	var heartbeat api.BoardExecution
	request("POST", external+"heartbeat", initial.BoardToken, `{"gitBranch":"topic","externalSessionId":"session-1"}`, 200, &heartbeat)
	if heartbeat.GitBranch == nil || *heartbeat.GitBranch != "topic" || heartbeat.LastHeartbeatAt == nil {
		t.Fatalf("heartbeat: %+v", heartbeat)
	}
	if _, err := conn.ExecContext(t.Context(), `UPDATE task_executions SET lease_expires_at = 1 WHERE id = ?`, initial.Execution.Id); err != nil {
		t.Fatal(err)
	}
	var failure api.Error
	request("POST", external+"finish", initial.BoardToken, `{"outcome":"completed"}`, 409, &failure)
	if failure.Error != "claim_lost" {
		t.Fatalf("expired response: %+v", failure)
	}
	request("POST", "/api/board/executions/"+initial.Execution.Id+"/resume", owner, `{}`, 200, &resumed)
	request("POST", external+"heartbeat", initial.BoardToken, `{}`, 409, &failure)
	if failure.Error != "claim_lost" || resumed.BoardToken == initial.BoardToken || resumed.Execution.LeaseEpoch != 2 {
		t.Fatal("resume did not replace the claim")
	}
	request("POST", external+"finish", resumed.BoardToken, `{"outcome":"completed","summary":"Build passed"}`, 200, nil)
	var task api.BoardTaskDetail
	request("GET", firstPath, owner, "", 200, &task)
	if task.Task.Status != "in_progress" {
		t.Fatalf("finish changed Task status: %s", task.Task.Status)
	}
	var list api.BoardExecutionList
	request("GET", "/api/board/executions?kind=external_agent&state=completed&taskKey="+first.Key, owner, "", 200, &list)
	if len(list.Executions) != 1 || list.Executions[0].Id != initial.Execution.Id {
		t.Fatalf("filtered executions: %+v", list)
	}
}

func TestUserDispatchesExternalBoardRunThroughARunnerProcess(t *testing.T) {
	dir := t.TempDir()
	d := startDex(t, dir, "")
	m := enrollMachine(t, d)
	m.connect(d)
	var bot apiAgent
	d.must(t, http.MethodGet, "/api/agents/default", nil, &bot)
	bot.Provider, bot.ModelID, bot.ThinkingLevel, bot.EnvironmentID = "faux", "tool", "off", m.envID
	d.must(t, http.MethodPut, "/api/agents/default", bot, nil)
	var main chatInfo
	d.must(t, http.MethodGet, "/api/agents/default/chats/main", nil, &main)
	var task api.BoardTaskSummary
	d.must(t, http.MethodPost, "/api/board/tasks", map[string]string{"title": "Dispatch a build", "assigneeBotId": "default"}, &task, http.StatusCreated)
	workspace := filepath.Join(m.state, "workspace")
	answer := taskTurn(t, d, main.ID, "task_dispatch_external "+taskJSON(t, map[string]string{
		"id": task.Key, "harness": "custom", "workspace": workspace, "server": d.srv.URL,
		"command": `test -n "$DEX_BOARD_TOKEN" && printf '%s' "$DEX_BOARD_TOKEN" && : > ran`,
	}))
	if !strings.Contains(answer, "command-") || strings.Contains(answer, "dex_bt_") {
		t.Fatal("the tool did not return a Process ID without the Board Token")
	}
	var runs api.BoardExecutionList
	d.must(t, http.MethodGet, "/api/board/executions?machineId="+m.envID+"&kind=external_agent&state=running", nil, &runs)
	if len(runs.Executions) != 1 || runs.Executions[0].DispatchId == nil || runs.Executions[0].RunnerName == nil {
		t.Fatalf("registered Runner did not report the dispatch: %+v", runs)
	}
	conn, err := db.Open(t.Context(), filepath.Join(dir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	q := sqlc.New(conn)
	eventually(t, "Process completes on the Runner", func() (bool, any) {
		p, err := q.GetBackgroundCommandByKey(t.Context(), *runs.Executions[0].DispatchId)
		if err != nil || p.State != "exited" {
			return false, p.State
		}
		if p.ExitStatus.Int64 != 0 {
			t.Fatalf("Process exited with %d", p.ExitStatus.Int64)
		}
		output, err := os.ReadFile(p.OutputPath)
		if err != nil || len(output) != 0 || strings.Contains(p.Command, "dex_bt_") {
			t.Fatal("the managed Process persisted command output or token data")
		}
		_, err = os.Stat(filepath.Join(workspace, "ran"))
		return err == nil, err
	})
}
