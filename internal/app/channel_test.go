package app_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"
)

func TestUserClearsChannelMainContextOnce(t *testing.T) {
	// Given a Telegram peer with one conversation.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	f.message(1, 42, 7, "before")
	if got := f.next(t).Text; got != "echo: before" {
		t.Fatal(got)
	}
	// When /new is delivered twice and then the user sends another message.
	f.message(2, 42, 7, "/new")
	f.message(2, 42, 7, "/new")
	f.message(3, 42, 7, "after")
	// Then one notice confirms Clear, and main keeps the transcript.
	got := []string{f.next(t).Text, f.next(t).Text}
	slices.Sort(got)
	if !slices.Equal(got, []string{"Context cleared.", "echo: after"}) {
		t.Fatalf("replies: %q", got)
	}

	if chats := d.chats(t); len(chats) != 0 {
		t.Fatalf("channel in Sessions: %+v", chats)
	}
	d.watch(t, d.mainChat(t)).until(t, "answer after Clear", answered("echo: after"))
}

func TestUserGetsUndeliveredTelegramReplyAfterRestart(t *testing.T) {
	// Given a settled reply whose Bot API send fails.
	f := newFakeTelegram(t)
	f.failSend.Store(true)
	dir := t.TempDir()
	d := startTelegram(t, f, dir, 42)
	f.message(1, 42, 7, "retry after restart")
	select {
	case <-f.attempted:
	case <-time.After(30 * time.Second):
		t.Fatal("no send attempt")
	}
	// When control stops and starts with the same storage and a working API.
	d.stop()
	f.failSend.Store(false)
	startDex(t, dir, f.srv.URL)
	// Then the settled answer is sent, and its delivery is stored.
	if got := f.next(t).Text; got != "echo: retry after restart" {
		t.Fatal(got)
	}
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "control.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		var n int
		err = conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE delivered_at IS NOT NULL").Scan(&n)
		if err != nil {
			t.Fatal(err)
		}
		if n == 1 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatalf("delivered rows: %d", n)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestUserStopsCurrentChannelAnswer(t *testing.T) {
	// Given a Telegram chat whose agent runs a long command.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	d.setModel(t, "bash")
	f.message(1, 42, 7, "sleep 60")
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for !d.mainInfo(t).Busy {
		select {
		case <-deadline.C:
			t.Fatal("chat did not become busy")
		case <-time.After(20 * time.Millisecond):
		}
	}
	// When the user sends /stop, the pending run becomes unanswered.
	f.message(2, 42, 7, "/stop")
	var stopped bool
	for range 2 {
		if strings.Contains(f.next(t).Text, "Stop requested. Earlier actions are not undone. Processes and remote work may continue.") {
			stopped = true
		}
	}
	if !stopped {
		t.Fatal("no stop acknowledgement")
	}
	// Then the runtime status is visible without credentials.
	var settings map[string]any
	d.must(t, http.MethodGet, "/api/settings", nil, &settings)
	tg := settings["telegram"].(map[string]any)
	if tg["status"].(map[string]any)["state"] != "running" {
		t.Fatalf("status: %v", tg)
	}
}

func TestUserStopsChannelMessageBeforeWorkerAdmission(t *testing.T) {
	// Given an idle worker with admission closed for maintenance.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	d.must(t, http.MethodPost, "/api/maintenance", nil, nil)
	// When a message is queued and /stop follows before worker conversation creation.
	f.message(1, 42, 7, "must not execute")
	f.message(2, 42, 7, "/stop")
	var stopped bool
	for range 2 {
		if f.next(t).Text == "Stop requested. Earlier actions are not undone. Processes and remote work may continue." {
			stopped = true
		}
	}
	if !stopped {
		t.Fatal("no stop acknowledgement")
	}
	d.must(t, http.MethodDelete, "/api/maintenance", nil, nil)
	f.message(3, 42, 7, "after stop")
	// Then only the later message runs, and the stopped message remains unanswered.
	if got := f.next(t).Text; got != "echo: after stop" {
		t.Fatal(got)
	}
	if c := d.mainInfo(t); c.Busy {
		t.Fatalf("main: %+v", c)
	}
}

func TestUserRetrySkipsConfirmedTelegramParts(t *testing.T) {
	// Given an answer that spans two Telegram messages, with the second send failing.
	f := newFakeTelegram(t)
	f.failAfter.Store(1)
	startTelegram(t, f, t.TempDir(), 42)
	f.message(1, 42, 7, strings.Repeat("a", 4100))
	first := f.next(t).Text
	if len(first) != 4096 {
		t.Fatalf("first length: %d", len(first))
	}
	eventually(t, "second send failure", func() (bool, any) { n := f.sendCount.Load(); return n >= 2, n })
	// When Telegram becomes available again.
	f.failAfter.Store(0)
	// Then only the remaining part is sent.
	second := f.next(t).Text
	if first+second != "echo: "+strings.Repeat("a", 4100) {
		t.Fatalf("second part: %q", second)
	}
}

func TestUserCannotWriteToAnyExternalChannelOnWeb(t *testing.T) {
	// Given external channel chats in control storage.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "echo")
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "control.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	for _, platform := range []string{"telegram", "wechat", "feishu"} {
		res, err := conn.ExecContext(t.Context(), "INSERT INTO conversations (agent_id,channel_key,created_at) VALUES ('default',?,1)", platform+":owner/old")
		if err != nil {
			t.Fatal(err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		// When the web sends input or changes settings, the server refuses it.
		for method, body := range map[string]any{http.MethodPost: map[string]any{"id": uuid.NewV7().String(), "text": "not allowed"}, http.MethodPut: map[string]string{"thinkingLevel": "high"}} {
			suffix := "messages"
			if method == http.MethodPut {
				suffix = "settings"
			}
			status, raw := d.call(t, method, fmt.Sprintf("/api/agents/default/chats/%d/%s", id, suffix), body)
			if status != http.StatusForbidden {
				t.Fatalf("%s %s: %d %s", platform, method, status, raw)
			}
		}
	}
	// Then Sessions excludes the old channel conversations.
	if chats := d.chats(t); len(chats) != 0 {
		t.Fatalf("old channels in Sessions: %+v", chats)
	}
}

func TestUserBlockedPeerDoesNotStopOtherReplies(t *testing.T) {
	// Given a bot that one peer has blocked.
	f := newFakeTelegram(t)
	f.permanentChat.Store(7)
	startTelegram(t, f, t.TempDir(), 42)
	// When that peer and another peer each request an answer.
	f.message(1, 42, 7, "cannot deliver")
	f.message(2, 42, 8, "still reachable")
	// Then the permanent failure does not stop the other answer.
	if got := f.next(t); got.ChatID != 8 || got.Text != "echo: still reachable" {
		t.Fatalf("reply: %+v", got)
	}
}

func TestUserFailedStopDoesNotBlockLaterChannelReplies(t *testing.T) {
	// Given a chat whose worker conversation no longer exists.
	f := newFakeTelegram(t)
	dir := t.TempDir()
	startTelegram(t, f, dir, 42)
	f.message(1, 42, 7, "initial")
	if got := f.next(t).Text; got != "echo: initial" {
		t.Fatal(got)
	}
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "control.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var workerID int64
	if err := conn.QueryRowContext(t.Context(), "SELECT worker_conversation_id FROM conversations WHERE channel_key='main'").Scan(&workerID); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(t.Context(), "UPDATE conversations SET worker_conversation_id = 999999 WHERE channel_key = 'main'"); err != nil {
		t.Fatal(err)
	}
	// When /stop gets a permanent worker error, the command still has an answer.
	f.message(2, 42, 7, "/stop")
	if got := f.next(t).Text; got != "Stop requested. Earlier actions are not undone. Processes and remote work may continue." {
		t.Fatalf("stop reply: %q", got)
	}
	// Then the platform advances past /stop. Repair the test's missing worker record.
	if _, err = conn.ExecContext(t.Context(), "UPDATE conversations SET worker_conversation_id=? WHERE channel_key='main'", workerID); err != nil {
		t.Fatal(err)
	}
	f.message(3, 42, 8, "after failed stop")
	if got := f.next(t); got.ChatID != 8 || got.Text != "echo: after failed stop" {
		t.Fatalf("later reply: %+v", got)
	}
}

func TestUserStopsExistingChannelChatDuringMaintenance(t *testing.T) {
	// Given a chat whose worker conversation already exists and admission is closed.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	f.message(1, 42, 7, "initial")
	if got := f.next(t).Text; got != "echo: initial" {
		t.Fatal(got)
	}
	eventually(t, "maintenance after the initial answer", func() (bool, any) {
		status, body := d.call(t, http.MethodPost, "/api/maintenance", nil)
		return status == http.StatusOK, body
	})
	// When a message waits for admission and the user requests /stop.
	f.message(2, 42, 7, "must not execute")
	eventually(t, "queued message", func() (bool, any) { c := d.mainInfo(t); return c.Busy, c })
	f.message(3, 42, 7, "/stop")
	// Then stop completes while the gate is still closed.
	var stopped bool
	for range 2 {
		if f.next(t).Text == "Stop requested. Earlier actions are not undone. Processes and remote work may continue." {
			stopped = true
		}
	}
	if !stopped {
		t.Fatal("no stop acknowledgement")
	}
	d.must(t, http.MethodDelete, "/api/maintenance", nil, nil)
	f.message(4, 42, 7, "after gate")
	if got := f.next(t).Text; got != "echo: after gate" {
		t.Fatal(got)
	}
	// The reply precedes the memory commit. Wait for that commit before the worker exits,
	// so its git child cannot write after TempDir removes the data directory.
	eventually(t, "memory commit before shutdown", func() (bool, any) {
		status, body := d.call(t, http.MethodPost, "/api/maintenance", nil)
		return status == http.StatusOK, body
	})
	d.stop()
}
