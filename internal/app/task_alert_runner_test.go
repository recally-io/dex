package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/durable/env/node"
	"github.com/coder/websocket"
	"github.com/recally-io/dex/internal/runner/outbound"
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
	d.must(t, http.MethodPut, "/api/plugins/github/webhooks/10", map[string]string{"secret": secret, "issueAgentId": "default"}, nil, http.StatusNoContent)
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
	dialCtx, cancelDial := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancelDial()
	url := strings.Replace(d.srv.URL, "http://", "ws://", 1) + "/machine/" + machine.ID + "/connect"
	ws, res, err := websocket.Dial(dialCtx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + enrolled.Token}}, Subprotocols: []string{outbound.Version}})
	if res != nil && res.Body != nil {
		_ = res.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	cancelDial()
	local := node.NewNodeExecutionEnv(node.NodeExecutionEnvOptions{Cwd: t.TempDir()})
	runnerDone := make(chan error, 1)
	go func() { runnerDone <- outbound.Serve(t.Context(), ws, local) }()
	defer func() {
		_ = ws.CloseNow()
		if err := <-runnerDone; err != nil {
			t.Errorf("runner: %v", err)
		}
	}()

	// Then Main settles the saved alert. Open the transcript after settlement so
	// this test does not depend on live events during the cold start.
	var main chatInfo
	d.must(t, http.MethodGet, "/api/agents/default/chats/main", nil, &main)
	conn := sessionDB(t, d)
	var alertID string
	if err := conn.QueryRowContext(t.Context(), `SELECT s.id FROM submissions s JOIN task_inputs i ON i.submission_id = s.id WHERE s.kind = 'task_wake'`).Scan(&alertID); err != nil {
		t.Fatal(err)
	}
	waitResultSubmission(t, conn, alertID, "done")
	deliverGitHub(t, d, secret, "issue-1-again", "issues", issue)
	if got := count(t, conn, `SELECT count(*) FROM submissions WHERE kind = 'task_wake'`); got != 1 {
		t.Fatalf("Main has %d Task alerts, want one", got)
	}
	w := d.watch(t, main.ID)
	w.until(t, "the settled transcript snapshot", func(tr *transcript) bool { return tr.snapshots > 0 })
	answers := 0
	for _, it := range w.tr.items {
		if it.Kind == "assistant" && strings.HasPrefix(it.Text, "context: ") && strings.Contains(it.Text, `\"reason\":\"assigned\"`) {
			answers++
		}
	}
	if answers != 1 {
		t.Fatalf("Main answered the Task alert %d times, want one: %+v", answers, w.tr.items)
	}
	t.Logf("artifact: Main items=%+v", w.tr.items)
}
