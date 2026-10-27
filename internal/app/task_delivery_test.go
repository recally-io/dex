package app_test

import (
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserTaskNativeCompletionSurvivesRestartBeforeControlSettlement(t *testing.T) {
	// Given a real app whose control settlement fails after native completion.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "control.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(t.Context(), `CREATE TRIGGER hold_task_settlement BEFORE UPDATE OF status ON tasks WHEN NEW.status IN ('done','failed') BEGIN SELECT RAISE(FAIL, 'crash window before control settlement'); END`); err != nil {
		t.Fatal(err)
	}
	d.setModel(t, "spawn")
	parent := d.newChat(t, taskJSON(t, map[string]string{"title": "Recover native report", "goal": "native full report", "provider": "faux", "modelId": "echo"}))
	task := waitExecution(t, conn, parent, "Recover native report", "running")
	var conversation int64
	if err = conn.QueryRowContext(t.Context(), "SELECT conversation_id FROM tasks WHERE id = ?", task.ID).Scan(&conversation); err != nil {
		t.Fatal(err)
	}
	w := d.watch(t, conversation)
	w.until(t, "native task answer committed", answered("echo: native full report"))
	w.cancel()
	d.stop()
	if _, err = conn.ExecContext(t.Context(), "DROP TRIGGER hold_task_settlement"); err != nil {
		t.Fatal(err)
	}
	// When the app restarts before the report reaches control.
	d = startDex(t, dir, "")
	task = waitExecution(t, conn, parent, "Recover native report", "done")
	if task.Report != "echo: native full report" {
		t.Fatalf("native report lost: %+v", task)
	}
	resultID := "task:" + task.ID + ":result"
	result := waitResultSubmission(t, conn, resultID, "done")
	if !strings.Contains(result.answer, "native full report") {
		t.Fatalf("parent lost report: %+v", result)
	}
	d.stop()
	// Then another restart preserves exactly one record and parent input.
	d = startDex(t, dir, "")
	var count int
	if err = conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE id = ?", resultID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("result count=%d err=%v", count, err)
	}
	var saved map[string]any
	d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/tasks/%s", task.ID), nil, &saved)
	if saved["status"] != "done" {
		t.Fatalf("terminal task changed: %+v", saved)
	}
	t.Logf("artifact: task=%s result=%s report=%q count=%d", task.ID, resultID, task.Report, count)
}

func TestUserTaskNativeFailureSurvivesRestartBeforeControlSettlement(t *testing.T) {
	// Given a real app whose control settlement fails after the native execution failed.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "control.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(t.Context(), `CREATE TRIGGER hold_task_settlement BEFORE UPDATE OF status ON tasks WHEN NEW.status IN ('done','failed') BEGIN SELECT RAISE(FAIL, 'crash window before control settlement'); END`); err != nil {
		t.Fatal(err)
	}
	d.setModel(t, "spawn")
	parent := d.newChat(t, taskJSON(t, map[string]string{"title": "Recover native failure", "goal": "invalid JSON", "provider": "faux", "modelId": "spawn"}))
	task := waitExecution(t, conn, parent, "Recover native failure", "running")
	var conversation int64
	if err = conn.QueryRowContext(t.Context(), "SELECT conversation_id FROM tasks WHERE id = ?", task.ID).Scan(&conversation); err != nil {
		t.Fatal(err)
	}
	w := d.watch(t, conversation)
	w.until(t, "native task failure committed", func(tr *transcript) bool {
		return !tr.running && slices.ContainsFunc(tr.items, func(it item) bool { return it.Kind == "error" })
	})
	w.cancel()
	d.stop()
	if _, err = conn.ExecContext(t.Context(), "DROP TRIGGER hold_task_settlement"); err != nil {
		t.Fatal(err)
	}
	// When the app restarts before the failure reaches control.
	d = startDex(t, dir, "")
	task = waitExecution(t, conn, parent, "Recover native failure", "failed")
	if task.Report != "model_error" {
		t.Fatalf("native failure became another outcome: %+v", task)
	}
	// Then the parent receives one failed result, and another restart keeps it single.
	resultID := "task:" + task.ID + ":result"
	result := waitResultSubmission(t, conn, resultID, "done")
	if !strings.Contains(result.content, `"status":"failed"`) || !strings.Contains(result.content, `"goalMet":false`) {
		t.Fatalf("parent result: %+v", result)
	}
	d.stop()
	_ = startDex(t, dir, "")
	var count int
	if err = conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE id = ?", resultID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("result count=%d err=%v", count, err)
	}
	t.Logf("artifact: task=%s result=%s report=%q count=%d", task.ID, resultID, task.Report, count)
}

func TestUserStructuredTaskReportSurvivesRestartBeforeControlSettlement(t *testing.T) {
	// Given a structured report staged by the real task tool and a failed control commit.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "control.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(t.Context(), `CREATE TRIGGER hold_structured_settlement BEFORE UPDATE OF status ON tasks WHEN NEW.status IN ('done','failed') BEGIN SELECT RAISE(FAIL, 'structured settlement window'); END`); err != nil {
		t.Fatal(err)
	}
	d.setModel(t, "spawn")
	report := engine.TaskReport{Outcome: "done", Summary: "native structured evidence", Cause: "checked cause", Reproduction: []engine.TaskCommand{{Command: "pwd", Ran: true}}, Files: []string{"main.go"}, OpenItems: []string{}}
	reportJSON, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	parent := d.newChat(t, taskJSON(t, map[string]string{"title": "Recover structured report", "goal": "report_task " + string(reportJSON), "provider": "faux", "modelId": "tool"}))
	task := waitExecution(t, conn, parent, "Recover structured report", "running")
	var conversation int64
	if err = conn.QueryRowContext(t.Context(), "SELECT conversation_id FROM tasks WHERE id = ?", task.ID).Scan(&conversation); err != nil {
		t.Fatal(err)
	}
	w := d.watch(t, conversation)
	w.until(t, "native structured report completed", answered("tool: Task report saved."))
	w.cancel()
	d.stop()
	if _, err = conn.ExecContext(t.Context(), "DROP TRIGGER hold_structured_settlement"); err != nil {
		t.Fatal(err)
	}
	// When recovery reads the native completion, it settles the staged report.
	d = startDex(t, dir, "")
	task = waitExecution(t, conn, parent, task.Title, "done")
	if task.Report != string(reportJSON) {
		t.Fatalf("structured recovery lost report: %+v", task)
	}
	resultID := "task:" + task.ID + ":result"
	result := waitResultSubmission(t, conn, resultID, "done")
	if !strings.Contains(result.content, `"goalMet":true`) || !strings.Contains(result.answer, report.Summary) {
		t.Fatalf("structured recovery result: %+v", result)
	}
	// Then get_task still returns the full report after result admission and another restart.
	d.stop()
	d = startDex(t, dir, "")
	d.setModel(t, "tool")
	answer := taskTurn(t, d, parent, `get_task {"id":"`+task.ID+`"}`)
	var saved chat.Task
	if err = json.Unmarshal([]byte(strings.TrimPrefix(answer, "tool: ")), &saved); err != nil || !reflect.DeepEqual(saved.StructuredReport, &report) || !saved.GoalMet {
		t.Fatalf("get_task recovered report: %+v err=%v", saved, err)
	}
	var count int
	if err = conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE id = ?", resultID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("structured results=%d err=%v", count, err)
	}
	t.Logf("artifact: task=%s report=%s result=%s count=%d", task.ID, task.Report, resultID, count)
}

func TestUserInterruptedTaskReturnsOneWarningToItsChat(t *testing.T) {
	// Given an execution created before shutdown, with no terminal native input.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "spawn")
	parent := d.newChat(t, `{"task-result":{"report":"prepare parent"}}`)
	d.stop()
	agents, err := engine.New(engine.Config{DataDir: dir, Home: t.TempDir(), FakeModel: true})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := agents.Create(t.Context(), "default", engine.Options{Model: engine.ModelRef{Provider: "faux", ModelID: "echo"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = agents.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "control.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	q := sqlc.New(conn)
	conv, err := q.EnsureConversation(t.Context(), sqlc.EnsureConversationParams{AgentID: "default", ChannelKey: "task:interrupted", Title: "Interrupted work", ModelProvider: "faux", ModelID: "echo", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(t.Context(), "UPDATE conversations SET execution_id = ? WHERE id = ?", ref.ID, conv.ID); err != nil {
		t.Fatal(err)
	}
	var source string
	if err = conn.QueryRowContext(t.Context(), "SELECT id FROM submissions WHERE conversation_id = ? LIMIT 1", parent).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if _, err = q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{ID: "unfinished-task", ConversationID: conv.ID, Content: "unfinished", Attachments: "null", Origin: "task", Kind: "message", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err = q.CreateTask(t.Context(), sqlc.CreateTaskParams{ID: "unfinished-task", ParentConversationID: parent, ConversationID: conv.ID, SpawnKey: "default/unfinished", SourceRequestID: source, Title: "Interrupted work", ConstraintsJson: "[]", DoneCriteriaJson: "[]", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = q.StartTask(t.Context(), sqlc.StartTaskParams{ID: "unfinished-task", Now: sql.NullInt64{Int64: 1, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	// When startup isolates unfinished work, its warning enters the original Chat.
	d = startDex(t, dir, "")
	task := waitExecution(t, conn, parent, "Interrupted work", "interrupted")
	resultID := "task:" + task.ID + ":result"
	result := waitResultSubmission(t, conn, resultID, "done")
	if !strings.Contains(result.answer, "side effects may exist") || !strings.Contains(result.content, `"goalMet":false`) {
		t.Fatalf("missing warning: %+v", result)
	}
	d.stop()
	// Then a repeated restart cannot add a second warning or resume the task.
	startDex(t, dir, "")
	var count int
	if err = conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE kind = 'task_result' AND conversation_id = ?", parent).Scan(&count); err != nil || count != 1 {
		t.Fatalf("warnings=%d err=%v", count, err)
	}
	waitExecution(t, conn, parent, "Interrupted work", "interrupted")
	t.Logf("artifact: task=%s result=%s warning=%q count=%d", task.ID, resultID, result.answer, count)
}

func TestUserStoppedTaskCannotReturnAfterRestartOrLateCompletion(t *testing.T) {
	// Given native completion whose control settlement is still pending.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "control.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(t.Context(), `CREATE TRIGGER hold_task_settlement BEFORE UPDATE OF status ON tasks WHEN NEW.status IN ('done','failed') BEGIN SELECT RAISE(FAIL, 'late task settlement'); END`); err != nil {
		t.Fatal(err)
	}
	d.setModel(t, "spawn")
	parent := d.newChat(t, taskJSON(t, map[string]string{"title": "Stopped completed task", "goal": "late native answer", "provider": "faux", "modelId": "echo"}))
	task := waitExecution(t, conn, parent, "Stopped completed task", "running")
	var conversation int64
	if err = conn.QueryRowContext(t.Context(), "SELECT conversation_id FROM tasks WHERE id = ?", task.ID).Scan(&conversation); err != nil {
		t.Fatal(err)
	}
	w := d.watch(t, conversation)
	w.until(t, "late native answer", answered("echo: late native answer"))
	w.cancel()
	// When the user stops the parent, the terminal cancellation wins over the native outcome.
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/abort", parent), nil, nil)
	waitExecution(t, conn, parent, task.Title, "cancelled")
	if _, err = conn.ExecContext(t.Context(), "DROP TRIGGER hold_task_settlement"); err != nil {
		t.Fatal(err)
	}
	d.setModel(t, "echo")
	if answer := taskTurn(t, d, parent, "hello after stop"); answer != "echo: hello after stop" {
		t.Fatal(answer)
	}
	d.stop()
	// Then recovery and a later user turn preserve cancellation without any result input.
	d = startDex(t, dir, "")
	if answer := taskTurn(t, d, parent, "hello after restart"); answer != "echo: hello after restart" {
		t.Fatal(answer)
	}
	waitExecution(t, conn, parent, task.Title, "cancelled")
	var count int
	if err = conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE id = ?", "task:"+task.ID+":result").Scan(&count); err != nil || count != 0 {
		t.Fatalf("stopped task revived: results=%d err=%v", count, err)
	}
	t.Logf("artifact: cancelled=%s nativeAnswer=%q results=%d", task.ID, "echo: late native answer", count)
}

func TestUserForegroundCannotReportABackgroundTask(t *testing.T) {
	// Given a normal Chat with the real tool registry.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "tool")
	id := d.newChat(t, `report_task {"outcome":"done","summary":"forged report","cause":"","reproduction":[],"files":[],"openItems":[]}`)
	// When the model tries to report a background task from the foreground.
	d.watch(t, id).until(t, "background report tool refused", func(tr *transcript) bool {
		for _, item := range tr.items {
			if item.Kind == "tool" && item.Name == "report_task" && item.Status == "error" {
				return true
			}
		}
		return false
	})
	// Then no task or report can be created.
	conn := sessionDB(t, d)
	var count int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM tasks").Scan(&count); err != nil || count != 0 {
		t.Fatalf("tasks=%d err=%v", count, err)
	}
}

func TestUserTaskStructuredReportAndContractRoundTrip(t *testing.T) {
	for _, outcome := range []string{"done", "blocked", "failed", "missing"} {
		t.Run(outcome, func(t *testing.T) {
			// Given a contract and a report larger than the parent input limit.
			dir := t.TempDir()
			d := startDex(t, dir, "")
			d.setModel(t, "spawn")
			conn := taskResultDB(t, dir)
			report := engine.TaskReport{Outcome: outcome, Summary: "saved evidence", Cause: "observed cause", Reproduction: []engine.TaskCommand{{Command: "go test ./...", Ran: false}, {Command: "pwd", Ran: true}}, Files: []string{"main.go"}, OpenItems: []string{"review current files"}}
			if outcome == "done" {
				report.Summary = strings.Repeat("x", 4100)
			}
			reportJSON, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			goal, model := "report_task "+string(reportJSON), "tool"
			if outcome == "missing" {
				goal, model = "fallback final answer", "echo"
			}
			args := engine.SpawnTaskArgs{Title: "Structured " + outcome, Goal: goal, Provider: "faux", ModelID: model, Constraints: []string{"Do not change code"}, DoneCriteria: []string{"Explain the cause"}}
			raw, err := json.Marshal(args)
			if err != nil {
				t.Fatal(err)
			}
			parent := d.newChat(t, string(raw))
			task := waitExecution(t, conn, parent, args.Title, "done")
			result := waitResultSubmission(t, conn, "task:"+task.ID+":result", "done")
			// When the main agent reads get_task by ID, it receives the full saved contract and report.
			d.setModel(t, "tool")
			answer := taskTurn(t, d, parent, `get_task {"id":"`+task.ID+`"}`)
			var saved chat.Task
			if err = json.Unmarshal([]byte(strings.TrimPrefix(answer, "tool: ")), &saved); err != nil {
				t.Fatalf("get_task=%s err=%v", answer, err)
			}
			if saved.Goal != goal || !reflect.DeepEqual(saved.Constraints, args.Constraints) || !reflect.DeepEqual(saved.DoneCriteria, args.DoneCriteria) {
				t.Fatalf("contract changed: %+v", saved)
			}
			if outcome == "missing" {
				if saved.HasStructuredReport || saved.GoalMet || saved.StructuredReport != nil || saved.Report != "echo: fallback final answer" {
					t.Fatalf("unreported success claimed: %+v", saved)
				}
			} else {
				if !saved.HasStructuredReport || saved.GoalMet != (outcome == "done") || !reflect.DeepEqual(saved.StructuredReport, &report) || saved.Report != string(reportJSON) {
					t.Fatalf("structured report lost: %+v", saved)
				}
				if outcome == "done" && (!strings.Contains(result.content, "Use get_task for the full report") || len([]rune(saved.Report)) <= 4000) {
					t.Fatalf("report truncation/full read: input=%d full=%d", len(result.content), len(saved.Report))
				}
			}
			// The HTTP task summary exposes the structured outcome so the panel can show blocked work.
			var summary struct {
				ReportOutcome string `json:"reportOutcome"`
			}
			d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/tasks/%s", task.ID), nil, &summary)
			want := outcome
			if outcome == "missing" {
				want = ""
			}
			if summary.ReportOutcome != want {
				t.Fatalf("reportOutcome=%q, want %q", summary.ReportOutcome, want)
			}
			t.Logf("artifact: task=%s outcome=%s structured=%v goalMet=%v full=%d parent=%d", task.ID, outcome, saved.HasStructuredReport, saved.GoalMet, len(saved.Report), len(result.content))
		})
	}
}
