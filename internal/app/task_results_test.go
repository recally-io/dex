package app_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestUserTaskResultWaitsForBusyParent(t *testing.T) {
	// Given independent shell gates for the background stage and its parent.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "spawn")
	parent := d.newChat(t, taskJSON(t, map[string]string{
		"title": "Busy parent", "goal": taskGate(t, dir, "background"), "provider": "faux", "modelId": "bash",
	}))
	waitUserTask(t, d, parent, "Busy parent", "running", 1)
	w := d.watch(t, parent)
	w.until(t, "spawn answer", answeredWith("spawn: "))
	waitTaskFile(t, filepath.Join(dir, "background.pid"))
	setTaskChatModel(t, d, parent, "bash")
	foreground := d.send(t, parent, taskGate(t, dir, "parent"))
	waitTaskFile(t, filepath.Join(dir, "parent.pid"))
	w.until(t, "parent tool running", resultParentToolRunning)
	conn := taskResultDB(t, dir)

	// When the stage completes, its report is durable while the parent stays in its tool.
	releaseResultGate(t, dir, "background")
	task := waitUserTask(t, d, parent, "Busy parent", "done", 1)
	resultID := "task:" + task.Stages[0].ID + ":result"
	result := waitResultSubmission(t, conn, resultID, "pending", 0)
	// The later answer proves control attempted delivery before the parent changes model.
	d.setModel(t, "echo")
	barrier := d.newChat(t, "busy result delivery attempted")
	d.watch(t, barrier).until(t, "busy delivery barrier", answered("echo: busy result delivery attempted"))
	if !strings.Contains(task.Stages[0].Report, "gate released") {
		t.Fatalf("completed report missing: %+v", task)
	}
	w.cancel()
	w = d.watch(t, parent)
	w.until(t, "result waits beside running parent", func(tr *transcript) bool {
		return tr.snapshots > 0 && resultParentToolRunning(tr) && slices.ContainsFunc(tr.items, func(it item) bool {
			return it.ID == resultID && it.Kind == "user" && it.Queued
		})
	})
	if taskPIDGone(t, filepath.Join(dir, "parent.pid")) {
		t.Fatal("parent shell ended before its gate was released")
	}
	if got := resultSubmission(t, conn, foreground); got.status != "pending" {
		t.Fatalf("parent ended while gated: %+v", got)
	}

	// Then releasing the parent processes the saved result without another user input.
	// A result already in the worker inbox would keep bash and could not return this spawn answer.
	setTaskChatModel(t, d, parent, "spawn")
	releaseResultGate(t, dir, "parent")
	w.until(t, "background result answer", answeredWith(`"stageId":"`+task.Stages[0].ID+`"`))
	processed := waitResultSubmission(t, conn, resultID, "done", 0)
	if !strings.HasPrefix(processed.answer, "task-result: ") || !strings.Contains(processed.answer, task.ID) || processed.content != result.content {
		t.Fatalf("result was not processed intact: before=%+v after=%+v", result, processed)
	}
	assertResultGoal(t, d, conn, parent, task, 2)
	t.Logf("artifact: stage=%s report=%q result=%s answer=%q", task.Stages[0].ID, task.Stages[0].Report, resultID, processed.answer)
}

func TestUserTaskResultRecoversFailedParentWithQueuedInput(t *testing.T) {
	// Given a parent in a shell tool and a separate background stage.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "spawn")
	parent := d.newChat(t, taskJSON(t, map[string]string{
		"title": "Failed parent", "goal": taskGate(t, dir, "background"), "provider": "faux", "modelId": "bash",
	}))
	task := waitUserTask(t, d, parent, "Failed parent", "running", 1)
	w := d.watch(t, parent)
	w.until(t, "spawn answer", answeredWith("spawn: "))
	waitTaskFile(t, filepath.Join(dir, "background.pid"))
	setTaskChatModel(t, d, parent, "bash")
	d.send(t, parent, taskGate(t, dir, "parent"))
	waitTaskFile(t, filepath.Join(dir, "parent.pid"))
	setTaskChatModel(t, d, parent, "spawn")
	invalid := d.send(t, parent, "invalid JSON")
	continuation := d.send(t, parent, `{"task-result":{"report":"queued continuation"}}`)
	w.until(t, "both inputs queued behind the shell", func(tr *transcript) bool {
		return resultParentToolRunning(tr) && slices.ContainsFunc(tr.items, func(it item) bool { return it.ID == invalid && it.Queued }) &&
			slices.ContainsFunc(tr.items, func(it item) bool { return it.ID == continuation && it.Queued })
	})
	// A later chat answer is a delivery-loop barrier: earlier queued inputs have been submitted.
	d.setModel(t, "echo")
	barrier := d.newChat(t, "queued inputs admitted")
	d.watch(t, barrier).until(t, "admission barrier", answered("echo: queued inputs admitted"))
	conn := taskResultDB(t, dir)

	// When the shell ends, the next model turn fails on actual invalid JSON.
	releaseResultGate(t, dir, "parent")
	w.until(t, "failed parent with continuation still queued", func(tr *transcript) bool {
		return !tr.running && slices.ContainsFunc(tr.items, func(it item) bool { return it.Kind == "error" }) &&
			slices.ContainsFunc(tr.items, func(it item) bool { return it.ID == continuation && it.Queued })
	})
	failed := waitResultSubmission(t, conn, invalid, "unanswered", 0)
	if failed.reason == "" || failed.reason == "aborted" {
		t.Fatalf("parent did not fail at the model boundary: %+v", failed)
	}
	if got := resultSubmission(t, conn, continuation); got.status != "pending" {
		t.Fatalf("no leftover input after failure: %+v", got)
	}

	// Then the background result wakes the leftover inbox and gets its own assistant answer.
	releaseResultGate(t, dir, "background")
	task = waitUserTask(t, d, parent, "Failed parent", "done", 1)
	resultID := "task:" + task.Stages[0].ID + ":result"
	continued := waitResultSubmission(t, conn, continuation, "done", 0)
	if continued.answer != `task-result: {"report":"queued continuation"}` {
		t.Fatalf("queued input answer: %+v", continued)
	}
	processed := waitResultSubmission(t, conn, resultID, "done", 0)
	w.until(t, "result after failed run", answeredWith(`"stageId":"`+task.Stages[0].ID+`"`))
	if !strings.Contains(processed.answer, task.ID) || !strings.Contains(processed.answer, "gate released") {
		t.Fatalf("lost result after model failure: %+v", processed)
	}
	assertResultGoal(t, d, conn, parent, task, 4)
	t.Logf("artifact: failed=%s reason=%q continuation=%q result=%q", invalid, failed.reason, continued.answer, processed.answer)
}

func TestUserTaskResultSurvivesStopAfterParentAcceptsIt(t *testing.T) {
	// Given a report that starts a real shell gate when faux/bash reads the result envelope.
	// Shell substitution is intentional here: the model boundary executes the report text.
	dir := t.TempDir()
	gate := taskGate(t, dir, "accepted-result")
	script := filepath.Join(dir, "result-gate.sh")
	if err := os.WriteFile(script, []byte("#!/bin/bash\n"+gate+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	report := "$(" + resultShellQuote(script) + ")"
	goal := taskGate(t, dir, "background") + "; printf '%s\\n' " + resultShellQuote(report)
	d := startDex(t, dir, "")
	d.setModel(t, "spawn")
	parent := d.newChat(t, taskJSON(t, map[string]string{
		"title": "Stopped result", "goal": goal, "provider": "faux", "modelId": "bash",
	}))
	waitUserTask(t, d, parent, "Stopped result", "running", 1)
	w := d.watch(t, parent)
	w.until(t, "spawn answer", answeredWith("spawn: "))
	waitTaskFile(t, filepath.Join(dir, "background.pid"))
	setTaskChatModel(t, d, parent, "bash")
	conn := taskResultDB(t, dir)
	releaseResultGate(t, dir, "background")
	task := waitUserTask(t, d, parent, "Stopped result", "done", 1)
	resultID := "task:" + task.Stages[0].ID + ":result"
	waitTaskFile(t, filepath.Join(dir, "accepted-result.pid"))
	w.until(t, "accepted result executing a tool", func(tr *transcript) bool {
		return resultParentToolRunning(tr) && slices.ContainsFunc(tr.items, func(it item) bool {
			return it.ID == resultID && it.Kind == "user" && !it.Queued
		})
	})
	before := waitResultSubmission(t, conn, resultID, "pending", 0)

	// When Stop interrupts the accepted result, its retry remains pending in the same goal.
	d.must(t, http.MethodPost, fmt.Sprintf("/api/chats/%d/abort", parent), nil, nil)
	retained := waitResultSubmission(t, conn, resultID, "pending", 1)
	if retained.content != before.content || retained.answer != "" || retained.reason != "" {
		t.Fatalf("Stop lost or settled the result: before=%+v after=%+v", before, retained)
	}
	// The retry uses the same tool gate. Wait for its actual user entry before stopping again.
	w.until(t, "retried result accepted", func(tr *transcript) bool {
		return resultParentToolRunning(tr) && slices.ContainsFunc(tr.items, func(it item) bool {
			return it.ID == resultID+":attempt:1" && it.Kind == "user" && !it.Queued
		})
	})
	assertResultGoal(t, d, conn, parent, task, 1)

	// Then another Stop with a result-aware model retries the same input to completion.
	setTaskChatModel(t, d, parent, "spawn")
	d.must(t, http.MethodPost, fmt.Sprintf("/api/chats/%d/abort", parent), nil, nil)
	processed := waitResultSubmission(t, conn, resultID, "done", 2)
	w.until(t, "result answer after Stop", answeredWith(`"stageId":"`+task.Stages[0].ID+`"`))
	if processed.content != before.content || !strings.HasPrefix(processed.answer, "task-result: ") || !strings.Contains(processed.answer, task.ID) {
		t.Fatalf("retry did not process the original result: %+v", processed)
	}
	assertResultGoal(t, d, conn, parent, task, 1)
	t.Logf("artifact: stage=%s report=%q result=%s attempts=%d answer=%q", task.Stages[0].ID, task.Stages[0].Report, resultID, processed.attempt, processed.answer)
}

func resultShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func resultParentToolRunning(tr *transcript) bool {
	return tr.running && slices.ContainsFunc(tr.items, func(it item) bool {
		return it.Kind == "tool" && it.Name == "bash" && it.Status == "running"
	})
}

func releaseResultGate(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".gate"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func taskResultDB(t *testing.T, dir string) *sql.DB {
	t.Helper()
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "control.sqlite")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

type resultSubmissionState struct {
	status, content, answer, reason string
	attempt                         int
}

func resultSubmission(t *testing.T, conn *sql.DB, id string) resultSubmissionState {
	t.Helper()
	var row resultSubmissionState
	if err := conn.QueryRowContext(t.Context(), `SELECT status, content, coalesce(answer, ''), coalesce(reason, ''), delivery_attempt FROM submissions WHERE id = ?`, id).
		Scan(&row.status, &row.content, &row.answer, &row.reason, &row.attempt); err != nil {
		t.Fatal(err)
	}
	return row
}

func waitResultSubmission(t *testing.T, conn *sql.DB, id, status string, attempt int) resultSubmissionState {
	t.Helper()
	var row resultSubmissionState
	eventually(t, fmt.Sprintf("submission %s %s attempt %d", id, status, attempt), func() (bool, any) {
		row = resultSubmission(t, conn, id)
		return row.status == status && row.attempt == attempt, row
	})
	return row
}

func assertResultGoal(t *testing.T, d *dex, conn *sql.DB, parent int64, task userTask, userInputs int) {
	t.Helper()
	got := getUserTask(t, d, parent, task.ShortID)
	if got.ID != task.ID || got.Status != "active" || len(got.Stages) != 1 || got.Stages[0].ID != task.Stages[0].ID || got.Stages[0].Report != task.Stages[0].Report {
		t.Fatalf("result changed the goal or lost its report: %+v", got)
	}
	if tasks := d.tasks(t, parent); len(tasks) != 1 {
		t.Fatalf("result created another goal: %+v", tasks)
	}
	var inputs, results int
	if err := conn.QueryRowContext(t.Context(), `SELECT count(*) FILTER (WHERE kind = 'message'), count(*) FILTER (WHERE kind = 'task_result') FROM submissions WHERE conversation_id = ?`, parent).Scan(&inputs, &results); err != nil {
		t.Fatal(err)
	}
	if inputs != userInputs || results != 1 {
		t.Fatalf("unexpected parent inputs: user=%d want=%d results=%d", inputs, userInputs, results)
	}
}
