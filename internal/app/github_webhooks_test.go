package app_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/worktask"
	"github.com/recally-io/dex/internal/worktask/github"
)

func TestUserReceivesOnlySignedGitHubWebhookDeliveries(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	adminToken := strings.Repeat("a", 40)
	secret := strings.Repeat("b", 40)
	d := startDexWith(t, app.Config{DataDir: dir, Token: adminToken})
	const settings = "/api/settings/github-webhooks/10"
	const hook = "/hooks/github/10"
	const taskPath = "/api/agents/default/inbox/tasks/"
	const payload = `{"action":"submitted","repository":{"id":10},"pull_request":{"number":2},"review":{"id":71,"state":"approved","body":"Looks good.","commit_id":"abc","submitted_at":"2026-11-21T03:00:00Z"}}`
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	request := func(method, path, body, token, signed string, status int) []byte {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, d.srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if strings.HasPrefix(path, "/hooks/") {
			req.Header.Set("X-GitHub-Delivery", "delivery-1")
			req.Header.Set("X-GitHub-Event", "pull_request_review")
			req.Header.Set("X-Hub-Signature-256", signed)
		}
		response, err := d.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != status {
			t.Fatalf("%s %s: status=%d want %d, %s, %v", method, path, response.StatusCode, status, raw, err)
		}
		if strings.Contains(string(raw), secret) {
			t.Fatal("response contained the Webhook secret")
		}
		return raw
	}

	// Given an unconfigured repository, only the admin can enable its Webhook.
	settingsBody := `{"secret":"` + secret + `"}`
	request(http.MethodPost, hook, payload, "", signature, http.StatusNotFound)
	request(http.MethodPut, settings, settingsBody, "", "", http.StatusUnauthorized)
	request(http.MethodPut, settings, settingsBody, adminToken, "", http.StatusNoContent)
	var task api.WorkTask
	if err := json.Unmarshal(request(http.MethodPost, strings.TrimSuffix(taskPath, "/"), `{"title":"Review the change"}`, adminToken, "", http.StatusCreated), &task); err != nil {
		t.Fatal(err)
	}
	conn, err := db.Open(t.Context(), filepath.Join(dir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	store := worktask.New(conn)
	if err := store.LinkObject(t.Context(), "default", task.Id, github.Namespace(10), github.PullRequest(2)); err != nil {
		t.Fatal(err)
	}

	// When GitHub signs the same review twice, neither delivery needs an admin token.
	request(http.MethodPost, hook, payload, adminToken, "", http.StatusUnauthorized)
	request(http.MethodPost, hook, payload+" ", "", signature, http.StatusUnauthorized)
	request(http.MethodPost, hook, payload, "", signature, http.StatusNoContent)
	request(http.MethodPost, hook, payload, "", signature, http.StatusNoContent)

	// Then one timeline entry is visible, and disabling the Webhook blocks new deliveries.
	var detail api.GetWorkTask200JSONResponse
	if err := json.Unmarshal(request(http.MethodGet, taskPath+task.Id, "", adminToken, "", http.StatusOK), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Entries) != 1 || detail.Entries[0].Content != "GitHub review: approved\n\nLooks good." {
		t.Fatalf("unexpected timeline: %+v", detail.Entries)
	}
	request(http.MethodDelete, settings, "", adminToken, "", http.StatusNoContent)
	request(http.MethodPost, hook, payload, "", signature, http.StatusNotFound)
}
