package app_test

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/background"
)

// The tests in this file run real processes, real SQLite files, and the faux provider. The chat uses
// the model "spawn": its message is the JSON arguments of one tool. A wake-up reaches the same model,
// which answers "background-result: <the wake-up>".

type bgInfo struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Mode       string `json:"mode"`
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
	toolJSON(t, callSessionTool(t, d, chat, "process_start", args), &info)
	if !strings.HasPrefix(info.ID, "command-") || info.State != "running" || info.OutputFile == "" {
		t.Fatalf("process_start returned %+v", info)
	}
	return info
}

func bgList(t *testing.T, d *dex, chat int64) []bgInfo {
	t.Helper()
	var out struct {
		Processes []bgInfo `json:"processes"`
	}
	toolJSON(t, callSessionTool(t, d, chat, "process_list", map[string]any{}), &out)
	return out.Processes
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
	if err := conn.QueryRowContext(t.Context(), `SELECT state, reason FROM background_commands WHERE id = ?`, strings.TrimPrefix(id, "command-")).Scan(&state, &reason); err != nil {
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
	if err := os.WriteFile(script, []byte("printf 'compiling %s\\n' \"$DEX_DATA_DIR\"\nwhile ! test -f "+filepath.Join(dir, "build.done")+"; do sleep 0.05; done\necho 'build failed: 2 errors' >&2\nexit 3\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	// When the bot starts it.
	info := bgStart(t, d, chat, map[string]any{"command": script, "label": "build", "env": map[string]string{"DEX_DATA_DIR": "review fixture"}})
	// Then the start call returned while the build runs, and no wake-up exists yet.
	if got := bgList(t, d, chat); len(got) != 1 || got[0].ID != info.ID || got[0].State != "running" || got[0].Label != "build" {
		t.Fatalf("process_list while the build runs: %+v", got)
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
	if wake.result.ID != strings.TrimPrefix(info.ID, "command-") || wake.result.Event != "exited" || wake.result.ExitStatus == nil || *wake.result.ExitStatus != 3 {
		t.Fatalf("wake-up: %+v", wake.result)
	}
	if !strings.Contains(wake.result.Output, "compiling review fixture") || !strings.Contains(wake.result.Output, "build failed: 2 errors") {
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
	if err != nil || string(data) != "compiling review fixture\nbuild failed: 2 errors\n" {
		t.Fatalf("output file %q: %v", data, err)
	}
	if got := bgList(t, d, chat); got[0].State != "exited" || got[0].ExitStatus == nil || *got[0].ExitStatus != 3 {
		t.Fatalf("process_list after the exit: %+v", got)
	}
}

func TestUserGetsBatchedWakeUpsForTheLinesOfAWatch(t *testing.T) {
	t.Parallel()
	// Given a CI watch with wake lines that prints each check result and exits when the run completes.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	chat := bgChat(t, d)
	const value = "monitor-fixture-secret"
	saveVault(t, d, "default", "agent", "CI_TOKEN", value)
	d.applied(t)
	gate := filepath.Join(dir, "second.gate")
	command := fmt.Sprintf("echo \"lint: $CI_TOKEN\"; echo 'test: pass'; echo 'ignored' >&2; until test -f %s; do sleep 0.05; done; echo \"deploy: $CI_TOKEN\"", gate)
	info := bgStart(t, d, chat, map[string]any{"command": command, "wake": "lines", "label": "ci", "env": map[string]string{"CI_TOKEN": "vault:CI_TOKEN"}})
	// Then the first two lines arrive as one wake-up, and the line of stderr is not an event.
	first := waitWakes(t, conn, chat, 1)[0]
	if first.result.Event != "lines" || strings.Join(first.result.Lines, "|") != "lint: [vault:CI_TOKEN]|test: pass" {
		t.Fatalf("first wake-up: %+v", first.result)
	}
	// When the run completes after the first batch.
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Then the last line arrives, and then the end of the watch.
	wakes := waitWakes(t, conn, chat, 3)
	if wakes[1].result.Event != "lines" || strings.Join(wakes[1].result.Lines, "|") != "deploy: [vault:CI_TOKEN]" {
		t.Fatalf("second wake-up: %+v", wakes[1].result)
	}
	if end := wakes[2].result; end.Event != "exited" || end.ExitStatus == nil || *end.ExitStatus != 0 || end.ID != strings.TrimPrefix(info.ID, "command-") || !strings.Contains(end.Output, "deploy: [vault:CI_TOKEN]") || strings.Contains(end.Output, value) {
		t.Fatalf("end of the watch: %+v", end)
	}
	// And the output file has the lines of both streams.
	data, err := os.ReadFile(info.OutputFile)
	if err != nil || !strings.Contains(string(data), "ignored") || !strings.Contains(string(data), "deploy: "+value) {
		t.Fatalf("output file %q: %v", data, err)
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

func TestUserMainStopLeavesBackgroundCommandAndMonitorActive(t *testing.T) {
	t.Parallel()
	// Given a background command and a scheduled Monitor on Main.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	main := d.mainChat(t)
	d.setModel(t, "spawn")
	command := bgStart(t, d, main, map[string]any{"command": taskGate(t, dir, "main-bg"), "wake": "none"})
	waitTaskFile(t, filepath.Join(dir, "main-bg.pid"))
	var schedule bgInfo
	toolJSON(t, callSessionTool(t, d, main, "monitor_set", map[string]any{"delaySeconds": 300, "prompt": "check later"}), &schedule)

	// When the user stops the foreground turn, only that turn ends.
	d.setModel(t, "bash")
	d.send(t, main, taskGate(t, dir, "main-fg"))
	waitTaskFile(t, filepath.Join(dir, "main-fg.pid"))
	target := d.watch(t, main).activeRunTarget(t)
	d.must(t, http.MethodPost, chatPath(main, "abort?runTarget="+url.QueryEscape(target)), nil, nil)
	waitTaskPIDGone(t, filepath.Join(dir, "main-fg.pid"))
	if state, _ := bgRow(t, conn, command.ID); state != "running" {
		t.Fatalf("background command after Main Stop: %s", state)
	}
	if n := count(t, conn, `SELECT count(*) FROM monitors WHERE conversation_id=? AND state='scheduled'`, main); n != 1 {
		t.Fatalf("expected the scheduled Monitor after Main Stop, got %d", n)
	}
	releaseResultGate(t, dir, "main-bg")
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
	if _, err := conn.ExecContext(t.Context(), `UPDATE background_commands SET state = 'running', reason = '', exit_status = NULL, ended_at = NULL, pid = ?, pgid = ? WHERE id = ?`, process.Process.Pid, process.Process.Pid, strings.TrimPrefix(info.ID, "command-")); err != nil {
		t.Fatal(err)
	}
	// When dex starts again with the same data.
	d = startDex(t, dir, "")
	// Then the list says unknown, and dex does not claim that the process stopped.
	list := bgList(t, d, chat)
	if len(list) != 1 || list[0].ID != info.ID || list[0].State != "unknown" || list[0].ExitStatus != nil {
		t.Fatalf("process_list after the crash: %+v", list)
	}
	// And dex does not signal a process that it cannot identify.
	answer := callSessionTool(t, d, chat, "process_stop", map[string]any{"id": info.ID})
	if !strings.HasPrefix(answer, "spawn error:") || !strings.Contains(answer, "crashed") {
		t.Fatalf("process_stop on an unknown command: %s", answer)
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
		t.Fatalf("process_list after the restart: %+v", list)
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

func TestUserParentReadsTheCutAnswerOfAChildTurn(t *testing.T) {
	t.Parallel()
	// Given a child session whose bash output makes its parent-turn answer exceed the inline limit.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := newParent(t, d)
	command := "for i in $(seq 1 20); do head -c 300 /dev/zero | tr '\\0' z; echo; done"
	child := spawnChild(t, d, parent, "Builder", command, "bash")
	// When the turn of the child ends, the parent gets a result with a cut answer.
	var turn string
	eventually(t, "a cut result of the child turn", func() (bool, any) {
		rows := resultRows(t, conn, parent)
		for _, row := range rows {
			if strings.Contains(row.content, "The answer is cut") {
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

func TestUserChildBackgroundWakeDoesNotAutoEscalateToParent(t *testing.T) {
	t.Parallel()
	// Given a child session that starts a background command with wake=exit.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := newParent(t, d)
	done := filepath.Join(dir, "child.done")
	command := fmt.Sprintf("until test -f %s; do sleep 0.02; done; echo bg-finished", done)
	message := childJSON(t, map[string]any{"tool": "process_start", "command": command})
	child := spawnChild(t, d, parent, "Worker", message, "spawn")
	waitResults(t, conn, parent, 1)
	if err := os.WriteFile(done, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// When the background command finishes, the child receives its wake-up,
	// and the parent receives no extra auto-escalated task_result.
	childID, err := strconv.ParseInt(child.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	wakes := waitWakes(t, conn, childID, 1)
	if wakes[0].result.Event != "exited" {
		t.Fatalf("expected child exit wake, got %+v", wakes[0].result)
	}
	rows := resultRows(t, conn, parent)
	if len(rows) != 1 {
		t.Fatalf("parent must only have the initial spawn result, got %d: %+v", len(rows), rows)
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
	answer := callSessionTool(t, d, chat, "process_start", map[string]any{"command": "true"})
	// Then the call fails, and a stop makes room again.
	if !strings.HasPrefix(answer, "spawn error:") || !strings.Contains(answer, "too many background commands") {
		t.Fatalf("answer: %s", answer)
	}
	toolJSON(t, callSessionTool(t, d, chat, "process_stop", map[string]any{"id": first.ID}), &bgInfo{})
	bgStart(t, d, chat, map[string]any{"command": "true"})
}

func TestUserManagesMonitorsAndProcessesSeparately(t *testing.T) {
	t.Parallel()
	// Given a timer, a check, and a command in one session and a timer in another.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	chat, other := bgChat(t, d), bgChat(t, d)
	setSchedule := func(chat int64, checkCommand string) bgInfo {
		t.Helper()
		var info bgInfo
		toolJSON(t, callSessionTool(t, d, chat, "monitor_set", map[string]any{
			"delaySeconds": 300, "prompt": "fixture", "checkCommand": checkCommand,
		}), &info)
		return info
	}
	timer, check, foreign := setSchedule(chat, ""), setSchedule(chat, "exit 10"), setSchedule(other, "")
	leader, child := filepath.Join(dir, "leader.pid"), filepath.Join(dir, "child.pid")
	command := bgStart(t, d, chat, map[string]any{"command": fmt.Sprintf("echo $$ > %s; sleep 300 & echo $! > %s; wait", leader, child), "wake": "exit"})
	killAtEnd(t, pidFile(t, leader))
	killAtEnd(t, pidFile(t, child))

	// Then each list shows only its own resource type from the calling session.
	var listed struct {
		Monitors []bgInfo `json:"monitors"`
	}
	toolJSON(t, callSessionTool(t, d, chat, "monitor_list", map[string]any{}), &listed)
	byID := make(map[string]bgInfo)
	for _, monitor := range listed.Monitors {
		byID[monitor.ID] = monitor
	}
	if len(listed.Monitors) != 2 || byID[timer.ID].Mode != "prompt" || byID[check.ID].Mode != "script" {
		t.Fatalf("session Monitor list: %+v", listed.Monitors)
	}
	if list := bgList(t, d, chat); len(list) != 1 || list[0].ID != command.ID || list[0].State != "running" {
		t.Fatalf("session Process list: %+v", list)
	}
	for _, attempt := range []struct {
		chat int64
		tool string
		id   string
	}{
		{chat, "monitor_cancel", command.ID}, {chat, "monitor_cancel", foreign.ID},
		{chat, "process_stop", timer.ID}, {other, "process_stop", command.ID},
	} {
		if got := callSessionTool(t, d, attempt.chat, attempt.tool, map[string]any{"id": attempt.id}); !strings.HasPrefix(got, "spawn error:") {
			t.Fatalf("%s %s accepted the wrong resource: %s", attempt.tool, attempt.id, got)
		}
	}
	for _, attempt := range []struct{ tool, id, state string }{
		{"monitor_cancel", timer.ID, "cancelled"},
		{"monitor_cancel", strings.TrimPrefix(check.ID, "schedule-"), "cancelled"},
		{"process_stop", command.ID, "stopped"},
	} {
		var info bgInfo
		toolJSON(t, callSessionTool(t, d, chat, attempt.tool, map[string]any{"id": attempt.id}), &info)
		if info.State != attempt.state {
			t.Fatalf("%s %s: %+v, want %s", attempt.tool, attempt.id, info, attempt.state)
		}
	}
	waitTaskPIDGone(t, leader)
	waitTaskPIDGone(t, child)
	if wakes := bgWakes(t, conn, chat); len(wakes) != 0 {
		t.Fatalf("cancelled command woke the session: %+v", wakes)
	}
}

func TestUserMonitorsAndProcessesShareSessionCapacity(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d := startDex(t, dir, "")
	chat := bgChat(t, d)
	// Given one active Process and 31 active Monitors.
	process := bgStart(t, d, chat, map[string]any{"command": taskGate(t, dir, "shared-cap"), "wake": "none"})
	args := map[string]any{"delaySeconds": 3600, "prompt": "check later"}
	for range 31 {
		toolJSON(t, callSessionTool(t, d, chat, "monitor_set", args), &bgInfo{})
	}
	// When either tool tries to add one more item, the shared cap denies it.
	for tool, input := range map[string]map[string]any{"monitor_set": args, "process_start": {"command": "true"}} {
		if got := callSessionTool(t, d, chat, tool, input); !strings.Contains(got, "32 active Monitors and Processes combined") {
			t.Fatalf("%s exceeded shared capacity: %s", tool, got)
		}
	}
	// Then stopping the Process frees the slot for a Monitor.
	toolJSON(t, callSessionTool(t, d, chat, "process_stop", map[string]any{"id": strings.TrimPrefix(process.ID, "command-")}), &bgInfo{})
	toolJSON(t, callSessionTool(t, d, chat, "monitor_set", args), &bgInfo{})
}

func TestUserCannotPassCommandArgumentsToASchedule(t *testing.T) {
	t.Parallel()
	d := startDex(t, t.TempDir(), "")
	chat := bgChat(t, d)
	for _, tc := range []struct {
		key  string
		arg  any
		want string
	}{
		{"command", "exit 10", "command"},
		{"env", map[string]string{"FIXTURE": "vault:FIXTURE"}, "env requires checkCommand"},
	} {
		got := callSessionTool(t, d, chat, "monitor_set", map[string]any{
			"delaySeconds": 300, "prompt": "fixture", tc.key: tc.arg,
		})
		if !strings.HasPrefix(got, "spawn error:") || !strings.Contains(got, tc.want) {
			t.Fatalf("schedule with %s: %s", tc.key, got)
		}
	}
}

func TestUserTaskCanSetARepeatingCheckAfterCommandExit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := newParent(t, d)
	child := spawnChild(t, d, parent, "Check worker", childJSON(t, map[string]any{
		"tool": "process_start", "command": "printf ready", "wake": "exit", "label": "check fixture",
	}), "spawn-monitor").conversation(t)
	wake := waitWakes(t, conn, child, 1)[0]
	if wake.result.Event != "exited" || !strings.Contains(wake.answer, `"kind":"schedule"`) || !strings.Contains(wake.answer, `"maxRuns":2`) {
		t.Fatalf("task did not set a repeating check on exit: %+v", wake)
	}
}

func TestUserProcessWakeChainCannotScheduleMonitorPastLimit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := newParent(t, d)
	child := spawnChild(t, d, parent, "Chain worker", childJSON(t, map[string]any{
		"tool": "process_start", "command": "printf ready", "wake": "exit", "label": "chain fixture 6",
	}), "spawn-monitor").conversation(t)
	wakes := waitWakes(t, conn, child, 6)
	if len(wakes) != 6 || !strings.Contains(wakes[5].answer, "wait for user input") {
		t.Fatalf("six Processes followed by refusal to set a Monitor: %+v", wakes)
	}
	// A new user turn can set another command in the same session.
	var info bgInfo
	toolJSON(t, callSessionTool(t, d, child, "process_start", map[string]any{"command": "printf reset", "wake": "none"}), &info)
	if info.ID == "" {
		t.Fatalf("command after user input: %+v", info)
	}
}

func TestUserCollectsParallelBashResultsInCodemode(t *testing.T) {
	t.Parallel()
	d := startDex(t, t.TempDir(), "")
	saveVault(t, d, "default", "agent", "GITHUB_TOKEN", "codemode-fixture-secret")
	answer := callSessionTool(t, d, bgChat(t, d), "codemode", map[string]any{"code": `
const results = await Promise.all([
  tools.bash({command: 'printf %s "$GH_TOKEN"', env: {GH_TOKEN: "vault:GITHUB_TOKEN"}}),
  tools.bash({command: "printf second; exit 7"}),
]);
if (!results[0].output.startsWith("codemode-fixture-")) throw new Error("Expected the value inside the script");
return results;`})
	if strings.Contains(answer, "codemode-fixture-secret") {
		t.Fatalf("codemode leaked a vault value: %s", answer)
	}
	_, body, ok := strings.Cut(html.UnescapeString(answer), "\nOutput:\n")
	if !ok {
		t.Fatalf("codemode result: %s", answer)
	}
	var results []struct {
		Output    string `json:"output"`
		Truncated *bool  `json:"truncated"`
		ExitCode  *int   `json:"exitCode"`
	}
	if err := json.Unmarshal([]byte(body), &results); err != nil || len(results) != 2 {
		t.Fatalf("expected two structured results: %s (%v)", body, err)
	}
	for i, want := range []struct {
		output string
		exit   int
	}{{"[vault:GITHUB_TOKEN]", 0}, {"second", 7}} {
		got := results[i]
		if got.Output != want.output || got.Truncated == nil || *got.Truncated || got.ExitCode == nil || *got.ExitCode != want.exit {
			t.Fatalf("bash result %d: %s", i, body)
		}
	}
}
