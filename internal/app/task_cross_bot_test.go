package app_test

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"uuid"

	"github.com/recally-io/dex/internal/mcptest"
)

// botTasks drives one bot's Chat through HTTP; tasks inherit the Chat project.
type botTasks struct {
	d     *dex
	agent string
	chat  int64
}

func newBotTasks(t *testing.T, d *dex, agent, project string) botTasks {
	t.Helper()
	if agent != "default" {
		var created apiAgent
		d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": agent, "provider": "faux", "modelId": "spawn", "thinkingLevel": "off"}, &created, http.StatusCreated)
		agent = created.ID
	} else {
		d.setModel(t, "spawn")
	}
	var c sessionInfo
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/%s/chats", agent), nil, &c, http.StatusCreated)
	if project != "" {
		d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/%s/chats/%d/project", agent, c.ID), map[string]string{"cwd": project}, nil)
	}
	return botTasks{d: d, agent: agent, chat: c.ID}
}

// spawn asks the bot's faux spawn model to delegate a gated bash task.
func (b botTasks) spawn(t *testing.T, dir, title string, modifies bool) {
	t.Helper()
	text := taskJSON(t, map[string]any{"title": title, "goal": taskGate(t, dir, title), "provider": "faux", "modelId": "bash", "modifiesFiles": modifies})
	b.d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/%s/chats/%d/messages", b.agent, b.chat), map[string]string{"id": uuid.NewV7().String(), "text": text}, nil, http.StatusAccepted)
}

// idle waits until the bot's Chat has answered every message.
func (b botTasks) idle(t *testing.T) {
	t.Helper()
	eventually(t, "idle Chat", func() (bool, any) {
		var c struct {
			Busy bool `json:"busy"`
		}
		b.d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/%s/chats/%d", b.agent, b.chat), nil, &c)
		return !c.Busy, c
	})
}

// waitQueued waits until the scheduler has evaluated a queued task and returns its reason.
func (b botTasks) waitQueued(t *testing.T, id, want string) {
	t.Helper()
	eventually(t, "wait reason "+want, func() (bool, any) {
		var task struct {
			Status     string `json:"status"`
			WaitReason string `json:"waitReason"`
		}
		b.d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/%s/tasks/%s", b.agent, id), nil, &task)
		return task.Status == "queued" && task.WaitReason == want, task
	})
}

func TestUserBotsShareOneProjectLockForModifyingTasks(t *testing.T) {
	t.Parallel()
	// Given two bots whose Chats use the same project.
	dir, project := t.TempDir(), t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	a := newBotTasks(t, d, "default", project)
	b := newBotTasks(t, d, "Other", project)
	// When both delegate a task that modifies files.
	a.spawn(t, dir, "a-writes", true)
	waitTaskFile(t, filepath.Join(dir, "a-writes.pid"))
	holder := waitExecution(t, conn, a.chat, "a-writes", "running")
	b.spawn(t, dir, "b-writes", true)
	queued := waitExecution(t, conn, b.chat, "b-writes", "queued")
	// Then the second bot waits for the holder, and starts after it finishes.
	b.waitQueued(t, queued.ID, "cwd held by task "+holder.ID)
	if _, err := os.Stat(filepath.Join(dir, "b-writes.pid")); !os.IsNotExist(err) {
		t.Fatalf("second writer started while the project was held: %v", err)
	}
	releaseResultGate(t, dir, "a-writes")
	waitTaskFile(t, filepath.Join(dir, "b-writes.pid"))
	releaseResultGate(t, dir, "b-writes")
	waitExecution(t, conn, b.chat, "b-writes", "done")
	t.Logf("artifact: holder=%s queued=%s", holder.ID, queued.ID)
}

func TestUserGlobalTaskLimitQueuesAnIdleBot(t *testing.T) {
	t.Parallel()
	// Given two bots that each run four read-only tasks.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	bots := []botTasks{newBotTasks(t, d, "default", ""), newBotTasks(t, d, "Second", "")}
	for i, b := range bots {
		for j := range 4 {
			b.spawn(t, dir, fmt.Sprintf("bot%d-%d", i, j), false)
		}
	}
	for i, b := range bots {
		for j := range 4 {
			name := fmt.Sprintf("bot%d-%d", i, j)
			waitTaskFile(t, filepath.Join(dir, name+".pid"))
			waitExecution(t, conn, b.chat, name, "running")
		}
	}
	// When an idle third bot delegates a task.
	idle := newBotTasks(t, d, "Idle", "")
	idle.spawn(t, dir, "idle-task", false)
	queued := waitExecution(t, conn, idle.chat, "idle-task", "queued")
	// Then it waits for the global limit and starts when any running task ends.
	idle.waitQueued(t, queued.ID, "global limit")
	if _, err := os.Stat(filepath.Join(dir, "idle-task.pid")); !os.IsNotExist(err) {
		t.Fatalf("idle bot exceeded the global limit: %v", err)
	}
	releaseResultGate(t, dir, "bot0-0")
	waitTaskFile(t, filepath.Join(dir, "idle-task.pid"))
	for i := range 2 {
		for j := range 4 {
			releaseResultGate(t, dir, fmt.Sprintf("bot%d-%d", i, j))
		}
	}
	releaseResultGate(t, dir, "idle-task")
	waitExecution(t, conn, idle.chat, "idle-task", "done")
}

// heldMCP is an MCP server whose initialize waits until release.
func heldMCP(t *testing.T) (map[string]any, <-chan struct{}, func()) {
	t.Helper()
	entered, gate := make(chan struct{}), make(chan struct{})
	var enter, open sync.Once
	mcp := &mcptest.Server{Dir: t.TempDir()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var req struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(body, &req) == nil && req.Method == "initialize" {
			enter.Do(func() { close(entered) })
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		mcp.ServeHTTP(w, r)
	}))
	release := func() { open.Do(func() { close(gate) }) }
	t.Cleanup(func() { release(); srv.Close() })
	return map[string]any{"type": "http", "url": srv.URL + "/mcp"}, entered, release
}

func TestUserCancelDuringSlowAdmissionDoesNotBlockOtherBots(t *testing.T) {
	t.Parallel()
	// Given bot B has a writer queued behind bot A's writer in one project.
	dir, project := t.TempDir(), t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	holderBot := newBotTasks(t, d, "default", project)
	b := newBotTasks(t, d, "Slow", project)
	holderBot.spawn(t, dir, "a-holder", true)
	waitTaskFile(t, filepath.Join(dir, "a-holder.pid"))
	b.spawn(t, dir, "b-target", true)
	target := waitExecution(t, conn, b.chat, "b-target", "queued")
	b.waitQueued(t, target.ID, "cwd held by task "+waitExecution(t, conn, holderBot.chat, "a-holder", "running").ID)
	b.idle(t)
	holderBot.idle(t)
	// When restart interrupts A's holder and B's agent opens through a held MCP server.
	d.stop()
	server, entered, release := heldMCP(t)
	writeMCP(t, filepath.Join(dir, "agents", b.agent, ".agents", "mcp.json"), map[string]any{"held": server})
	d = startDex(t, dir, "")
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("bot B did not start opening its agent")
	}
	waitExecution(t, conn, b.chat, "b-target", "admitting")
	// Then bot A can still start and cancel a task while B's admission is blocked.
	a := newBotTasks(t, d, "default", "")
	a.spawn(t, dir, "a-running", true)
	waitTaskFile(t, filepath.Join(dir, "a-running.pid"))
	running := waitExecution(t, conn, a.chat, "a-running", "running")
	done := make(chan int, 1)
	go func() {
		status, _ := d.call(t, http.MethodPost, fmt.Sprintf("/api/agents/default/tasks/%s/cancel", running.ID), nil)
		done <- status
	}()
	select {
	case status := <-done:
		if status != http.StatusOK {
			t.Fatalf("cancel A status=%d", status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelling bot A waited for bot B's admission")
	}
	waitExecution(t, conn, a.chat, "a-running", "cancelled")
	// And B's task cancelled during admission never runs or reports.
	if status, raw := d.call(t, http.MethodPost, fmt.Sprintf("/api/agents/%s/tasks/%s/cancel", b.agent, target.ID), nil); status != http.StatusOK {
		t.Fatalf("cancel B status=%d %s", status, raw)
	}
	waitExecution(t, conn, b.chat, "b-target", "cancelled")
	release()
	time.Sleep(2 * time.Second) // observe that a late admission does not start the cancelled task
	if _, err := os.Stat(filepath.Join(dir, "b-target.pid")); !os.IsNotExist(err) {
		t.Fatalf("cancelled task ran: %v", err)
	}
	var results int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE id = ?", "task:"+target.ID+":result").Scan(&results); err != nil || results != 0 {
		t.Fatalf("cancelled task results=%d err=%v", results, err)
	}
	waitExecution(t, conn, b.chat, "b-target", "cancelled")
}
