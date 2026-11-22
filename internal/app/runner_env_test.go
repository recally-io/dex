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
	"github.com/recally-io/dex/internal/execenv/runner"
)

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
	var created apiEnvironment
	d.must(t, http.MethodPost, "/api/settings/environments", map[string]any{"name": "Debian VM", "kind": "runner"}, &created, http.StatusCreated)
	settingsPath := "/api/settings/environments/" + created.ID
	machinePath := d.srv.URL + "/machine/" + created.ID
	var issued struct {
		Code string `json:"code"`
	}
	d.must(t, http.MethodPost, settingsPath+"/runner-pairing", nil, &issued)

	enroll := func(code string) (int, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, machinePath+"/pair", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+code)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var body struct {
			Token string `json:"token"`
		}
		_ = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&body)
		return res.StatusCode, body.Token
	}
	// The code needs no certificate, and a browser request is refused.
	if status, _ := enroll(""); status != http.StatusUnauthorized {
		t.Fatalf("enrollment without a code returned %d", status)
	}
	anonymous, err := http.NewRequestWithContext(ctx, http.MethodPost, machinePath+"/pair", nil)
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
	status, token := enroll(issued.Code)
	if status != http.StatusOK || token == "" {
		t.Fatalf("enrollment returned %d and token %q", status, token)
	}
	if status, _ := enroll(issued.Code); status != http.StatusUnauthorized {
		t.Fatalf("a used code returned %d", status)
	}

	dial := func(token string) (*websocket.Conn, error) {
		u := strings.Replace(machinePath+"/connect", "http://", "ws://", 1)
		header := http.Header{"Authorization": []string{"Bearer " + token}}
		ws, res, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: header, Subprotocols: []string{runner.Version}})
		if res != nil && res.Body != nil {
			_ = res.Body.Close()
		}
		return ws, err
	}
	if ws, err := dial("wrong-token-that-is-long-enough"); err == nil {
		_ = ws.CloseNow()
		t.Fatal("a wrong token connected")
	}
	ws, err := dial(token)
	if err != nil {
		t.Fatalf("connect with the enrolled token: %v", err)
	}
	defer func() { _ = ws.CloseNow() }()
	machineState := t.TempDir()
	work := filepath.Join(machineState, "workspace")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	stop := make(chan error, 1)
	go func() {
		stop <- runner.Serve(ctx, ws, node.NewNodeExecutionEnv(node.NodeExecutionEnvOptions{Cwd: work}))
	}()
	var probe apiEnvironmentTestResult
	for !probe.OK {
		d.must(t, http.MethodPost, settingsPath+"/test", nil, &probe)
		if probe.OK {
			break
		}
		select {
		case err := <-stop:
			t.Fatalf("machine stopped early: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	// Both bots remain local by default and explicitly select the same runner for a chat.
	d.setModel(t, "bash")
	var second apiAgent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{
		"name": "Second bot", "provider": "faux", "modelId": "bash", "thinkingLevel": "off",
	}, &second, http.StatusCreated)
	newChat := func(agentID, environmentID string) botChat {
		var session sessionInfo
		d.must(t, http.MethodPost, "/api/agents/"+agentID+"/chats", nil, &session, http.StatusCreated)
		d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/%s/chats/%d/environment", agentID, session.ID),
			map[string]string{"environmentId": environmentID}, nil)
		return botChat{bots: &bots{t: t, d: d}, agent: agentID, id: session.ID}
	}
	first, other := newChat("default", created.ID), newChat(second.ID, created.ID)
	first.send(`printf 'first' > note.txt; cat note.txt`)
	first.watch().until(t, "first bot writes its own file", answeredWith("bash: first"))
	other.send(`test ! -e note.txt && printf 'second' > note.txt && cat note.txt`)
	other.watch().until(t, "second bot writes its own file", answeredWith("bash: second"))
	for botID, want := range map[string]string{"default": "first", second.ID: "second"} {
		file := filepath.Join(machineState, "workspaces", botID, "workspace", "note.txt")
		got, err := os.ReadFile(file)
		if err != nil || string(got) != want {
			t.Fatalf("bot %s file = %q, want %q (err %v)", botID, got, want, err)
		}
	}

	// Changing the bot default still connects to the machine and survives an extension reload.
	var bot apiAgent
	d.must(t, http.MethodGet, "/api/agents/default", nil, &bot)
	bot.EnvironmentID = created.ID
	d.must(t, http.MethodPut, "/api/agents/default", bot, &bot)
	reload := func() {
		t.Helper()
		eventually(t, "reload idle bot", func() (bool, any) {
			status, body := d.call(t, http.MethodPost, "/api/agents/default/extensions/reload", nil)
			if status != http.StatusOK && status != http.StatusConflict {
				t.Fatalf("reload: %d %s", status, body)
			}
			return status == http.StatusOK, body
		})
	}
	for range 2 {
		reload()
	}
	chat := d.newSession(t).ID
	if got := taskTurn(t, d, chat, `cat note.txt`); got != "bash: first" {
		t.Fatalf("default runner after reloading the bot: %q", got)
	}

	d.must(t, http.MethodDelete, settingsPath+"/runner-pairing", nil, nil)
	select {
	case <-stop:
	case <-ctx.Done():
		t.Fatal("revocation did not close the machine")
	}
	if ws, err := dial(token); err == nil {
		_ = ws.CloseNow()
		t.Fatal("a revoked token reconnected")
	}

	// An offline runner remains listed, but an explicit session never runs on local.
	var listed struct {
		Environments []apiSessionEnvironment `json:"environments"`
	}
	d.must(t, http.MethodGet, "/api/agents/"+second.ID+"/environments", nil, &listed)
	if len(listed.Environments) != 2 || listed.Environments[1].ID != created.ID || listed.Environments[1].Status != "offline" {
		t.Fatalf("offline runner: %+v", listed.Environments)
	}
	other.send(`printf 'offline-fallback' > offline.txt`)
	other.watch().until(t, "offline runner refuses the tool", func(tr *transcript) bool {
		last := lastItem(tr)
		return !tr.running && last.Kind == "assistant" && (strings.Contains(last.Text, "offline") || strings.Contains(last.Text, "selected environment changed"))
	})
	// The unavailable default does not prevent an explicit local chat in that bot.
	reload()
	local := newChat("default", "local")
	local.send(`test ! -e note.txt && printf 'local-ok'`)
	local.watch().until(t, "explicit local when its bot runner is offline", answeredWith("bash: local-ok"))

	// The loaded bot uses the machine again after a new enrollment: the new session must not reuse
	// the stamp of the revoked session, or the bot would keep its closed connection.
	d.must(t, http.MethodPost, settingsPath+"/runner-pairing", nil, &issued)
	status, token = enroll(issued.Code)
	if status != http.StatusOK || token == "" {
		t.Fatalf("second enrollment returned %d and token %q", status, token)
	}
	ws2, err := dial(token)
	if err != nil {
		t.Fatalf("connect after the second enrollment: %v", err)
	}
	defer func() { _ = ws2.CloseNow() }()
	go func() {
		_ = runner.Serve(ctx, ws2, node.NewNodeExecutionEnv(node.NodeExecutionEnvOptions{Cwd: work}))
	}()
	for probe = (apiEnvironmentTestResult{}); !probe.OK; {
		d.must(t, http.MethodPost, settingsPath+"/test", nil, &probe)
		if probe.OK {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	// The new session changes the stamp, so dex reloads the idle bot on its own.
	d.applied(t)
	if got := taskTurn(t, d, chat, `printf 'machine-again'`); !strings.Contains(got, "machine-again") {
		t.Fatalf("remote command after a new enrollment returned %q", got)
	}
}
