package app_test

import (
	"bytes"
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

// TestUserKeepsAMachineThatRunsAsTheirAccountToTheBotThatItServes installs a machine for the
// account that uses it. Its work runs as that person, so it holds their home, their files, and
// their credentials: only the bot that the machine is bound to may use it. A session of another
// bot cannot select it, and a selection that an earlier enrollment allowed stops working.
func TestUserKeepsAMachineThatRunsAsTheirAccountToTheBotThatItServes(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
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
	d.must(t, http.MethodPost, "/api/settings/environments", map[string]any{"name": "Build box", "kind": "runner"}, &created, http.StatusCreated)
	settingsPath := "/api/settings/environments/" + created.ID
	machinePath := d.srv.URL + "/machine/" + created.ID
	issueCode := func() string {
		t.Helper()
		var issued struct {
			Code string `json:"code"`
		}
		d.must(t, http.MethodPost, settingsPath+"/runner-pairing", nil, &issued)
		return issued.Code
	}
	// enroll sends the enrollment of the install command, with the account that its work runs
	// as. A machine of a dedicated account sends none.
	enroll := func(code, runAsUser string) string {
		t.Helper()
		var body io.Reader
		if runAsUser != "" {
			raw, err := json.Marshal(map[string]string{"runAsUser": runAsUser})
			if err != nil {
				t.Fatal(err)
			}
			body = bytes.NewReader(raw)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, machinePath+"/pair", body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+code)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var issued struct {
			Token string `json:"token"`
		}
		_ = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&issued)
		if res.StatusCode != http.StatusOK || issued.Token == "" {
			t.Fatalf("enrollment returned %d and token %q", res.StatusCode, issued.Token)
		}
		return issued.Token
	}
	machineState := t.TempDir()
	work := filepath.Join(machineState, "workspace")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	// connect enrolls the machine and serves its work until the enrollment ends.
	connect := func(token string) chan error {
		t.Helper()
		u := strings.Replace(machinePath+"/connect", "http://", "ws://", 1)
		ws, res, err := websocket.Dial(ctx, u, &websocket.DialOptions{
			HTTPHeader:   http.Header{"Authorization": []string{"Bearer " + token}},
			Subprotocols: []string{runner.Version},
		})
		if res != nil && res.Body != nil {
			_ = res.Body.Close()
		}
		if err != nil {
			t.Fatalf("connect the machine: %v", err)
		}
		t.Cleanup(func() { _ = ws.CloseNow() })
		stop := make(chan error, 1)
		go func() {
			stop <- runner.Serve(ctx, ws, node.NewNodeExecutionEnv(node.NodeExecutionEnvOptions{Cwd: work}))
		}()
		return stop
	}
	// probe waits until Dex reaches the machine and reports what its work can reach.
	probe := func() apiEnvironmentTestResult {
		t.Helper()
		return probeMachine(t, d, settingsPath)
	}
	d.setModel(t, "bash")
	var second apiAgent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{
		"name": "Second bot", "provider": "faux", "modelId": "bash", "thinkingLevel": "off",
	}, &second, http.StatusCreated)
	newChat := func(agentID, environmentID string) botChat {
		t.Helper()
		var session sessionInfo
		d.must(t, http.MethodPost, "/api/agents/"+agentID+"/chats", nil, &session, http.StatusCreated)
		d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/%s/chats/%d/environment", agentID, session.ID),
			map[string]string{"environmentId": environmentID}, nil)
		return botChat{bots: &bots{t: t, d: d}, agent: agentID, id: session.ID}
	}

	// A machine that a dedicated account serves publishes nothing about a person, and the bot
	// that is not bound to it uses it as it always could.
	stop := connect(enroll(issueCode(), ""))
	if result := probe(); len(result.Warnings) != 0 {
		t.Fatalf("a dedicated account warns about a person: %v", result.Warnings)
	}
	other := newChat(second.ID, created.ID)
	other.send(`printf 'dedicated' > mode.txt; cat mode.txt`)
	other.watch().until(t, "the second bot uses a machine of a dedicated account", answeredWith("bash: dedicated"))

	// The account that uses the machine installs it again for itself.
	d.must(t, http.MethodDelete, settingsPath+"/runner-pairing", nil, nil)
	select {
	case <-stop:
	case <-ctx.Done():
		t.Fatal("revocation did not close the machine")
	}
	connect(enroll(issueCode(), "vaayne"))
	result := probe()
	if joined := strings.Join(result.Warnings, "\n"); !strings.Contains(joined, "runs as vaayne") || !strings.Contains(joined, "credentials") {
		t.Fatalf("the machine does not say what its work can reach: %v", result.Warnings)
	}
	if result.Home == "" || result.Home != os.Getenv("HOME") {
		t.Fatalf("the machine reports the home %q, want the home of the account that runs its work", result.Home)
	}

	// The list of environments of the second bot says why it may not use the machine.
	var listed struct {
		Environments []apiSessionEnvironment `json:"environments"`
	}
	d.must(t, http.MethodGet, "/api/agents/"+second.ID+"/environments", nil, &listed)
	var status string
	for _, item := range listed.Environments {
		if item.ID == created.ID {
			status = item.Status
		}
	}
	if !strings.Contains(status, "unavailable") || !strings.Contains(status, "runs as vaayne") {
		t.Fatalf("the machine is listed for the second bot as %q", status)
	}

	// A selection of that bot does not take effect.
	var fresh sessionInfo
	d.must(t, http.MethodPost, "/api/agents/"+second.ID+"/chats", nil, &fresh, http.StatusCreated)
	code, body := d.call(t, http.MethodPut, fmt.Sprintf("/api/agents/%s/chats/%d/environment", second.ID, fresh.ID), map[string]string{"environmentId": created.ID})
	if code != http.StatusBadRequest || !strings.Contains(body, "runs as vaayne") {
		t.Fatalf("the second bot selected a machine of a person: %d %s", code, body)
	}

	// The chat that selected the machine while it served a dedicated account runs nothing on it.
	other.send(`printf 'personal' > personal.txt`)
	other.watch().until(t, "the machine refuses a bot that it does not serve", func(tr *transcript) bool {
		last := lastItem(tr)
		return !tr.running && last.Kind == "assistant" && !strings.Contains(last.Text, "bash: personal")
	})
	if _, err := os.Stat(filepath.Join(machineState, "workspaces", second.ID, "workspace", "personal.txt")); !os.IsNotExist(err) {
		t.Fatalf("the second bot ran work on a machine of a person (err %v)", err)
	}

	// The bot that the machine serves uses it.
	var bot apiAgent
	d.must(t, http.MethodGet, "/api/agents/default", nil, &bot)
	bot.EnvironmentID = created.ID
	d.must(t, http.MethodPut, "/api/agents/default", bot, &bot)
	own := newChat("default", created.ID)
	own.send(`printf 'mine' > note.txt; cat note.txt`)
	own.watch().until(t, "the bound bot uses the machine of its person", answeredWith("bash: mine"))
	if got, err := os.ReadFile(filepath.Join(machineState, "workspaces", "default", "workspace", "note.txt")); err != nil || string(got) != "mine" {
		t.Fatalf("the bound bot wrote %q on the machine (err %v)", got, err)
	}
}

// probeMachine waits until Dex reaches a machine and returns what the probe reported.
func probeMachine(t *testing.T, d *dex, settingsPath string) apiEnvironmentTestResult {
	t.Helper()
	for {
		var result apiEnvironmentTestResult
		d.must(t, http.MethodPost, settingsPath+"/test", nil, &result)
		if result.OK {
			return result
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}
