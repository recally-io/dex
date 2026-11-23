package app_test

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/durable/env/node"
	"github.com/coder/websocket"
	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/execenv/runner"
)

// machine is one enrolled remote machine. It connects with its token, as the dex-agent does.
type machine struct {
	t     *testing.T
	envID string
	token string
	state string
	ws    *websocket.Conn
	done  chan struct{}
}

// enrollMachine creates a machine environment in d and enrolls a machine for it.
func enrollMachine(t *testing.T, d *dex) *machine {
	t.Helper()
	var created apiEnvironment
	d.must(t, http.MethodPost, "/api/settings/environments", map[string]any{"name": "Laptop", "kind": "runner"}, &created, http.StatusCreated)
	var issued struct {
		Code string `json:"code"`
	}
	d.must(t, http.MethodPost, "/api/settings/environments/"+created.ID+"/runner-pairing", nil, &issued)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, d.srv.URL+"/machine/"+created.ID+"/pair", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+issued.Code)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&body); err != nil || body.Token == "" {
		t.Fatalf("enrollment returned %d: %v", res.StatusCode, err)
	}
	m := &machine{t: t, envID: created.ID, token: body.Token, state: t.TempDir()}
	if err := os.Mkdir(filepath.Join(m.state, "workspace"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.disconnect)
	return m
}

// connect opens a session to d with the machine token and waits until d reports it online.
func (m *machine) connect(d *dex) {
	m.t.Helper()
	u := strings.Replace(d.srv.URL+"/machine/"+m.envID+"/connect", "http://", "ws://", 1)
	header := http.Header{"Authorization": []string{"Bearer " + m.token}}
	ws, res, err := websocket.Dial(m.t.Context(), u, &websocket.DialOptions{HTTPHeader: header, Subprotocols: []string{runner.Version}})
	if res != nil && res.Body != nil {
		_ = res.Body.Close()
	}
	if err != nil {
		m.t.Fatalf("connect the machine: %v", err)
	}
	m.ws, m.done = ws, make(chan struct{})
	go func(done chan struct{}) {
		defer close(done)
		_ = runner.Serve(m.t.Context(), ws, node.NewNodeExecutionEnv(node.NodeExecutionEnvOptions{Cwd: filepath.Join(m.state, "workspace")}))
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
	eventually(m.t, "machine online="+map[bool]string{true: "true", false: "false"}[online], func() (bool, any) {
		var probe apiEnvironmentTestResult
		d.must(m.t, http.MethodPost, "/api/settings/environments/"+m.envID+"/test", nil, &probe)
		return probe.OK == online, probe
	})
}

// botWorkspace is the workspace of a bot on the machine.
func (m *machine) botWorkspace(botID string) string {
	return filepath.Join(m.state, "workspaces", botID, "workspace")
}

// useMachine makes the machine the environment of the default bot and waits for the bot to load it.
func useMachine(t *testing.T, d *dex, m *machine, model string) {
	t.Helper()
	var bot apiAgent
	d.must(t, http.MethodGet, "/api/agents/default", nil, &bot)
	bot.Provider, bot.ModelID, bot.ThinkingLevel, bot.EnvironmentID = "faux", model, "off", m.envID
	d.must(t, http.MethodPut, "/api/agents/default", bot, &bot)
	eventually(t, "reload of the default bot", func() (bool, any) {
		status, body := d.call(t, http.MethodPost, "/api/agents/default/extensions/reload", nil)
		if status != http.StatusOK && status != http.StatusConflict {
			t.Fatalf("reload: %d %s", status, body)
		}
		return status == http.StatusOK, body
	})
}

// TestUserBotKeepsItsMachineAcrossAReconnect covers a short network failure of a laptop while
// another bot works. The bot must not need a reload for the same machine.
func TestUserBotKeepsItsMachineAcrossAReconnect(t *testing.T) {
	// Given a bot on an enrolled machine, and another bot that runs a long local command.
	d := startDexWith(t, app.Config{DataDir: t.TempDir()})
	m := enrollMachine(t, d)
	m.connect(d)
	useMachine(t, d, m, "bash")
	chat := d.newSession(t).ID
	if got := taskTurn(t, d, chat, `printf before > note.txt; cat note.txt`); got != "bash: before" {
		t.Fatalf("turn before the reconnect: %q", got)
	}
	var other apiAgent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "Busy", "provider": "faux", "modelId": "bash", "thinkingLevel": "off", "environmentId": "local"}, &other, http.StatusCreated)
	var session sessionInfo
	d.must(t, http.MethodPost, "/api/agents/"+other.ID+"/chats", nil, &session, http.StatusCreated)
	busy := botChat{bots: &bots{t: t, d: d}, agent: other.ID, id: session.ID}
	busy.send(`while :; do sleep 1; done`)
	busy.watch().until(t, "the other bot runs", func(tr *transcript) bool { return tr.running })

	// When the machine loses its session, a message arrives, and the machine connects again.
	m.disconnect()
	m.waitOnline(d, false)
	id := d.send(t, chat, `cat note.txt; printf ' after'`)
	m.connect(d)

	// Then the message runs on the machine while the other bot still works, with no reload pending.
	d.watch(t, chat).until(t, "answer to "+id+" after the reconnect", answeredWith("bash: before after"))
	busy.watch().until(t, "the other bot still runs", func(tr *transcript) bool { return tr.running })
	var ext apiExtensionsPending
	d.must(t, http.MethodGet, "/api/agents/default/extensions", nil, &ext)
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
	chat := d.newSession(t).ID
	id := d.send(t, chat, "skills")
	// The extensions of a bot that is not open need its machine, so success means the bot opened.
	eventually(t, "the bot opens without its machine", func() (bool, any) {
		status, body := d.call(t, http.MethodGet, "/api/agents/default/extensions", nil)
		return status == http.StatusOK, body
	})

	// When the machine connects again, with no other input.
	m.connect(d)

	// Then the bot reloads on its own, and the waiting message answers with the skill from the
	// machine. A reload ends the event streams of the bot, so watch after it.
	d.applied(t)
	d.watch(t, chat).until(t, "answer to "+id, answeredWith("laptop-only"))
}
