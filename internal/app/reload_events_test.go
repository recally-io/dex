package app_test

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/runner"
	"github.com/recally-io/dex/internal/sysops"
)

type observedStamps struct {
	runner.Provider
	checked chan struct{}
}

func (p *observedStamps) Stamp(ctx context.Context, agentID, envID string) (string, error) {
	select {
	case p.checked <- struct{}{}:
	default:
	}
	return p.Provider.Stamp(ctx, agentID, envID)
}

func TestUserIdleBotDoesNotCheckExtensionStampsPeriodically(t *testing.T) {
	t.Parallel()
	data, home := t.TempDir(), t.TempDir()
	p := &observedStamps{Provider: runner.NewLocalProvider(data, home), checked: make(chan struct{}, 1)}
	d := startDexHome(t, app.Config{DataDir: data, ExecEnv: p}, home)
	d.setModel(t, "echo")
	if got := taskTurn(t, d, d.newSession(t).ID, "finished"); got != "echo: finished" {
		t.Fatal(got)
	}
	select {
	case <-p.checked:
	default:
	}
	// Leave the bot idle for longer than the old scheduler interval.
	deadline := time.NewTimer(4 * time.Second)
	defer deadline.Stop()
	select {
	case <-p.checked:
		t.Fatal("dex checked the idle bot's environment stamp without a change")
	case <-deadline.C:
	}
}

func TestUserRunnerEditReloadsOnlyItsIdleBot(t *testing.T) {
	t.Parallel()
	data, files := t.TempDir(), t.TempDir()
	seedMCP(t, data, app.DefaultAgentID, map[string]any{"files": stdioServer(t, files)})
	d := startDexWith(t, app.Config{DataDir: data})
	m := enrollMachine(t, d)
	m.connect(d)
	useMachine(t, d, m, "tool")
	var other apiAgent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "Local", "provider": "faux", "modelId": "echo", "runnerId": ""}, &other, http.StatusCreated)
	otherChat := (&bots{t: t, d: d}).echoChat(other.ID, "before the edit")
	otherWatch := otherChat.watch()

	// The edit must wait for the active MCP call before replacing the bot's resources.
	chat := d.newChat(t, `mcp__files__wait {"name":"edit"}`)
	w := d.watch(t, chat)
	eventually(t, "active MCP call", func() (bool, any) {
		got := readNotes(t, files)
		return strings.Contains(got, "started edit"), got
	})
	d.must(t, http.MethodPut, "/api/settings/runners/"+m.envID, map[string]string{"name": "New machine name", "kind": "runner"}, nil)
	var pending apiExtensionsPending
	d.must(t, http.MethodGet, "/api/agents/default/integrations", nil, &pending)
	if !pending.ReloadPending {
		t.Fatal("the environment edit did not request a reload")
	}
	writeFile(t, filepath.Join(files, "edit.release"), "")
	w.until(t, "active call completes", answered("tool: released edit"))
	d.applied(t)
	waitReloadStream(t, w)

	// A reload closes the bot's old streams. The other bot keeps its stream.
	otherChat.send("after the edit")
	otherWatch.until(t, "other bot keeps its connection", answered("echo: after the edit"))
}

func TestUserIdleBotRestoresMachineWithoutNewInput(t *testing.T) {
	t.Parallel()
	cfg := app.Config{DataDir: t.TempDir()}
	first := startDexWith(t, cfg)
	m := enrollMachine(t, first)
	m.connect(first)
	useMachine(t, first, m, "echo")
	writeSkill(t, filepath.Join(m.botWorkspace("default"), ".agents", "skills"), "machine-only", "description: Available on the machine")
	m.disconnect()
	first.stop()

	d := startDexWith(t, cfg)
	chat := d.newSession(t).ID
	d.must(t, http.MethodPut, chatPath(chat, "runner"), map[string]string{"runnerId": "local"}, nil)
	d.send(t, chat, "open without the default")
	w := d.watch(t, chat)
	w.until(t, "the local turn ends", answered("echo: open without the default"))

	m.connect(d)
	waitReloadStream(t, w)
	eventually(t, "the skill returns after reconnect", func() (bool, any) {
		info := d.extensions(t)
		for _, skill := range info.Skills {
			if skill.Name == "machine-only" {
				return true, nil
			}
		}
		return false, info
	})
}

func waitReloadStream(t *testing.T, w *watcher) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case _, ok := <-w.events:
			if !ok {
				return
			}
		case <-deadline.C:
			t.Fatal("the old event stream is still open after a reload was requested")
		}
	}
}

func TestUserKeepsWorkingIntegrationsUntilRefusedConfigChanges(t *testing.T) {
	data := t.TempDir()
	logs := sysops.NewLogBuffer(100)
	old := slog.Default()
	slog.SetDefault(slog.New(sysops.TeeHandler(slog.DiscardHandler, logs)))
	t.Cleanup(func() { slog.SetDefault(old) })
	d := startDexWith(t, app.Config{DataDir: data, Logs: logs})
	d.setModel(t, "tool")
	chat := d.newSession(t).ID
	if got := taskTool(t, d, chat, "mcp_server_list", map[string]string{}); got != "tool: []" {
		t.Fatalf("open agent: %q", got)
	}
	writeSkill := func(content string) {
		t.Helper()
		got := taskTool(t, d, chat, "skill_write", map[string]string{"path": "agent/demo/note.txt", "content": content})
		if !strings.Contains(got, "note.txt") {
			t.Fatalf("skill write: %q", got)
		}
	}
	refusals := func() int {
		var out api.SystemLogs
		d.must(t, http.MethodGet, "/api/system/logs?limit=50", nil, &out)
		n := 0
		for _, entry := range out.Entries {
			if entry.Message == "extension change not applied; fix the configuration" {
				n++
			}
		}
		return n
	}

	// The manual edit invalidates the vault. A tool write requests a reload but leaves the working bot in place.
	path := filepath.Join(data, "vault", "agent-default.age")
	writeFile(t, path, "invalid vault")
	writeSkill("first")
	eventually(t, "invalid configuration warning", func() (bool, any) {
		n := refusals()
		return n == 1, n
	})

	// Another tool write must not retry validation of the same refused configuration.
	writeSkill("second")
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticks := time.NewTicker(50 * time.Millisecond)
	defer ticks.Stop()
wait:
	for {
		if n := refusals(); n != 1 {
			t.Fatalf("unchanged invalid configuration was tried again: %d warnings", n)
		}
		select {
		case <-deadline.C:
			break wait
		case <-ticks.C:
		}
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	w := d.watch(t, chat)
	writeSkill("after the fix")
	waitReloadStream(t, w)
}
