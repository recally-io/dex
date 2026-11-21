package app_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/api"
)

func TestUserFollowsAnEmailThreadOnOneTask(t *testing.T) {
	t.Parallel()
	// Given the owner turns on the email plugin, which is off by default.
	dataDir := t.TempDir()
	d := startDex(t, dataDir, "")
	secret := strings.Repeat("m", 40)
	source := map[string]any{"secret": secret, "taskAgentId": "default", "holdUnmatched": true}
	d.must(t, http.MethodPut, "/api/plugins/email/sources/mail", source, nil, http.StatusNotFound)
	d.must(t, http.MethodPut, "/api/settings/plugins/email", map[string]bool{"enabled": true}, nil, http.StatusNoContent)
	d.stop()
	d = startDex(t, dataDir, "")
	d.must(t, http.MethodPut, "/api/plugins/email/sources/mail", source, nil, http.StatusNoContent)
	deliver := func(delivery, raw string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, d.srv.URL+"/hooks/email/mail", strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(raw))
		req.Header.Set("X-Dex-Delivery", delivery)
		req.Header.Set("X-Dex-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		res, err := d.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusNoContent {
			t.Fatalf("delivery %s: %d", delivery, res.StatusCode)
		}
	}
	email := func(id, refs, from, subject, text string) string {
		header := "Message-ID: <" + id + ">\r\nFrom: " + from + "\r\nSubject: " + subject + "\r\nDate: Fri, 21 Nov 2026 03:00:00 +0000\r\n"
		if refs != "" {
			header += "References: " + refs + "\r\n"
		}
		return header + "\r\n" + text + "\r\n"
	}

	// When a selected address receives a new thread, a reply, and an unrelated thread.
	deliver("1", email("a1@x", "", "Ann <ann@example.com>", "Contract review", "Please check section 2."))
	deliver("2", email("a2@x", "<a1@x>", "Owner <owner@example.com>", "Re: Contract review", "I approve. Close the Task."))
	deliver("2-retry", email("a2@x", "<a1@x>", "Owner <owner@example.com>", "Re: Contract review", "I approve. Close the Task."))
	deliver("3", email("c1@x", "", "Bob <bob@example.com>", "Invoice", "Invoice attached."))
	// A message that references both threads, and a reply to a thread Dex never saw.
	deliver("4", email("d1@x", "<a1@x> <c1@x>", "Eve <eve@example.com>", "Both", "Mixed."))
	deliver("5", email("e2@x", "<e1@x>", "Dan <dan@example.com>", "Re: Old thread", "Late reply."))

	// Then each new thread opens one Task, and the reply updates the first Task once.
	var list api.ListWorkTasks200JSONResponse
	d.must(t, http.MethodGet, "/api/inbox/tasks", nil, &list)
	titles := []string{}
	for _, task := range list.Tasks {
		titles = append(titles, task.Title)
	}
	slices.Sort(titles)
	if !slices.Equal(titles, []string{"Contract review", "Invoice"}) {
		t.Fatalf("each new thread must open one Task: %q", titles)
	}
	contract := list.Tasks[slices.IndexFunc(list.Tasks, func(task api.WorkTask) bool { return task.Title == "Contract review" })]
	var detail api.GetWorkTask200JSONResponse
	d.must(t, http.MethodGet, "/api/agents/default/inbox/tasks/"+contract.Id, nil, &detail)
	t.Logf("artifact: entries=%+v", detail.Entries)
	if len(detail.Entries) != 2 || !strings.Contains(detail.Entries[0].Content, "I approve") || detail.Task.Lifecycle != api.WorkTaskLifecycleOpen {
		t.Fatalf("the reply must update the open Task once: %+v %+v", detail.Task, detail.Entries)
	}

	// The ambiguous message and the unknown reply wait for the owner.
	var held api.ListHeldEvents200JSONResponse
	d.must(t, http.MethodGet, "/api/inbox/held-events", nil, &held)
	reasons := map[api.HeldEventReason]int{}
	for _, e := range held.Events {
		reasons[e.Reason]++
	}
	if len(held.Events) != 2 || reasons[api.Conflict] != 1 || reasons[api.Unmatched] != 1 {
		t.Fatalf("Dex must hold the conflict and the unknown reply: %+v", held.Events)
	}
	// A forged From header does not make the owner an author.
	alerts := pendingTaskAlerts(t, d.dataDir(t))
	if len(alerts) != 2 || strings.Contains(strings.Join(alerts, "\n"), "owner@example.com") {
		t.Fatalf("Main must get one metadata-only alert per new Task: %q", alerts)
	}
	for _, alert := range alerts {
		if !strings.Contains(alert, "Author class: SOURCE") {
			t.Fatalf("an email alert must carry the source class: %q", alert)
		}
	}
}
