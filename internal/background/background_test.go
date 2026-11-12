package background_test

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/background"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

// These tests run real shell processes against a real control.sqlite. They cover the process
// rules that the HTTP tests in internal/app cannot reach: a crash window, process groups that
// outlive their leader, and the limits of a lines batch.

type fixture struct {
	t            *testing.T
	dir          string
	conn         *sql.DB
	q            *sqlc.Queries
	m            *background.Manager
	conv         int64
	source       string
	nextKey      int
	closeManager func()
}

func newFixture(t *testing.T, limits background.Limits) *fixture {
	t.Helper()
	dir := t.TempDir()
	conn, err := db.Open(t.Context(), filepath.Join(dir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)
	if err := q.CreateAgentIfMissing(t.Context(), sqlc.CreateAgentIfMissingParams{ID: "bot", Name: "Bot", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	conv, err := q.EnsureConversation(t.Context(), sqlc.EnsureConversationParams{AgentID: "bot", ChannelKey: "chat:test", Title: "t", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{ID: "source", ConversationID: conv.ID, Content: "run", Attachments: "null", Origin: "web", Kind: "message", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, dir: dir, conn: conn, q: q, conv: conv.ID, source: "source"}
	f.m = background.New(conn, time.Now, limits, nil)
	closed := false
	t.Cleanup(func() {
		if !closed {
			f.m.Close()
		}
	})
	f.closeManager = func() { closed = true; f.m.Close() }
	return f
}

func (f *fixture) params(command, wake string) background.StartParams {
	f.nextKey++
	return background.StartParams{
		CallKey: fmt.Sprintf("bot/%d", f.nextKey), AgentID: "bot", ConversationID: f.conv, SourceRequestID: f.source,
		Command: command, Wake: wake, DefaultCwd: f.dir, OutputDir: filepath.Join(f.dir, "out"), Env: map[string]string{"PATH": os.Getenv("PATH")},
	}
}

func (f *fixture) start(command, wake string) sqlc.BackgroundCommand {
	f.t.Helper()
	row, err := f.m.Start(f.t.Context(), f.params(command, wake))
	if err != nil {
		f.t.Fatal(err)
	}
	return row
}

func (f *fixture) state(id string) (state, reason string) {
	f.t.Helper()
	if err := f.conn.QueryRowContext(f.t.Context(), `SELECT state, reason FROM background_commands WHERE id = ?`, id).Scan(&state, &reason); err != nil {
		f.t.Fatal(err)
	}
	return state, reason
}

func (f *fixture) waitExited(id string) {
	f.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		state, reason := f.state(id)
		if state == background.StateExited {
			return
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("state of %s is %s (%s), want %s", id, state, reason, background.StateExited)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type wake struct {
	ID      string
	Content string
	Body    struct {
		Result struct {
			Event   string   `json:"event"`
			Notice  string   `json:"notice"`
			Lines   []string `json:"lines"`
			Omitted int      `json:"omitted"`
			Output  string   `json:"output"`
		} `json:"background-result"`
	}
}

func (f *fixture) wakes() []wake {
	f.t.Helper()
	rows, err := f.conn.QueryContext(f.t.Context(), `SELECT id, content FROM submissions WHERE id LIKE 'background:%' ORDER BY rowid`)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []wake
	for rows.Next() {
		var w wake
		if err := rows.Scan(&w.ID, &w.Content); err != nil {
			f.t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(w.Content), &w.Body); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *fixture) waitWakes(n int) []wake {
	f.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if got := f.wakes(); len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("want %d wake-ups, got %+v", n, f.wakes())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if data, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(data)) != "" {
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL); _ = syscall.Kill(pid, syscall.SIGKILL) })
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatalf("no PID in %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func gone(pid int) bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }

func TestAReplayAfterACrashBeforeTheProcessStartsNeverRunsTheCommand(t *testing.T) {
	t.Parallel()
	// Given the row of a start that crashed after the insert and before the process: state starting.
	f := newFixture(t, background.Limits{})
	p := f.params("echo ran", background.WakeExit)
	if _, err := f.q.CreateBackgroundCommand(t.Context(), sqlc.CreateBackgroundCommandParams{
		ID: "crashed", CallKey: p.CallKey, ConversationID: f.conv, SourceRequestID: f.source, Wake: p.Wake, Label: "l", Command: p.Command,
		Cwd: f.dir, OutputPath: filepath.Join(f.dir, "out", "crashed.log"), ProcessStartedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	// When the tool call replays after the restart.
	row, err := f.m.Start(t.Context(), p)
	// Then it gets the stored row, and no process starts: the output file that a start creates first does not exist.
	if err != nil || row.ID != "crashed" || row.State != background.StateStarting {
		t.Fatalf("replay: %+v, %v", row, err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "out")); err == nil {
		t.Fatal("the replay started a process")
	}
	// And the next start marks the row unknown.
	if err := f.m.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if state, _ := f.state("crashed"); state != background.StateUnknown {
		t.Fatalf("state after recovery: %s", state)
	}
}

func TestACommandThatEndsLeavesNoProcessOfItsGroup(t *testing.T) {
	t.Parallel()
	// Given a command whose shell exits and leaves a member that ignores SIGTERM.
	f := newFixture(t, background.Limits{Grace: 300 * time.Millisecond})
	pids := filepath.Join(f.dir, "member.pid")
	row := f.start(fmt.Sprintf("trap '' TERM; sleep 300 & echo $! > %s; exit 0", pids), background.WakeExit)
	member := readPID(t, pids)
	// When the state says exited, the member is gone already: the end is stored after the group ends.
	f.waitExited(row.ID)
	if !gone(member) {
		t.Fatalf("member %d of the process group still runs after the end was stored", member)
	}
	if wakes := f.waitWakes(1); wakes[0].Body.Result.Event != "exited" {
		t.Fatalf("wake-ups: %+v", wakes)
	}
}

func TestAnEndedRowAndItsEndWakeUpAppearTogether(t *testing.T) {
	t.Parallel()
	// Given a command that ends.
	f := newFixture(t, background.Limits{})
	row := f.start("echo done", background.WakeExit)
	f.waitExited(row.ID)
	// Then the end wake-up exists as soon as the row is ended.
	got := f.wakes()
	if len(got) != 1 || got[0].ID != "background:"+row.ID+":end" || !strings.Contains(got[0].Body.Result.Output, "done") {
		t.Fatalf("wake-ups: %+v", got)
	}
}

func TestAnArchivedBotGetsNoWakeUp(t *testing.T) {
	t.Parallel()
	// Given a command of a bot that is archived while the command runs.
	f := newFixture(t, background.Limits{})
	gate := filepath.Join(f.dir, "gate")
	row := f.start("until test -f "+gate+"; do sleep 0.02; done", background.WakeExit)
	if _, err := f.conn.ExecContext(t.Context(), `UPDATE agents SET archived_at = 1 WHERE id = 'bot'`); err != nil {
		t.Fatal(err)
	}
	// When it ends.
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Then the row ends and no input is stored.
	f.waitExited(row.ID)
	if got := f.wakes(); len(got) != 0 {
		t.Fatalf("wake-ups for an archived bot: %+v", got)
	}
}

func TestALinesBatchKeepsItsByteBudget(t *testing.T) {
	t.Parallel()
	// Given a command that prints 40 lines of 1800 bytes at once.
	f := newFixture(t, background.Limits{})
	row := f.start(`line=$(head -c 1800 /dev/zero | tr '\0' x); for i in $(seq 1 40); do echo "$line"; done`, background.WakeLines)
	f.waitExited(row.ID)
	// Then the batches stay within 8 KiB, and every line is either sent or counted.
	events := 0
	for _, w := range f.wakes() {
		if w.Body.Result.Event != "lines" {
			continue
		}
		size := 0
		for _, l := range w.Body.Result.Lines {
			size += len(l)
		}
		if size > 8<<10 {
			t.Fatalf("a batch has %d bytes", size)
		}
		events += len(w.Body.Result.Lines) + w.Body.Result.Omitted
	}
	if events != 40 {
		t.Fatalf("%d events, want 40", events)
	}
}

func TestALongLineDoesNotEndTheLineEvents(t *testing.T) {
	t.Parallel()
	// Given a command that prints one line of 200000 bytes, and then a short line.
	f := newFixture(t, background.Limits{})
	row := f.start(`head -c 200000 /dev/zero | tr '\0' y; echo; echo after`, background.WakeLines)
	f.waitExited(row.ID)
	// Then the long line is cut, and the next line still arrives.
	var lines []string
	for _, w := range f.wakes() {
		lines = append(lines, w.Body.Result.Lines...)
	}
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "...") || lines[1] != "after" {
		t.Fatalf("lines: %q", lines)
	}
	data, err := os.ReadFile(row.OutputPath)
	if err != nil || len(data) != 200000+1+len("after\n") {
		t.Fatalf("the output file lost bytes: %d, %v", len(data), err)
	}
}

func TestDexStopsItsCommandsWhenItShutsDown(t *testing.T) {
	t.Parallel()
	// Given a running command with a member process.
	f := newFixture(t, background.Limits{Grace: 300 * time.Millisecond})
	leader, member := filepath.Join(f.dir, "leader.pid"), filepath.Join(f.dir, "member.pid")
	row := f.start(fmt.Sprintf("echo $$ > %s; sleep 300 & echo $! > %s; wait", leader, member), background.WakeExit)
	leaderPID, memberPID := readPID(t, leader), readPID(t, member)
	// When dex shuts down.
	f.closeManager()
	// Then the group is gone, the state is stopped with reason shutdown, and the end wake-up says so.
	if !gone(leaderPID) || !gone(memberPID) {
		t.Fatalf("processes survive the shutdown: leader gone %v, member gone %v", gone(leaderPID), gone(memberPID))
	}
	if state, reason := f.state(row.ID); state != background.StateStopped || reason != "shutdown" {
		t.Fatalf("state %s, reason %s", state, reason)
	}
	got := f.wakes()
	if len(got) != 1 || got[0].Body.Result.Event != "stopped" || !strings.Contains(got[0].Body.Result.Notice, "shut down") {
		t.Fatalf("wake-ups: %+v", got)
	}
	// And a start after the shutdown fails.
	if _, err := f.m.Start(context.Background(), f.params("true", background.WakeExit)); !errors.Is(err, background.ErrInvalid) {
		t.Fatalf("start after Close: %v", err)
	}
}
