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
	tasksvc "github.com/recally-io/dex/internal/task"
	"github.com/recally-io/dex/plugins/github"
)

func TestUserReceivesOnlySignedGitHubWebhookDeliveries(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	adminToken := strings.Repeat("a", 40)
	secret := strings.Repeat("b", 40)
	d := startDexWith(t, app.Config{DataDir: dir, Token: adminToken})
	const settings = "/api/plugins/github/webhooks/10"
	const hook = "/hooks/github/10"
	const taskPath = "/api/tasks/"
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
			delivery := "delivery-1"
			if strings.Contains(body, `"id":74`) {
				delivery = "delivery-2"
			}
			req.Header.Set("X-GitHub-Delivery", delivery)
			kind := "pull_request_review"
			if path == "/hooks/github/11" {
				kind = "issues"
			}
			req.Header.Set("X-GitHub-Event", kind)
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
	var task api.TaskSummary
	if err := json.Unmarshal(request(http.MethodPost, strings.TrimSuffix(taskPath, "/"), `{"assigneeBotId":"default","title":"Review the change"}`, adminToken, "", http.StatusCreated), &task); err != nil {
		t.Fatal(err)
	}
	conn, err := db.Open(t.Context(), filepath.Join(dir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	store := tasksvc.New(conn, nil)
	if err := store.LinkObject(t.Context(), "default", task.Id, github.Namespace(10), github.PullRequest(2)); err != nil {
		t.Fatal(err)
	}

	// When GitHub signs the same review twice, neither delivery needs an admin token.
	request(http.MethodPost, hook, payload, adminToken, "", http.StatusUnauthorized)
	request(http.MethodPost, hook, payload+" ", "", signature, http.StatusUnauthorized)
	request(http.MethodPost, hook, payload, "", signature, http.StatusNoContent)
	request(http.MethodPost, hook, payload, "", signature, http.StatusNoContent)

	// Then one timeline entry is visible, and disabling the Webhook blocks new deliveries.
	var detail api.TaskDetail
	if err := json.Unmarshal(request(http.MethodGet, taskPath+task.Id, "", adminToken, "", http.StatusOK), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Entries) != 3 || detail.Entries[2].Content != "GitHub review: approved\n\nLooks good." || detail.Entries[2].EventId == nil {
		t.Fatalf("unexpected timeline: %+v", detail.Entries)
	}
	request(http.MethodDelete, settings, "", adminToken, "", http.StatusNoContent)
	request(http.MethodPost, hook, payload, "", signature, http.StatusNotFound)

	const issueSettings = "/api/plugins/github/webhooks/11"
	const issueHook = "/hooks/github/11"
	const issue = `{"action":"opened","repository":{"id":11},"issue":{"id":73,"number":3,"user":{"login":"maintainer"},"author_association":"MEMBER","title":"Login fails","body":"Please check the logs.", "created_at":"2026-11-21T03:00:00Z"}}`
	mac.Reset()
	_, _ = mac.Write([]byte(issue))
	issueSignature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	listTasks := func(want int) []api.TaskSummary {
		t.Helper()
		var list api.TaskList
		if err := json.Unmarshal(request(http.MethodGet, "/api/tasks", "", adminToken, "", http.StatusOK), &list); err != nil {
			t.Fatal(err)
		}
		if len(list.Tasks) != want {
			t.Fatalf("Task list has %d tasks, want %d", len(list.Tasks), want)
		}
		return list.Tasks
	}

	// A signed Issue needs an explicit creation destination.
	request(http.MethodPut, issueSettings, settingsBody, adminToken, "", http.StatusNoContent)
	request(http.MethodPost, issueHook, issue, "", issueSignature, http.StatusNoContent)
	listTasks(1)
	request(http.MethodPut, issueSettings, `{"secret":"`+secret+`","issueBotId":"missing"}`, adminToken, "", http.StatusBadRequest)
	request(http.MethodPut, issueSettings, `{"secret":"`+secret+`","issueBotId":"default"}`, adminToken, "", http.StatusNoContent)
	outside := strings.ReplaceAll(issue, `"MEMBER"`, `"NONE"`)
	mac.Reset()
	_, _ = mac.Write([]byte(outside))
	request(http.MethodPost, issueHook, outside, "", "sha256="+hex.EncodeToString(mac.Sum(nil)), http.StatusNoContent)
	listTasks(1)
	// Use a second receipt and change identity for the maintainer's Issue.
	trusted := strings.ReplaceAll(issue, `"id":73`, `"id":74`) + strings.Repeat(" ", 300<<10)
	request(http.MethodPost, issueHook, trusted+strings.Repeat(" ", 2<<20), "", "", http.StatusRequestEntityTooLarge)
	mac.Reset()
	_, _ = mac.Write([]byte(trusted))
	trustedSignature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	request(http.MethodPost, issueHook, trusted, "", trustedSignature, http.StatusNoContent)
	request(http.MethodPost, issueHook, trusted, "", trustedSignature, http.StatusNoContent)
	var created api.TaskSummary
	for _, row := range listTasks(2) {
		if row.Title == "GitHub issue #3" {
			created = row
		}
	}
	if created.Id == "" {
		t.Fatal("Issue did not create a Task")
	}
	result, err := store.Accept(t.Context(), tasksvc.Event{
		Namespace: github.Namespace(11), ReceiptKey: "follow-up", Type: "issue.follow_up",
		Objects: []tasksvc.Object{{Type: "issue", Key: "3"}}, Content: "Follow-up", OccurredAt: 1,
	})
	if err != nil || result.TaskID != created.Id {
		t.Fatalf("Issue link did not route the follow-up: %+v, %v", result, err)
	}
}
