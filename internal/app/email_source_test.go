package app_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/recally-io/dex/internal/api"
	tasksvc "github.com/recally-io/dex/internal/task"
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
	var list api.TaskList
	d.must(t, http.MethodGet, "/api/tasks", nil, &list)
	titles := []string{}
	for _, task := range list.Tasks {
		titles = append(titles, task.Title)
	}
	slices.Sort(titles)
	if !slices.Equal(titles, []string{"Contract review", "Invoice"}) {
		t.Fatalf("each new thread must open one Task: %q", titles)
	}
	contract := list.Tasks[slices.IndexFunc(list.Tasks, func(task api.TaskSummary) bool { return task.Title == "Contract review" })]
	var detail api.TaskDetail
	d.must(t, http.MethodGet, "/api/tasks/"+contract.Id, nil, &detail)
	t.Logf("artifact: entries=%+v", detail.Entries)
	if len(detail.Entries) != 2 || !strings.Contains(detail.Entries[1].Content, "I approve") || detail.Task.Status != api.TaskStatusReady {
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
	if len(alerts) != 1 {
		t.Fatalf("Main must get one Task wake for both Tasks: %q", alerts)
	}
	want := []tasksvc.WakeItem{{TaskKey: "T-1", Reason: tasksvc.WakeAssigned}, {TaskKey: "T-2", Reason: tasksvc.WakeAssigned}}
	if items := decodeTaskWake(t, alerts[0]).Items; !slices.Equal(items, want) {
		t.Fatalf("only the new Tasks must wake Main: %+v", items)
	}

	// The owner can also let Dex read a mailbox folder. The API accepts only a TLS
	// address off loopback, and only for a saved source.
	pull := map[string]any{"address": "imaps://imap.example.com", "username": "owner@example.com", "password": "app-password", "folder": "Dex"}
	d.must(t, http.MethodPut, "/api/plugins/email/sources/missing/imap", pull, nil, http.StatusNotFound)
	d.must(t, http.MethodPut, "/api/plugins/email/sources/mail/imap", map[string]any{"address": "imap://imap.example.com", "username": "owner", "password": "p"}, nil, http.StatusBadRequest)
	d.must(t, http.MethodPut, "/api/plugins/email/sources/mail/imap", map[string]any{"address": "imaps://imap.example.com", "username": "owner"}, nil, http.StatusBadRequest)
	d.must(t, http.MethodPut, "/api/plugins/email/sources/mail/imap", pull, nil, http.StatusNoContent)
	d.must(t, http.MethodDelete, "/api/plugins/email/sources/mail/imap", nil, nil, http.StatusNoContent)
}

func TestUserLetsDexReadAMailboxFolder(t *testing.T) {
	t.Parallel()
	// Given a mailbox server on loopback and an email source.
	mem := imapmemserver.New()
	user := imapmemserver.NewUser("owner", "app-password")
	if err := user.Create("Dex", nil); err != nil {
		t.Fatal(err)
	}
	mem.AddUser(user)
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		InsecureAuth: true,
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}},
	})
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() { _ = server.Close() })
	dataDir := t.TempDir()
	d := startDex(t, dataDir, "")
	d.must(t, http.MethodPut, "/api/settings/plugins/email", map[string]bool{"enabled": true}, nil, http.StatusNoContent)
	d.stop()
	d = startDex(t, dataDir, "")
	source := map[string]any{"secret": strings.Repeat("m", 40), "taskAgentId": "default"}
	d.must(t, http.MethodPut, "/api/plugins/email/sources/mail", source, nil, http.StatusNoContent)

	// When the owner saves a Pull and dex starts again.
	pull := map[string]any{"address": "imap://" + ln.Addr().String(), "username": "owner", "password": "app-password", "folder": "Dex"}
	d.must(t, http.MethodPut, "/api/plugins/email/sources/mail/imap", pull, nil, http.StatusNoContent)
	d.stop()
	d = startDex(t, dataDir, "")

	// Then the email/imap-pull job reads the folder and stores its cursor.
	conn := controlWriteDB(t, dataDir)
	validity := func() (int64, error) {
		var v int64
		err := conn.QueryRowContext(t.Context(), "SELECT uid_validity FROM plugin_email_imap_pulls WHERE source_id = 'mail'").Scan(&v)
		return v, err
	}
	eventually(t, "IMAP cursor", func() (bool, any) {
		v, err := validity()
		return err == nil && v != 0, []any{v, err}
	})
	// A new source secret keeps the Pull. Deleting the source deletes the Pull.
	source["secret"] = strings.Repeat("n", 40)
	d.must(t, http.MethodPut, "/api/plugins/email/sources/mail", source, nil, http.StatusNoContent)
	if v, err := validity(); err != nil || v == 0 {
		t.Fatalf("saving the source must keep its Pull: %d %v", v, err)
	}
	d.must(t, http.MethodDelete, "/api/plugins/email/sources/mail", nil, nil, http.StatusNoContent)
	if _, err := validity(); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleting the source must delete its Pull: %v", err)
	}
}
