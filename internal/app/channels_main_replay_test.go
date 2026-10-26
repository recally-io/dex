package app_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"uuid"
)

func TestUserChannelMainLegacyNewReplayPreservesContext(t *testing.T) {
	t.Parallel()
	// Given main context and an incomplete /new from the previous version.
	f := newFakeTelegram(t)
	dir := t.TempDir()
	d := startTelegram(t, f, dir, 42)
	conn := sessionDB(t, d)
	main := d.mainChat(t)
	d.setModel(t, "context")
	seed := d.send(t, main, "web context before upgrade")
	waitResultSubmission(t, conn, seed, "done")
	sessionFixture(t, d, "telegram:7", 1)
	d.stop()
	// The previous version committed the receipt before completion.
	if _, err := conn.ExecContext(t.Context(), "INSERT INTO channel_commands (agent_id,source_key,peer,channel_key,command) VALUES ('default','telegram:123:1','telegram:7','telegram:7','/new')"); err != nil {
		t.Fatal(err)
	}

	// When dex restarts and replays the old command.
	d = startDex(t, dir, f.srv.URL)
	waitChannelCommandCompleted(t, conn)
	probe := d.send(t, main, "probe")
	result := waitResultSubmission(t, conn, probe, "done")

	// Then main keeps its context and receives no reset.
	if result.answer != `context: ["web context before upgrade","probe"]` {
		t.Fatalf("legacy /new cleared main context: %s", result.answer)
	}
	var resets int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE conversation_id=? AND kind='context_reset'", main).Scan(&resets); err != nil {
		t.Fatal(err)
	}
	if resets != 0 {
		t.Fatalf("legacy /new created %d main resets", resets)
	}
	t.Logf("artifact: legacy /new answer=%s; main resets=%d", result.answer, resets)
}

func TestUserChannelMainLegacyStopReplayPreservesWork(t *testing.T) {
	t.Parallel()
	// Given main work and an old /stop before the channel starts.
	f := newFakeTelegram(t)
	dir := t.TempDir()
	d := startDex(t, dir, f.srv.URL)
	conn := sessionDB(t, d)
	main := d.mainChat(t)
	d.setModel(t, "bash")
	input := d.send(t, main, taskGate(t, dir, "legacy-stop"))
	waitTaskFile(t, filepath.Join(dir, "legacy-stop.pid"))
	if _, err := conn.ExecContext(t.Context(), "INSERT INTO channel_commands (agent_id,source_key,peer,channel_key,command) VALUES ('default','telegram:123:1','telegram:7','telegram:7','/stop')"); err != nil {
		t.Fatal(err)
	}

	// When the channel replays its stored command.
	d.must(t, http.MethodPut, "/api/agents/default/channels/telegram", map[string]any{"token": fakeToken, "allowedUsers": []int64{42}}, nil)
	waitChannelCommandCompleted(t, conn)

	// Then main work can still finish with its answer.
	if got := resultSubmission(t, conn, input); got.status != "pending" {
		t.Fatalf("legacy /stop withdrew main input: %+v", got)
	}
	releaseResultGate(t, dir, "legacy-stop")
	result := waitResultSubmission(t, conn, input, "done")
	if result.answer != "bash: gate released" {
		t.Fatalf("legacy /stop changed main answer: %+v", result)
	}
	t.Logf("artifact: legacy /stop preserved input=%s answer=%q", input, result.answer)
}

func TestUserChannelMainStopReplayPreservesLaterWebInput(t *testing.T) {
	t.Parallel()
	// Given a committed withdrawal whose command completion fails.
	f := newFakeTelegram(t)
	dir := t.TempDir()
	d := startTelegram(t, f, dir, 42)
	conn := sessionDB(t, d)
	main := d.mainChat(t)
	d.setModel(t, "bash")
	first := d.send(t, main, taskGate(t, dir, "first-stop"))
	waitTaskFile(t, filepath.Join(dir, "first-stop.pid"))
	failChannelCommandCompletion(t, conn)
	f.message(1, 42, 7, "/stop")
	waitResultSubmission(t, conn, first, "unanswered")
	waitChannelCommandFailure(t, d)
	d.must(t, http.MethodDelete, "/api/agents/default/channels/telegram", nil, nil)
	waitTaskPIDGone(t, filepath.Join(dir, "first-stop.pid"))
	// Maintenance rejects web input without saving a receipt.
	eventually(t, "maintenance after Stop", func() (bool, any) {
		status, body := d.call(t, http.MethodPost, "/api/maintenance", nil)
		return status == http.StatusOK, body
	})
	later := uuid.NewV7().String()
	path := fmt.Sprintf("/api/agents/default/chats/%d/messages", main)
	input := map[string]string{"id": later, "text": taskGate(t, dir, "after-first-stop")}
	d.must(t, http.MethodPost, path, input, nil, http.StatusConflict)
	var count int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE id=?", later).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("maintenance rejection saved a web receipt")
	}
	d.stop()
	var completed int
	if err := conn.QueryRowContext(t.Context(), "SELECT completed FROM channel_commands WHERE source_key='telegram:123:1'").Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != 0 {
		t.Fatal("the failure did not leave an incomplete receipt")
	}
	if _, err := conn.ExecContext(t.Context(), "DROP TRIGGER fail_command_completion"); err != nil {
		t.Fatal(err)
	}

	// When dex restarts and replays the incomplete Stop.
	d = startDex(t, dir, f.srv.URL)
	d.must(t, http.MethodPost, path, input, nil, http.StatusAccepted)
	waitTaskFile(t, filepath.Join(dir, "after-first-stop.pid"))
	d.must(t, http.MethodPut, "/api/agents/default/channels/telegram", map[string]any{"token": fakeToken, "allowedUsers": []int64{42}}, nil)
	waitTaskChannelText(t, f, "Stop requested.")
	if got := resultSubmission(t, conn, later); got.status != "pending" {
		t.Fatalf("replayed Stop withdrew later web input: %+v", got)
	}

	// Then the new input stays admitted and gets its answer.
	releaseResultGate(t, dir, "after-first-stop")
	result := waitResultSubmission(t, conn, later, "done")
	d.watch(t, main).until(t, "web answer after Stop replay", answered("bash: gate released"))
	if result.answer != "bash: gate released" {
		t.Fatalf("later web answer changed: %+v", result)
	}
	t.Logf("artifact: first=%s later=%s later status=%s answer=%q", first, later, result.status, result.answer)
}

func TestUserChannelMainStopReplayPreservesLaterWork(t *testing.T) {
	t.Parallel()
	// Given a Stop whose withdrawal commits but whose completion fails.
	f := newFakeTelegram(t)
	dir := t.TempDir()
	d := startTelegram(t, f, dir, 42)
	conn := sessionDB(t, d)
	main := d.mainChat(t)
	d.setModel(t, "bash")
	first := d.send(t, main, taskGate(t, dir, "first-stop"))
	waitTaskFile(t, filepath.Join(dir, "first-stop.pid"))
	failChannelCommandCompletion(t, conn)
	f.message(1, 42, 7, "/stop")
	waitResultSubmission(t, conn, first, "unanswered")
	waitChannelCommandFailure(t, d)
	d.must(t, http.MethodDelete, "/api/agents/default/channels/telegram", nil, nil)
	waitTaskPIDGone(t, filepath.Join(dir, "first-stop.pid"))
	spawnExecution(t, d, main, "Later task", taskGate(t, dir, "later-task"), "bash")
	waitTaskFile(t, filepath.Join(dir, "later-task.pid"))
	d.setModel(t, "bash")
	later := d.send(t, main, taskGate(t, dir, "later-turn"))
	waitTaskFile(t, filepath.Join(dir, "later-turn.pid"))

	// When command delivery retries while the new turn and task run.
	if _, err := conn.ExecContext(t.Context(), "DROP TRIGGER fail_command_completion"); err != nil {
		t.Fatal(err)
	}
	d.must(t, http.MethodPut, "/api/agents/default/channels/telegram", map[string]any{"token": fakeToken, "allowedUsers": []int64{42}}, nil)
	waitTaskChannelText(t, f, "Stop requested.")
	if got := resultSubmission(t, conn, later); got.status != "pending" {
		t.Fatalf("replayed Stop withdrew later running input: %+v", got)
	}
	var status string
	if err := conn.QueryRowContext(t.Context(), "SELECT status FROM tasks WHERE title='Later task'").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "running" {
		t.Fatalf("replayed Stop cancelled later task: %s", status)
	}

	// Then both new processes remain alive and finish through the native engine.
	for _, name := range []string{"later-turn", "later-task"} {
		if taskPIDGone(t, filepath.Join(dir, name+".pid")) {
			t.Fatalf("replayed Stop aborted %s", name)
		}
	}
	releaseResultGate(t, dir, "later-turn")
	waitResultSubmission(t, conn, later, "done")
	d.setModel(t, "spawn")
	releaseResultGate(t, dir, "later-task")
	task := waitExecution(t, conn, main, "Later task", "done")
	result := waitResultSubmission(t, conn, "task:"+task.ID+":result", "done")
	if !strings.Contains(result.answer, "gate released") {
		t.Fatalf("later task answer missing: %+v", result)
	}
	t.Logf("artifact: later input=%s; task=%s status=%s report=%q", later, task.ID, task.Status, task.Report)
}

func failChannelCommandCompletion(t *testing.T, conn *sql.DB) {
	t.Helper()
	if _, err := conn.ExecContext(t.Context(), "CREATE TRIGGER fail_command_completion BEFORE UPDATE OF completed ON channel_commands BEGIN SELECT RAISE(ABORT,'command completion failure'); END"); err != nil {
		t.Fatal(err)
	}
}

func waitChannelCommandFailure(t *testing.T, d *dex) {
	t.Helper()
	eventually(t, "command completion failure", func() (bool, any) {
		_, body := d.call(t, http.MethodGet, "/api/agents/default/channels", nil)
		return strings.Contains(body, "command completion failure"), "waiting for the database error"
	})
}

func waitChannelCommandCompleted(t *testing.T, conn *sql.DB) {
	t.Helper()
	eventually(t, "command completion", func() (bool, any) {
		var completed int
		err := conn.QueryRowContext(t.Context(), "SELECT completed FROM channel_commands WHERE source_key='telegram:123:1'").Scan(&completed)
		return err == nil && completed == 1, completed
	})
}
