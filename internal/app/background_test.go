package app_test

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"html"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/background"
)

// The tests in this file run real processes, real SQLite files, and the faux provider. The chat uses
// the model "spawn": its message is the JSON arguments of one tool. A wake-up reaches the same model,
// which answers "background-result: <the wake-up>".

type bgInfo struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	State      string `json:"state"`
	ExitStatus *int   `json:"exitStatus"`
	Reason     string `json:"reason"`
	OutputFile string `json:"outputFile"`
}

// bgResult is the body of one wake-up.
type bgResult struct {
	ID         string   `json:"id"`
	Event      string   `json:"event"`
	ExitStatus *int     `json:"exitStatus"`
	Notice     string   `json:"notice"`
	Lines      []string `json:"lines"`
	Omitted    int      `json:"omitted"`
	Output     string   `json:"output"`
	OutputFile string   `json:"outputFile"`
}

type bgWake struct {
	id, status, answer string
	result             bgResult
}

func bgStart(t *testing.T, d *dex, chat int64, args map[string]any) bgInfo {
	t.Helper()
	var info bgInfo
	toolJSON(t, callSessionTool(t, d, chat, "background_start", args), &info)
	if info.ID == "" || info.State != "running" || info.OutputFile == "" {
		t.Fatalf("background_start returned %+v", info)
	}
	return info
}

func bgList(t *testing.T, d *dex, chat int64) []bgInfo {
	t.Helper()
	var out struct {
		Commands []bgInfo `json:"commands"`
	}
	toolJSON(t, callSessionTool(t, d, chat, "background_list", map[string]any{}), &out)
	return out.Commands
}

func bgWakes(t *testing.T, conn *sql.DB, chat int64) []bgWake {
	t.Helper()
	rows, err := conn.QueryContext(t.Context(), `SELECT id, status, content, coalesce(answer, '') FROM submissions WHERE conversation_id = ? AND id LIKE 'background:%' ORDER BY rowid`, chat)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var wakes []bgWake
	for rows.Next() {
		var w bgWake
		var content string
		if err := rows.Scan(&w.id, &w.status, &content, &w.answer); err != nil {
			t.Fatal(err)
		}
		var body struct {
			Result bgResult `json:"background-result"`
		}
		if err := json.Unmarshal([]byte(content), &body); err != nil {
			t.Fatalf("wake-up %q: %v", content, err)
		}
		w.result = body.Result
		wakes = append(wakes, w)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return wakes
}

// waitWakes waits until the chat has n wake-ups and the session answered all of them.
func waitWakes(t *testing.T, conn *sql.DB, chat int64, n int) []bgWake {
	t.Helper()
	var wakes []bgWake
	eventually(t, fmt.Sprintf("%d answered wake-ups", n), func() (bool, any) {
		wakes = bgWakes(t, conn, chat)
		done := len(wakes) >= n
		for _, w := range wakes {
			done = done && w.status == "done"
		}
		return done, wakes
	})
	if len(wakes) != n {
		t.Fatalf("want %d wake-ups, got %+v", n, wakes)
	}
	return wakes
}

func bgRow(t *testing.T, conn *sql.DB, id string) (state, reason string) {
	t.Helper()
	if err := conn.QueryRowContext(t.Context(), `SELECT state, reason FROM background_commands WHERE id = ?`, id).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	return state, reason
}

func waitBgState(t *testing.T, conn *sql.DB, id, want string) {
	t.Helper()
	eventually(t, "background state "+want, func() (bool, any) {
		state, reason := bgRow(t, conn, id)
		return state == want, state + " " + reason
	})
}

func pidFile(t *testing.T, path string) int {
	t.Helper()
	pid, err := strconv.Atoi(strings.TrimSpace(waitTaskFile(t, path)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// killAtEnd stops a process that a test leaves running when it fails.
func killAtEnd(t *testing.T, pid int) {
	t.Helper()
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
}

func bgChat(t *testing.T, d *dex) int64 {
	t.Helper()
	return newParent(t, d)
}

func TestUserGetsAWakeUpWithTheExitStatusWhenABackgroundCommandEnds(t *testing.T) {
	t.Parallel()
	// Given a 10-minute build that the bot starts in the background.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	chat := bgChat(t, d)
	script := filepath.Join(dir, "build.sh")
	if err := os.WriteFile(script, []byte("echo compiling\nwhile ! test -f "+filepath.Join(dir, "build.done")+"; do sleep 0.05; done\necho 'build failed: 2 errors' >&2\nexit 3\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	// When the bot starts it.
	info := bgStart(t, d, chat, map[string]any{"command": script, "label": "build"})
	// Then the start call returned while the build runs, and no wake-up exists yet.
	if got := bgList(t, d, chat); len(got) != 1 || got[0].ID != info.ID || got[0].State != "running" || got[0].Label != "build" {
		t.Fatalf("background_list while the build runs: %+v", got)
	}
	if wakes := bgWakes(t, conn, chat); len(wakes) != 0 {
		t.Fatalf("wake-up before the exit: %+v", wakes)
	}
	// When the build ends with status 3.
	if err := os.WriteFile(filepath.Join(dir, "build.done"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Then the session gets one wake-up with the status and the last output, and answers it without a user message.
	wake := waitWakes(t, conn, chat, 1)[0]
	if wake.result.ID != info.ID || wake.result.Event != "exited" || wake.result.ExitStatus == nil || *wake.result.ExitStatus != 3 {
		t.Fatalf("wake-up: %+v", wake.result)
	}
	if !strings.Contains(wake.result.Output, "compiling") || !strings.Contains(wake.result.Output, "build failed: 2 errors") {
		t.Fatalf("the wake-up lacks the output: %q", wake.result.Output)
	}
	if !strings.HasPrefix(html.UnescapeString(wake.answer), "background-result:") {
		t.Fatalf("the session did not answer the wake-up: %q", wake.answer)
	}
	// And the transcript marks the wake-up with the label, and links no child session.
	d.watch(t, chat).until(t, "the wake-up in the transcript", func(tr *transcript) bool {
		return slices.ContainsFunc(tr.items, func(i item) bool { return i.Kind == "task" && i.Title == "build" && i.TaskID == "" })
	})
	// And the output file has the whole output, and the list shows the end.
	data, err := os.ReadFile(info.OutputFile)
	if err != nil || string(data) != "compiling\nbuild failed: 2 errors\n" {
		t.Fatalf("output file %q: %v", data, err)
	}
	if got := bgList(t, d, chat); got[0].State != "exited" || got[0].ExitStatus == nil || *got[0].ExitStatus != 3 {
		t.Fatalf("background_list after the exit: %+v", got)
	}
}

func TestUserGetsAWakeUpWhenAWaitConditionChecksForAFailure(t *testing.T) {
	t.Parallel()
	// Given a bot that waits for the ready line of a dev server, and a loop that also checks for a dead server.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	chat := bgChat(t, d)
	log, died := filepath.Join(dir, "dev.log"), filepath.Join(dir, "died")
	command := fmt.Sprintf("until grep -q 'Ready on' %[1]s || test -f %[2]s; do sleep 0.05; done; grep -q 'Ready on' %[1]s", log, died)
	if err := os.WriteFile(log, []byte("compiling\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bgStart(t, d, chat, map[string]any{"command": command})
	// When the server exits before it prints the ready line.
	if err := os.WriteFile(died, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Then the bot gets a wake-up with status 1, and not silence.
	wake := waitWakes(t, conn, chat, 1)[0]
	if wake.result.Event != "exited" || wake.result.ExitStatus == nil || *wake.result.ExitStatus != 1 {
		t.Fatalf("wake-up: %+v", wake.result)
	}
}

func TestUserGetsBatchedWakeUpsForTheLinesOfAWatch(t *testing.T) {
	t.Parallel()
	// Given a CI watch with wake lines that prints each check result and exits when the run completes.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	chat := bgChat(t, d)
	gate := filepath.Join(dir, "second.gate")
	command := fmt.Sprintf("echo 'lint: pass'; echo 'test: pass'; echo 'ignored' >&2; until test -f %s; do sleep 0.05; done; echo 'deploy: fail'", gate)
	info := bgStart(t, d, chat, map[string]any{"command": command, "wake": "lines", "label": "ci"})
	// Then the first two lines arrive as one wake-up, and the line of stderr is not an event.
	first := waitWakes(t, conn, chat, 1)[0]
	if first.result.Event != "lines" || strings.Join(first.result.Lines, "|") != "lint: pass|test: pass" {
		t.Fatalf("first wake-up: %+v", first.result)
	}
	// When the run completes after the first batch.
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Then the last line arrives, and then the end of the watch.
	wakes := waitWakes(t, conn, chat, 3)
	if wakes[1].result.Event != "lines" || strings.Join(wakes[1].result.Lines, "|") != "deploy: fail" {
		t.Fatalf("second wake-up: %+v", wakes[1].result)
	}
	if end := wakes[2].result; end.Event != "exited" || end.ExitStatus == nil || *end.ExitStatus != 0 || end.ID != info.ID {
		t.Fatalf("end of the watch: %+v", end)
	}
	// And the output file has the lines of both streams.
	data, err := os.ReadFile(info.OutputFile)
	if err != nil || !strings.Contains(string(data), "ignored") || !strings.Contains(string(data), "deploy: fail") {
		t.Fatalf("output file %q: %v", data, err)
	}
}

func TestUserGetsTheLinesOfAWatchWhileTheDatabaseIsBusy(t *testing.T) {
	t.Parallel()
	// Given a watch that prints a line when a gate opens.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	chat := bgChat(t, d)
	gate, last := filepath.Join(dir, "line.gate"), filepath.Join(dir, "end.gate")
	command := fmt.Sprintf("until test -f %s; do sleep 0.05; done; echo 'lint: pass'; until test -f %s; do sleep 0.05; done", gate, last)
	bgStart(t, d, chat, map[string]any{"command": command, "wake": "lines"})
	// And another writer that holds the database longer than the busy timeout of 5 seconds.
	writer, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "control.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	lock, err := writer.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	// When the line arrives while the database is busy, and the writer lets go after 12 seconds.
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(12 * time.Second)
	if _, err := lock.ExecContext(t.Context(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	_ = lock.Close()
	// Then the session still gets the line.
	first := waitWakes(t, conn, chat, 1)[0]
	if first.result.Event != "lines" || strings.Join(first.result.Lines, "|") != "lint: pass" {
		t.Fatalf("wake-up: %+v", first.result)
	}
	if err := os.WriteFile(last, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitWakes(t, conn, chat, 2)
}

func TestUserGetsDefaultAndExplicitBackgroundTimeouts(t *testing.T) {
	t.Parallel()
	// Given both wake modes and omitted, null, and explicit timeouts.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	chat := bgChat(t, d)
	cases := []struct {
		name string
		args map[string]any
		want int64
	}{
		{"default wake and timeout", map[string]any{}, 1800},
		{"exit omitted", map[string]any{"wake": "exit"}, 1800},
		{"exit null", map[string]any{"wake": "exit", "timeout": nil}, 1800},
		{"exit explicit above lines limit", map[string]any{"wake": "exit", "timeout": 1860}, 1860},
		{"lines omitted", map[string]any{"wake": "lines"}, 300},
		{"lines null", map[string]any{"wake": "lines", "timeout": nil}, 300},
		{"lines explicit", map[string]any{"wake": "lines", "timeout": 1800}, 1800},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// When the bot starts a command that waits.
			tc.args["command"] = "sleep 300"
			info := bgStart(t, d, chat, tc.args)
			// Then the durable row has the applicable timeout, and stop sends no wake-up.
			var timeout sql.NullInt64
			if err := conn.QueryRowContext(t.Context(), `SELECT timeout_ms FROM background_commands WHERE id = ?`, info.ID).Scan(&timeout); err != nil {
				t.Fatal(err)
			}
			if !timeout.Valid || timeout.Int64 != tc.want*1000 {
				t.Fatalf("timeout = %+v, want %d seconds", timeout, tc.want)
			}
			callSessionTool(t, d, chat, "background_stop", map[string]any{"id": info.ID})
			if wakes := bgWakes(t, conn, chat); len(wakes) != 0 {
				t.Fatalf("stopped command sent wake-ups: %+v", wakes)
			}
		})
	}
}

func TestUserGetsAWakeUpWhenABackgroundCommandPassesItsTimeout(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"exit", "lines"} {
		t.Run(mode, func(t *testing.T) {
			// Given a command with a one-second timeout and a child process.
			dir := t.TempDir()
			d := startDex(t, dir, "")
			conn := taskResultDB(t, dir)
			chat := bgChat(t, d)
			leader, child := filepath.Join(dir, "leader.pid"), filepath.Join(dir, "child.pid")
			command := fmt.Sprintf("echo $$ > %s; sleep 300 & echo $! > %s; wait", leader, child)
			info := bgStart(t, d, chat, map[string]any{"command": command, "wake": mode, "timeout": 1})
			killAtEnd(t, pidFile(t, leader))
			killAtEnd(t, pidFile(t, child))
			// Then dex ends the group and stores one timeout wake-up with the final state.
			wake := waitWakes(t, conn, chat, 1)[0]
			if wake.result.Event != "timed_out" || !strings.Contains(wake.result.Notice, "timeout of 1 seconds") {
				t.Fatalf("wake-up: %+v", wake.result)
			}
			if state, reason := bgRow(t, conn, info.ID); state != "timed_out" || reason != "timeout" {
				t.Fatalf("state %s, reason %s", state, reason)
			}
			waitTaskPIDGone(t, leader)
			waitTaskPIDGone(t, child)
		})
	}
}

func TestUserRejectsAWatchLongerThanThirtyMinutes(t *testing.T) {
	t.Parallel()
	// Given a bot that asks for a lines watch of 31 minutes.
	d := startDex(t, t.TempDir(), "")
	chat := bgChat(t, d)
	// When it starts the watch.
	answer := callSessionTool(t, d, chat, "background_start", map[string]any{"command": "true", "wake": "lines", "timeout": 1860})
	// Then the call fails and says why.
	if !strings.HasPrefix(answer, "spawn error:") || !strings.Contains(answer, "1800 seconds") {
		t.Fatalf("answer: %s", answer)
	}
}

func TestUserStopsABackgroundCommandWithItsProcessGroup(t *testing.T) {
	t.Parallel()
	// Given a background command that has a child process.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	chat := bgChat(t, d)
	leader, child := filepath.Join(dir, "leader.pid"), filepath.Join(dir, "child.pid")
	info := bgStart(t, d, chat, map[string]any{"command": fmt.Sprintf("echo $$ > %s; sleep 300 & echo $! > %s; wait", leader, child)})
	killAtEnd(t, pidFile(t, leader))
	childPID := pidFile(t, child)
	killAtEnd(t, childPID)
	// When the bot stops it.
	var stopped bgInfo
	toolJSON(t, callSessionTool(t, d, chat, "background_stop", map[string]any{"id": info.ID}), &stopped)
	// Then the state is stopped, the whole group is gone, and no wake-up exists.
	if stopped.State != "stopped" || stopped.Reason != "stop" {
		t.Fatalf("background_stop returned %+v", stopped)
	}
	waitTaskPIDGone(t, leader)
	waitTaskPIDGone(t, child)
	if wakes := bgWakes(t, conn, chat); len(wakes) != 0 {
		t.Fatalf("a stopped command sent a wake-up: %+v", wakes)
	}
}

func TestUserStopsTheSessionAndItsBackgroundCommandsEnd(t *testing.T) {
	t.Parallel()
	// Given a session with a running background command and a child process of it.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	chat := bgChat(t, d)
	leader, child := filepath.Join(dir, "leader.pid"), filepath.Join(dir, "child.pid")
	info := bgStart(t, d, chat, map[string]any{"command": fmt.Sprintf("echo $$ > %s; sleep 300 & echo $! > %s; wait", leader, child), "wake": "lines"})
	killAtEnd(t, pidFile(t, leader))
	killAtEnd(t, pidFile(t, child))
	// When the user stops the session.
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/abort", chat), nil, nil)
	// Then dex ends the process group, and the session gets no wake-up.
	waitBgState(t, conn, info.ID, "stopped")
	waitTaskPIDGone(t, leader)
	waitTaskPIDGone(t, child)
	if wakes := bgWakes(t, conn, chat); len(wakes) != 0 {
		t.Fatalf("a stopped session got a wake-up: %+v", wakes)
	}
}

func TestUserSeesAnUnknownStateAfterDexCrashes(t *testing.T) {
	t.Parallel()
	// Given a background command that a crash of dex left in state running, and its process that still runs.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	chat := bgChat(t, d)
	info := bgStart(t, d, chat, map[string]any{"command": taskGate(t, dir, "crash")})
	d.stop()
	process := exec.CommandContext(context.WithoutCancel(t.Context()), "sleep", "300") // killed by the cleanup below
	process.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-process.Process.Pid, syscall.SIGKILL); _ = process.Wait() })
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "control.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.ExecContext(t.Context(), `UPDATE background_commands SET state = 'running', reason = '', exit_status = NULL, ended_at = NULL, pid = ?, pgid = ? WHERE id = ?`, process.Process.Pid, process.Process.Pid, info.ID); err != nil {
		t.Fatal(err)
	}
	// When dex starts again with the same data.
	d = startDex(t, dir, "")
	// Then the list says unknown, and dex does not claim that the process stopped.
	list := bgList(t, d, chat)
	if len(list) != 1 || list[0].ID != info.ID || list[0].State != "unknown" || list[0].ExitStatus != nil {
		t.Fatalf("background_list after the crash: %+v", list)
	}
	// And dex does not signal a process that it cannot identify.
	answer := callSessionTool(t, d, chat, "background_stop", map[string]any{"id": info.ID})
	if !strings.HasPrefix(answer, "spawn error:") || !strings.Contains(answer, "crashed") {
		t.Fatalf("background_stop on an unknown command: %s", answer)
	}
	if err := syscall.Kill(process.Process.Pid, 0); err != nil {
		t.Fatalf("dex signalled an unknown process: %v", err)
	}
}

func TestUserGetsAStoppedStateAndAWakeUpWhenDexShutsDown(t *testing.T) {
	t.Parallel()
	// Given a background command with a child process, when dex shuts down.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	chat := bgChat(t, d)
	leader, child := filepath.Join(dir, "leader.pid"), filepath.Join(dir, "child.pid")
	info := bgStart(t, d, chat, map[string]any{"command": fmt.Sprintf("echo $$ > %s; sleep 300 & echo $! > %s; wait", leader, child)})
	killAtEnd(t, pidFile(t, leader))
	killAtEnd(t, pidFile(t, child))
	d.stop()
	// Then the process group is gone, and the row says that dex stopped it.
	waitTaskPIDGone(t, leader)
	waitTaskPIDGone(t, child)
	if state, reason := bgRow(t, conn, info.ID); state != "stopped" || reason != "shutdown" {
		t.Fatalf("state %s, reason %s", state, reason)
	}
	// And the next start gives the agent the wake-up that tells it to start the command again.
	d = startDex(t, dir, "")
	wake := waitWakes(t, conn, chat, 1)[0]
	if wake.result.Event != "stopped" || !strings.Contains(wake.result.Notice, "shut down") {
		t.Fatalf("wake-up: %+v", wake.result)
	}
	if list := bgList(t, d, chat); len(list) != 1 || list[0].State != "stopped" || list[0].Reason != "shutdown" {
		t.Fatalf("background_list after the restart: %+v", list)
	}
}

func TestUserDoesNotSeeAWithdrawnWakeUpAsTheirOwnMessage(t *testing.T) {
	t.Parallel()
	// Given a wake-up that waits behind a run of the session.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	chat := bgChat(t, d)
	done := filepath.Join(dir, "ci.done")
	bgStart(t, d, chat, map[string]any{"command": "until test -f " + done + "; do sleep 0.02; done", "label": "ci"})
	d.setModel(t, "bash")
	d.send(t, chat, taskGate(t, dir, "busy"))
	waitTaskFile(t, filepath.Join(dir, "busy.pid"))
	if err := os.WriteFile(done, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the wake-up is queued behind the run", func() (bool, any) {
		var admitted sql.NullInt64
		err := conn.QueryRowContext(t.Context(), `SELECT admitted_at FROM submissions WHERE conversation_id = ? AND id LIKE 'background:%'`, chat).Scan(&admitted)
		return err == nil && admitted.Valid, err
	})
	// When the user stops the session, which withdraws the queued wake-up.
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/abort", chat), nil, nil)
	waitResultSubmission(t, conn, "background:"+bgWakes(t, conn, chat)[0].result.ID+":end", "unanswered")
	// Then the live stream and the history show a background result, and no message of the user.
	check := func(source string, items []item) {
		t.Helper()
		var marker bool
		for _, it := range items {
			if it.Kind == "user" && strings.Contains(it.Text, "background-result") {
				t.Fatalf("%s shows the wake-up as a message of the user: %+v", source, it)
			}
			marker = marker || it.Kind == "task" && it.Title == "ci" && it.TaskID == ""
		}
		if !marker {
			t.Fatalf("%s has no background result marker: %+v", source, items)
		}
	}
	w := d.watch(t, chat)
	w.until(t, "the withdrawn wake-up in the stream", func(tr *transcript) bool { return !tr.running && len(tr.items) > 0 })
	check("the live stream", w.tr.items)
	var history struct {
		Items []item `json:"items"`
	}
	d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d/history?before=9007199254740991&limit=100", chat), nil, &history)
	check("the history", history.Items)
}

func TestUserParentReadsTheCutAnswerOfAChildWakeUp(t *testing.T) {
	t.Parallel()
	// Given a child session that started a background command whose output makes the answer to its wake-up long.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := newParent(t, d)
	done := filepath.Join(dir, "child.done")
	command := fmt.Sprintf("until test -f %s; do sleep 0.02; done; for i in $(seq 1 20); do head -c 300 /dev/zero | tr '\\0' z; echo; done", done)
	message := childJSON(t, map[string]any{"tool": "background_start", "command": command})
	child := spawnChild(t, d, parent, "Builder", message, "spawn")
	waitResults(t, conn, parent, 1)
	if err := os.WriteFile(done, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// When the wake-up turn of the child ends, the parent gets a result with a cut answer.
	var turn string
	eventually(t, "a cut result of the wake-up", func() (bool, any) {
		rows := resultRows(t, conn, parent)
		for _, row := range rows {
			if strings.Contains(row.content, "background:") && strings.Contains(row.content, "The answer is cut") {
				var body struct {
					Result struct {
						Turn string `json:"turn"`
					} `json:"task-result"`
				}
				if err := json.Unmarshal([]byte(row.content), &body); err == nil {
					turn = body.Result.Turn
				}
				return turn != "", rows
			}
		}
		return false, rows
	})
	// Then the parent reads the rest of that turn with session_read.
	answer := callSessionTool(t, d, parent, "session_read", map[string]any{"id": child.ID, "turn": turn, "offset": 4000})
	var page struct {
		Text string `json:"text"`
	}
	toolJSON(t, answer, &page)
	if page.Text == "" {
		t.Fatalf("session_read returned no text: %s", answer)
	}
}

func TestUserHasAWatchStoppedWhenItPrintsTooManyLines(t *testing.T) {
	t.Parallel()
	// Given a watch that prints 400 lines at once and then waits.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	chat := bgChat(t, d)
	pids := filepath.Join(dir, "flood.pid")
	info := bgStart(t, d, chat, map[string]any{"command": "echo $$ > " + pids + "; seq 1 400; sleep 300 & wait", "wake": "lines"})
	killAtEnd(t, pidFile(t, pids))
	// Then dex stops it, and the session gets one batch and a notice, not 400 events.
	var end bgWake
	eventually(t, "the end of the flood", func() (bool, any) {
		wakes := bgWakes(t, conn, chat)
		if len(wakes) == 0 {
			return false, wakes
		}
		end = wakes[len(wakes)-1]
		return end.result.Event == "stopped" && end.status == "done", wakes
	})
	if !strings.Contains(end.result.Notice, "more than 100 lines") {
		t.Fatalf("notice: %+v", end.result)
	}
	wakes := bgWakes(t, conn, chat)
	total := 0
	for _, w := range wakes[:len(wakes)-1] {
		total += len(w.result.Lines) + w.result.Omitted
	}
	if total != 100 {
		t.Fatalf("the session got %d events, want 100: %+v", total, wakes)
	}
	if state, reason := bgRow(t, conn, info.ID); state != "stopped" || reason != "rate_limit" {
		t.Fatalf("state %s, reason %s", state, reason)
	}
	waitTaskPIDGone(t, pids)
}

func TestUserHasABackgroundCommandStoppedWhenItsOutputIsTooLarge(t *testing.T) {
	t.Parallel()
	// Given a limit of 20000 bytes of output and a command that prints without end.
	dir := t.TempDir()
	d := startDexWith(t, app.Config{DataDir: dir, Background: background.Limits{OutputBytes: 20_000}})
	conn := taskResultDB(t, dir)
	chat := bgChat(t, d)
	info := bgStart(t, d, chat, map[string]any{"command": "yes 'a line of output'"})
	// Then dex stops it and tells the session why.
	wake := waitWakes(t, conn, chat, 1)[0]
	if wake.result.Event != "stopped" || !strings.Contains(wake.result.Notice, "output passed 20000 bytes") {
		t.Fatalf("wake-up: %+v", wake.result)
	}
	if state, reason := bgRow(t, conn, info.ID); state != "stopped" || reason != "output_limit" {
		t.Fatalf("state %s, reason %s", state, reason)
	}
}

func TestUserCannotRunMoreBackgroundCommandsThanTheBotCap(t *testing.T) {
	t.Parallel()
	// Given a cap of one running command.
	dir := t.TempDir()
	d := startDexWith(t, app.Config{DataDir: dir, Background: background.Limits{MaxRunning: 1}})
	chat := bgChat(t, d)
	first := bgStart(t, d, chat, map[string]any{"command": taskGate(t, dir, "cap")})
	// When the bot starts a second one.
	answer := callSessionTool(t, d, chat, "background_start", map[string]any{"command": "true"})
	// Then the call fails, and a stop makes room again.
	if !strings.HasPrefix(answer, "spawn error:") || !strings.Contains(answer, "too many background commands") {
		t.Fatalf("answer: %s", answer)
	}
	toolJSON(t, callSessionTool(t, d, chat, "background_stop", map[string]any{"id": first.ID}), &bgInfo{})
	bgStart(t, d, chat, map[string]any{"command": "true"})
}
