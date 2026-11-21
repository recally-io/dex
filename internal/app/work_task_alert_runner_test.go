package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/durable/env/node"
	"github.com/coder/websocket"
	"github.com/recally-io/dex/internal/execenv/runner"
)

func TestUserMainRunsAPendingTaskAlertOnceWhenTheRunnerReturns(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "context")
	// Given a bot on a paired runner that is not connected.
	var machine apiEnvironment
	d.must(t, http.MethodPost, "/api/settings/environments", map[string]string{"name": "Runner", "kind": "runner"}, &machine, http.StatusCreated)
	var issued struct {
		Code string `json:"code"`
	}
	d.must(t, http.MethodPost, "/api/settings/environments/"+machine.ID+"/runner-pairing", nil, &issued)
	pair, err := http.NewRequestWithContext(t.Context(), http.MethodPost, d.srv.URL+"/machine/"+machine.ID+"/pair", nil)
	if err != nil {
		t.Fatal(err)
	}
	pair.Header.Set("Authorization", "Bearer "+issued.Code)
	var enrolled struct {
		Token string `json:"token"`
	}
	paired, err := http.DefaultClient.Do(pair)
	if err != nil {
		t.Fatal(err)
	}
	err = json.NewDecoder(paired.Body).Decode(&enrolled)
	_ = paired.Body.Close()
	if err != nil || paired.StatusCode != http.StatusOK || enrolled.Token == "" {
		t.Fatalf("pairing: %d, %v", paired.StatusCode, err)
	}
	var bot apiAgent
	d.must(t, http.MethodGet, "/api/agents/default", nil, &bot)
	bot.EnvironmentID = machine.ID
	d.must(t, http.MethodPut, "/api/agents/default", bot, nil)
	secret := strings.Repeat("r", 40)
	d.must(t, http.MethodPut, "/api/settings/github-webhooks/10", map[string]string{"secret": secret, "issueAgentId": "default"}, nil, http.StatusNoContent)
	issue := `{"action":"opened","repository":{"id":10},"issue":{"id":17,"number":1,"user":{"login":"maintainer"},"author_association":"MEMBER","title":"Fix login","html_url":"https://github.com/o/r/issues/1","created_at":"2026-11-21T02:00:00Z"}}`

	// When an Issue opens a Task while the runner is away, and the controller restarts.
	deliverGitHub(t, d, secret, "issue-1", "issues", issue)
	if got := pendingTaskAlerts(t, dir); len(got) != 1 {
		t.Fatalf("Main must keep one pending Task alert: %q", got)
	}
	d.stop()
	d = startDex(t, dir, "")
	if got := pendingTaskAlerts(t, dir); len(got) != 1 {
		t.Fatalf("a restart must keep the pending Task alert: %q", got)
	}

	// And the runner connects.
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	url := strings.Replace(d.srv.URL, "http://", "ws://", 1) + "/machine/" + machine.ID + "/connect"
	ws, res, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + enrolled.Token}}, Subprotocols: []string{runner.Version}})
	if res != nil && res.Body != nil {
		_ = res.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.CloseNow() }()
	go func() {
		_ = runner.Serve(ctx, ws, node.NewNodeExecutionEnv(node.NodeExecutionEnvOptions{Cwd: t.TempDir()}))
	}()

	// Then Main runs the Task alert exactly once.
	var main chatInfo
	d.must(t, http.MethodGet, "/api/agents/default/chats/main", nil, &main)
	w := d.watch(t, main.ID)
	answers := func(tr *transcript) int {
		return len(slices.DeleteFunc(slices.Clone(tr.items), func(it item) bool {
			return !strings.HasPrefix(it.Text, "context: ") || !strings.Contains(it.Text, "[Task event: issue.opened]")
		}))
	}
	w.until(t, "the answer to the Task alert", func(tr *transcript) bool { return answers(tr) > 0 && !tr.running })
	deliverGitHub(t, d, secret, "issue-1-again", "issues", issue)
	d.awaitIdle(t, main.ID)
	t.Logf("artifact: Main items=%+v", w.tr.items)
	if n := answers(&w.tr); n != 1 {
		t.Fatalf("Main must answer the Task alert once, got %d", n)
	}
	if got := pendingTaskAlerts(t, dir); len(got) != 0 {
		t.Fatalf("a Task alert stays pending after the runner returned: %q", got)
	}
}
