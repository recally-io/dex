package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/durable/env/node"
	"github.com/coder/websocket"
	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/runner/outbound"
)

// machine is one remote machine environment and the dex-agent that serves it.
type machine struct {
	t     *testing.T
	envID string
	token string
	state string
	ws    *websocket.Conn
	done  chan struct{}
}

// addMachine creates a machine environment in d. The machine is not enrolled yet.
func addMachine(t *testing.T, d *dex, name string) *machine {
	t.Helper()
	var created apiRunner
	d.must(t, http.MethodPost, "/api/settings/runners", map[string]any{"name": name, "kind": "runner"}, &created, http.StatusCreated)
	m := &machine{t: t, envID: created.ID, state: t.TempDir()}
	if err := os.Mkdir(filepath.Join(m.state, "workspace"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.disconnect)
	return m
}

// enrollMachine creates a machine environment in d and enrolls a machine for it.
func enrollMachine(t *testing.T, d *dex) *machine {
	t.Helper()
	m := addMachine(t, d, "Laptop")
	status, token := m.pair(d, m.issue(d))
	if status != http.StatusOK || token == "" {
		t.Fatalf("enrollment returned %d and token %q", status, token)
	}
	m.token = token
	return m
}

// issue makes a one-time enrollment code in Settings.
func (m *machine) issue(d *dex) string {
	m.t.Helper()
	var issued struct {
		Code string `json:"code"`
	}
	d.must(m.t, http.MethodPost, "/api/settings/runners/"+m.envID+"/pairing", nil, &issued)
	return issued.Code
}

// pair trades code for a machine token, as the installer does.
func (m *machine) pair(d *dex, code string) (int, string) {
	m.t.Helper()
	req, err := http.NewRequestWithContext(m.t.Context(), http.MethodPost, d.srv.URL+"/machine/"+m.envID+"/pair", nil)
	if err != nil {
		m.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+code)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		m.t.Fatal(err)
	}
	defer res.Body.Close()
	var body struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&body)
	return res.StatusCode, body.Token
}

func (m *machine) dial(d *dex, token string) (*websocket.Conn, error) {
	u := strings.Replace(d.srv.URL+"/machine/"+m.envID+"/connect", "http://", "ws://", 1)
	header := http.Header{"Authorization": []string{"Bearer " + token}}
	ws, res, err := websocket.Dial(m.t.Context(), u, &websocket.DialOptions{HTTPHeader: header, Subprotocols: []string{outbound.Version}})
	if res != nil && res.Body != nil {
		_ = res.Body.Close()
	}
	return ws, err
}

// connect opens a session to d with the machine token and waits until d reports it online.
func (m *machine) connect(d *dex) {
	m.t.Helper()
	ws, err := m.dial(d, m.token)
	if err != nil {
		m.t.Fatalf("connect the machine: %v", err)
	}
	m.ws, m.done = ws, make(chan struct{})
	go func(done chan struct{}) {
		defer close(done)
		_ = outbound.Serve(m.t.Context(), ws, node.NewNodeExecutionEnv(node.NodeExecutionEnvOptions{Cwd: filepath.Join(m.state, "workspace")}))
	}(m.done)
	m.waitOnline(d, true)
}

// disconnect drops the session without a close handshake, like a network failure.
func (m *machine) disconnect() {
	if m.ws == nil {
		return
	}
	_ = m.ws.CloseNow()
	<-m.done
	m.ws = nil
}

func (m *machine) waitOnline(d *dex, online bool) {
	m.t.Helper()
	eventually(m.t, fmt.Sprintf("machine online=%t", online), func() (bool, any) {
		var probe apiRunnerTestResult
		d.must(m.t, http.MethodPost, "/api/settings/runners/"+m.envID+"/test", nil, &probe)
		return probe.OK == online, probe
	})
}

// botWorkspace is the workspace of a bot on the machine.
func (m *machine) botWorkspace(botID string) string {
	return filepath.Join(m.state, "workspaces", botID, "workspace")
}

// TestUserEnrollsRemoteMachine installs a machine with one command. The machine trades the
// one-time code for a token and then connects with that token, over the same listener as the
// web UI, so a proxy in front of Dex needs no special configuration.
func TestUserEnrollsRemoteMachine(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	a, err := app.Start(ctx, app.Config{DataDir: t.TempDir(), Home: t.TempDir(), FakeModel: true, Token: "test-admin"})
	if err != nil {
		t.Fatal(err)
	}
	d := &dex{app: a, srv: httptest.NewServer(a.Handler())}
	t.Cleanup(d.stop)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	d.srv.Client().Jar = jar
	d.must(t, http.MethodPost, "/api/session", map[string]string{"token": "test-admin"}, nil, http.StatusNoContent)
	m := addMachine(t, d, "Debian VM")
	settingsPath := "/api/settings/runners/" + m.envID
	code := m.issue(d)

	// The code needs no certificate, and a browser request is refused.
	if status, _ := m.pair(d, ""); status != http.StatusUnauthorized {
		t.Fatalf("enrollment without a code returned %d", status)
	}
	anonymous, err := http.NewRequestWithContext(ctx, http.MethodPost, d.srv.URL+"/machine/"+m.envID+"/pair", nil)
	if err != nil {
		t.Fatal(err)
	}
	anonymous.Header.Set("Origin", "http://example.test")
	res, err := http.DefaultClient.Do(anonymous)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode == http.StatusOK {
		t.Fatal("a browser origin enrolled a machine")
	}
	status, token := m.pair(d, code)
	if status != http.StatusOK || token == "" {
		t.Fatalf("enrollment returned %d and token %q", status, token)
	}
	if status, _ := m.pair(d, code); status != http.StatusUnauthorized {
		t.Fatalf("a used code returned %d", status)
	}
	m.token = token

	if ws, err := m.dial(d, "wrong-token-that-is-long-enough"); err == nil {
		_ = ws.CloseNow()
		t.Fatal("a wrong token connected")
	}
	m.connect(d)
	// Both bots remain local by default and explicitly select the same runner for a session.
	d.setModel(t, "bash")
	var second apiBot
	d.must(t, http.MethodPost, "/api/bots", map[string]string{
		"name": "Second bot", "provider": "faux", "modelId": "bash", "thinkingLevel": "off",
	}, &second, http.StatusCreated)
	newSession := func(botID, runnerID string) botSession {
		var session sessionInfo
		d.must(t, http.MethodPost, "/api/bots/"+botID+"/sessions", nil, &session, http.StatusCreated)
		d.must(t, http.MethodPut, fmt.Sprintf("/api/bots/%s/sessions/%d/runner", botID, session.ID),
			map[string]string{"runnerId": runnerID}, nil)
		return botSession{bots: &bots{t: t, d: d}, bot: botID, id: session.ID}
	}
	first, other := newSession("default", m.envID), newSession(second.ID, m.envID)
	first.send(`printf 'first' > note.txt; cat note.txt`)
	first.watch().until(t, "first bot writes its own file", answeredWith("bash: first"))
	other.send(`test ! -e note.txt && printf 'second' > note.txt && cat note.txt`)
	other.watch().until(t, "second bot writes its own file", answeredWith("bash: second"))
	for botID, want := range map[string]string{"default": "first", second.ID: "second"} {
		got, err := os.ReadFile(filepath.Join(m.botWorkspace(botID), "note.txt"))
		if err != nil || string(got) != want {
			t.Fatalf("bot %s file = %q, want %q (err %v)", botID, got, want, err)
		}
	}

	// Changing the bot default still connects to the machine and survives an extension reload.
	var bot apiBot
	d.must(t, http.MethodGet, "/api/bots/default", nil, &bot)
	bot.RunnerID = m.envID
	d.must(t, http.MethodPut, "/api/bots/default", bot, &bot)
	for range 2 {
		reloadBot(t, d)
	}
	session := d.newSession(t).ID
	if got := taskTurn(t, d, session, `cat note.txt`); got != "bash: first" {
		t.Fatalf("default runner after reloading the bot: %q", got)
	}

	saveVault(t, d, "default", "agent", "GITHUB_TOKEN", "github-test-vault-value")
	d.applied(t)
	assertVaultCommand(t, d, session)
	d.setModel(t, "bash")

	d.must(t, http.MethodDelete, settingsPath+"/pairing", nil, nil)
	select {
	case <-m.done:
	case <-ctx.Done():
		t.Fatal("revocation did not close the machine")
	}
	if ws, err := m.dial(d, m.token); err == nil {
		_ = ws.CloseNow()
		t.Fatal("a revoked token reconnected")
	}

	// An offline runner remains listed, but an explicit session never runs on local.
	var listed struct {
		Runners []apiSessionRunner `json:"runners"`
	}
	d.must(t, http.MethodGet, "/api/bots/"+second.ID+"/runners", nil, &listed)
	if len(listed.Runners) != 2 || listed.Runners[1].ID != m.envID || listed.Runners[1].Status != "offline" {
		t.Fatalf("offline runner: %+v", listed.Runners)
	}
	other.send(`printf 'offline-fallback' > offline.txt`)
	other.watch().until(t, "offline runner refuses the tool", func(tr *transcript) bool {
		last := lastItem(tr)
		return !tr.running && last.Kind == "assistant" && (strings.Contains(last.Text, "offline") || strings.Contains(last.Text, "selected environment changed"))
	})
	// The unavailable default does not prevent an explicit local session in that bot.
	reloadBot(t, d)
	local := newSession("default", "local")
	local.send(`test ! -e note.txt && printf 'local-ok'`)
	local.watch().until(t, "explicit local when its bot runner is offline", answeredWith("bash: local-ok"))

	// The loaded bot uses the machine again after a new enrollment: the new enrollment changes
	// the stamp, or the bot would keep a lease on the revoked enrollment.
	status, m.token = m.pair(d, m.issue(d))
	if status != http.StatusOK || m.token == "" {
		t.Fatalf("second enrollment returned %d and token %q", status, m.token)
	}
	m.connect(d)
	d.applied(t)
	if got := taskTurn(t, d, session, `printf 'machine-again'`); !strings.Contains(got, "machine-again") {
		t.Fatalf("remote command after a new enrollment returned %q", got)
	}
}

// reloadBot reloads the extensions of the default bot when it is idle.
func reloadBot(t *testing.T, d *dex) {
	t.Helper()
	eventually(t, "reload of the default bot", func() (bool, any) {
		status, body := d.call(t, http.MethodPost, "/api/bots/default/integrations/reload", nil)
		if status != http.StatusOK && status != http.StatusConflict {
			t.Fatalf("reload: %d %s", status, body)
		}
		return status == http.StatusOK, body
	})
}

// useMachine makes the machine the environment of the default bot and waits for the bot to load it.
func useMachine(t *testing.T, d *dex, m *machine, model string) {
	t.Helper()
	var bot apiBot
	d.must(t, http.MethodGet, "/api/bots/default", nil, &bot)
	bot.Provider, bot.ModelID, bot.ThinkingLevel, bot.RunnerID = "faux", model, "off", m.envID
	d.must(t, http.MethodPut, "/api/bots/default", bot, &bot)
	reloadBot(t, d)
}

// TestUserBotKeepsItsMachineAcrossAReconnect covers a short network failure of a laptop while
// another bot works. The bot must not need a reload for the same machine.
func TestUserBotKeepsItsMachineAcrossAReconnect(t *testing.T) {
	// Given a bot on an enrolled machine, and another bot that runs a long local command.
	d := startDexWith(t, app.Config{DataDir: t.TempDir()})
	m := enrollMachine(t, d)
	m.connect(d)
	useMachine(t, d, m, "bash")
	session := d.newSession(t).ID
	if got := taskTurn(t, d, session, `printf before > note.txt; cat note.txt`); got != "bash: before" {
		t.Fatalf("turn before the reconnect: %q", got)
	}
	var other apiBot
	d.must(t, http.MethodPost, "/api/bots", map[string]string{"name": "Busy", "provider": "faux", "modelId": "bash", "thinkingLevel": "off", "runnerId": "local"}, &other, http.StatusCreated)
	var opened sessionInfo
	d.must(t, http.MethodPost, "/api/bots/"+other.ID+"/sessions", nil, &opened, http.StatusCreated)
	busy := botSession{bots: &bots{t: t, d: d}, bot: other.ID, id: opened.ID}
	busy.send(`while :; do sleep 1; done`)
	busy.watch().until(t, "the other bot runs", func(tr *transcript) bool { return tr.running })

	// When the machine loses its session, a message arrives, and the machine connects again.
	m.disconnect()
	m.waitOnline(d, false)
	id := d.send(t, session, `cat note.txt; printf ' after'`)
	m.connect(d)

	// Then the message runs on the machine while the other bot still works, with no reload pending.
	d.watch(t, session).until(t, "answer to "+id+" after the reconnect", answeredWith("bash: before after"))
	busy.watch().until(t, "the other bot still runs", func(tr *transcript) bool { return tr.running })
	var ext apiExtensionsPending
	d.must(t, http.MethodGet, "/api/bots/default/integrations", nil, &ext)
	if ext.ReloadPending {
		t.Fatal("a reconnect of the same machine made the bot stale")
	}
}

// TestUserBotRecoversItsMachineAfterAnOfflineStart covers a restart of dex while the machine
// is offline. The bot has no MCP server, so only the machine connection can start the recovery.
func TestUserBotRecoversItsMachineAfterAnOfflineStart(t *testing.T) {
	// Given a bot on an enrolled machine with a project skill there, and dex restarts while the machine is offline.
	dataDir := t.TempDir()
	first := startDexWith(t, app.Config{DataDir: dataDir})
	m := enrollMachine(t, first)
	m.connect(first)
	useMachine(t, first, m, "section")
	writeFile(t, filepath.Join(m.botWorkspace("default"), ".agents", "skills", "laptop-only", "SKILL.md"), "---\nname: laptop-only\ndescription: Skill on the laptop\n---\n")
	m.disconnect()
	first.stop()
	d := startDexWith(t, app.Config{DataDir: dataDir})
	session := d.newSession(t).ID
	id := d.send(t, session, "skills")
	// The extensions of a bot that is not open need its machine, so success means the bot opened.
	eventually(t, "the bot opens without its machine", func() (bool, any) {
		status, body := d.call(t, http.MethodGet, "/api/bots/default/integrations", nil)
		return status == http.StatusOK, body
	})

	// When the machine connects again, with no other input.
	m.connect(d)

	// Then the bot reloads on its own, and the waiting message answers with the skill from the
	// machine. A reload ends the event streams of the bot, so watch after it.
	d.applied(t)
	d.watch(t, session).until(t, "answer to "+id, answeredWith("laptop-only"))
}
