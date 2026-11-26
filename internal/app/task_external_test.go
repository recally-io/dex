package app_test

import (
	"encoding/json/v2"
	"fmt"
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

func TestUserExternalTaskTokenOnlyActsOnItsExecution(t *testing.T) {
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
	var first, second api.TaskSummary
	request("POST", "/api/tasks", owner, `{"title":"First","assigneeBotId":"default","status":"in_progress","note":"Task brief"}`, 201, &first)
	request("POST", "/api/tasks", owner, `{"title":"Second","assigneeBotId":"default"}`, 201, &second)
	conn, err := db.Open(t.Context(), filepath.Join(dir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), `INSERT INTO task_entries (task_id,seq,kind,trust,actor_kind,content,meta_json,created_at) VALUES
		(?1,1000,'external','external','external','Hidden source body','{}',1),
		(?1,1001,'artifact','owner','owner','Saved association','{}',1)`, first.Id); err != nil {
		t.Fatal(err)
	}
	firstPath, secondPath := "/api/tasks/"+first.Key, "/api/tasks/"+second.Key
	var initial, resumed api.TaskTokenIssue
	request("POST", firstPath+"/executions/attach", owner, `{"harness":"codex","hostMeta":{"hostname":"reported-host"},"workspacePath":"/workspace"}`, 201, &initial)
	if !strings.HasPrefix(initial.TaskToken, "dex_tt_") {
		t.Fatal("attach did not issue a Task token")
	}
	if initial.Execution.RunnerId != nil || initial.Execution.Hostname == nil || *initial.Execution.Hostname != "reported-host" {
		t.Fatalf("self-reported host must not identify a registered Runner: %+v", initial.Execution)
	}
	const external = "/api/tasks/external/"
	request("GET", external+"task", owner, "", 401, nil)
	request("GET", external+"task", "dex_tt_invalid", "", 401, nil)
	request("PATCH", firstPath, initial.TaskToken, `{"status":"done"}`, 401, nil)
	request("GET", secondPath, initial.TaskToken, "", 401, nil)
	request("POST", secondPath+"/executions/attach", initial.TaskToken, `{"harness":"custom"}`, 401, nil)
	var view api.TaskExternalTask
	request("GET", external+"task?limit=1", initial.TaskToken, "", 200, &view)
	if view.Task.Key != first.Key || len(view.Entries) != 1 || view.Entries[0].Content != "Task brief" || view.NextCursor != 1 {
		t.Fatalf("first page: %+v", view)
	}
	request("GET", fmt.Sprintf("%stask?afterSeq=%d", external, view.NextCursor), initial.TaskToken, "", 200, &view)
	if len(view.Entries) != 2 || view.Entries[0].Kind != "execution" || view.Entries[1].Content != "Saved association" || view.NextCursor != 1001 {
		t.Fatalf("external read must include the saved association but not the source entry: %+v", view.Entries)
	}
	request("POST", external+"entries", initial.TaskToken, `{"kind":"status","content":"done"}`, 400, nil)
	request("POST", external+"entries", initial.TaskToken, `{"kind":"artifact","content":"Build output"}`, 201, nil)
	var heartbeat api.TaskExecution
	request("POST", external+"heartbeat", initial.TaskToken, `{"gitBranch":"topic","externalSessionId":"session-1"}`, 200, &heartbeat)
	if heartbeat.GitBranch == nil || *heartbeat.GitBranch != "topic" || heartbeat.LastHeartbeatAt == nil {
		t.Fatalf("heartbeat: %+v", heartbeat)
	}
	if _, err := conn.ExecContext(t.Context(), `UPDATE task_external_runs SET lease_expires_at = 1 WHERE id = ?`, initial.Execution.Id); err != nil {
		t.Fatal(err)
	}
	var failure api.Error
	request("POST", external+"finish", initial.TaskToken, `{"outcome":"completed"}`, 409, &failure)
	if failure.Error != "claim_lost" {
		t.Fatalf("expired response: %+v", failure)
	}
	request("POST", "/api/tasks/executions/"+initial.Execution.Id+"/resume", owner, `{}`, 200, &resumed)
	request("POST", external+"heartbeat", initial.TaskToken, `{}`, 409, &failure)
	if failure.Error != "claim_lost" || resumed.TaskToken == initial.TaskToken || resumed.Execution.LeaseEpoch != 2 {
		t.Fatal("resume did not replace the claim")
	}
	request("POST", external+"finish", resumed.TaskToken, `{"outcome":"completed","summary":"Build passed"}`, 200, nil)
	var task api.TaskDetail
	request("GET", firstPath, owner, "", 200, &task)
	if task.Task.Status != "in_progress" {
		t.Fatalf("finish changed Task status: %s", task.Task.Status)
	}
	var list api.TaskExecutionList
	request("GET", "/api/tasks/executions?kind=external_agent&state=completed&taskKey="+first.Key, owner, "", 200, &list)
	if len(list.Executions) != 1 || list.Executions[0].Id != initial.Execution.Id {
		t.Fatalf("filtered executions: %+v", list)
	}

	// The execution list still includes Session links when no kind is selected.
	q := sqlc.New(conn)
	conv, err := q.EnsureSession(t.Context(), sqlc.EnsureSessionParams{BotID: "default", ChannelKey: "chat:list", Title: "List"})
	if err != nil {
		t.Fatal(err)
	}
	link, err := q.LinkTaskSession(t.Context(), sqlc.LinkTaskSessionParams{TaskID: first.Id, SessionID: conv.ID, LinkedAt: initial.Execution.StartedAt + 1})
	if err != nil {
		t.Fatal(err)
	}
	listPath := "/api/tasks/executions?taskKey=" + first.Key
	request("GET", listPath, owner, "", 200, &list)
	if len(list.Executions) != 2 || list.Executions[0].Id != link.ID || list.Executions[1].Id != initial.Execution.Id {
		t.Fatalf("combined execution list: %+v", list)
	}
	request("GET", listPath+"&kind=dex_session&state=running", owner, "", 200, &list)
	if len(list.Executions) != 1 || list.Executions[0].Kind != api.DexSession || list.Executions[0].LeaseEpoch != 0 ||
		list.Executions[0].TaskKey == nil || *list.Executions[0].TaskKey != first.Key {
		t.Fatalf("Session execution list: %+v", list)
	}
}

func TestUserDispatchesExternalBoardRunThroughARunnerProcess(t *testing.T) {
	dir := t.TempDir()
	d := startDex(t, dir, "")
	m := enrollMachine(t, d)
	m.connect(d)
	var bot apiBot
	d.must(t, http.MethodGet, "/api/bots/default", nil, &bot)
	bot.Provider, bot.ModelID, bot.ThinkingLevel, bot.RunnerID = "faux", "tool", "off", m.envID
	d.must(t, http.MethodPut, "/api/bots/default", bot, nil)
	var main sessionListItem
	d.must(t, http.MethodGet, "/api/bots/default/sessions/main", nil, &main)
	var task api.TaskSummary
	d.must(t, http.MethodPost, "/api/tasks", map[string]string{"title": "Dispatch a build", "assigneeBotId": "default"}, &task, http.StatusCreated)
	workspace := filepath.Join(m.state, "workspace")
	answer := taskTurn(t, d, main.ID, "task_dispatch_external "+taskJSON(t, map[string]string{
		"id": task.Key, "harness": "custom", "workspace": workspace, "server": d.srv.URL,
		"command": `test -n "$DEX_TASK_TOKEN" && printf '%s' "$DEX_TASK_TOKEN" && : > ran`,
	}))
	if !strings.Contains(answer, "command-") || strings.Contains(answer, "dex_tt_") {
		t.Fatal("the tool did not return a Process ID without the Task token")
	}
	var runs api.TaskExecutionList
	d.must(t, http.MethodGet, "/api/tasks/executions?runnerId="+m.envID+"&kind=external_agent&state=running", nil, &runs)
	if len(runs.Executions) != 1 || runs.Executions[0].DispatchId == nil || runs.Executions[0].RunnerName == nil ||
		runs.Executions[0].RunnerId == nil || *runs.Executions[0].RunnerId != m.envID {
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
		if err != nil || len(output) != 0 || strings.Contains(p.Command, "dex_tt_") {
			t.Fatal("the managed Process persisted command output or token data")
		}
		_, err = os.Stat(filepath.Join(workspace, "ran"))
		return err == nil, err
	})
}
