package app_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
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
	work := t.TempDir()
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
	var bot apiAgent
	d.must(t, http.MethodGet, "/api/agents/default", nil, &bot)
	bot.Provider = "faux"
	bot.ModelID = "bash"
	bot.ThinkingLevel = "off"
	bot.EnvironmentID = created.ID
	d.must(t, http.MethodPut, "/api/agents/default", bot, &bot)
	chat := d.newSession(t).ID
	if got := taskTurn(t, d, chat, `printf 'machine-ok'`); !strings.Contains(got, "machine-ok") {
		t.Fatalf("remote command returned %q", got)
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
}
