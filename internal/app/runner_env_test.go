package app_test

import (
	"context"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/durable/env/node"
	"github.com/coder/websocket"
	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/execenv/runner"
	"github.com/recally-io/dex/internal/execenv/runnerproto"
)

func TestUserPairsRunnerAssignsBotAndRevokesDevice(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	a, err := app.Start(ctx, app.Config{DataDir: t.TempDir(), Home: t.TempDir(), FakeModel: true, Token: "test-admin"})
	if err != nil {
		t.Fatal(err)
	}
	d := &dex{app: a, srv: httptest.NewTLSServer(a.Handler())}
	t.Cleanup(d.stop)
	origin, err := url.Parse(d.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	d.srv.Client().Jar = jar
	d.must(t, http.MethodPost, "/api/session", map[string]string{"token": "test-admin"}, nil, http.StatusNoContent)
	var created apiEnvironment
	d.must(t, http.MethodPost, "/api/settings/environments", map[string]any{"name": "Linux runner", "kind": "runner"}, &created, http.StatusCreated)
	settingsPath := "/api/settings/environments/" + created.ID
	devicePath := "/api/runner/" + created.ID
	var code struct {
		Code string `json:"code"`
	}
	d.must(t, http.MethodPost, settingsPath+"/runner-pairing", nil, &code)
	request := func(method, path, token string, status int) []byte {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, d.srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		response, err := d.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != status {
			t.Fatalf("%s %s returned %d: %s (want %d)", method, path, response.StatusCode, body, status)
		}
		return body
	}
	var credential struct {
		Credential string `json:"credential"`
	}
	if err := json.Unmarshal(request(http.MethodPost, devicePath+"/pair", code.Code, http.StatusOK), &credential); err != nil {
		t.Fatal(err)
	}
	request(http.MethodPost, devicePath+"/pair", code.Code, http.StatusUnauthorized)
	request(http.MethodGet, "/api/settings/environments", credential.Credential, http.StatusUnauthorized)
	// Connect the target through the public device route. The browser cookie is not needed.
	ws, _, err := websocket.Dial(ctx, "wss://"+origin.Host+devicePath+"/connect", &websocket.DialOptions{HTTPClient: &http.Client{Transport: d.srv.Client().Transport}, Subprotocols: []string{runnerproto.Version}, HTTPHeader: http.Header{"Authorization": []string{"Bearer " + credential.Credential}}}) //nolint:bodyclose
	if err != nil {
		t.Fatal(err)
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
			t.Fatalf("runner stopped early: %v", err)
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
	if got := taskTurn(t, d, chat, `printf 'runner-ok'`); !strings.Contains(got, "runner-ok") {
		t.Fatalf("remote command returned %q", got)
	}
	d.must(t, http.MethodDelete, settingsPath+"/runner-pairing", nil, nil)
	select {
	case <-stop:
	case <-ctx.Done():
		t.Fatal("revocation did not close runner")
	}
	request(http.MethodGet, devicePath+"/connect", credential.Credential, http.StatusUnauthorized)
}
