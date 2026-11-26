package app_test

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/api"
)

func waitTimelineNotice(t *testing.T, w *watcher, text string, initial bool) api.TimelineNotification {
	t.Helper()
	var notice api.TimelineNotification
	w.until(t, text, func(tr *transcript) bool {
		for _, event := range tr.noticeEvents {
			if event.Initial != initial {
				continue
			}
			for _, entry := range event.Notifications {
				if entry.Text == text {
					notice = entry
					return true
				}
			}
		}
		return false
	})
	return notice
}

func TestUserSeesWebChildAndMonitorNotificationsInTheirOrigin(t *testing.T) {
	t.Parallel()
	// Given separate Main and Session streams, neither of which has a notification.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "spawn-notify")
	sessionID := d.newSession(t).ID
	mainID := d.mainSession(t)
	sessionWatch, mainWatch := d.watch(t, sessionID), d.watch(t, mainID)
	for _, w := range []*watcher{sessionWatch, mainWatch} {
		w.until(t, "empty initial notification page", func(tr *transcript) bool {
			return len(tr.noticeEvents) == 1 && tr.noticeEvents[0].Initial && len(tr.noticeEvents[0].Notifications) == 0
		})
	}

	// When a Web-started child returns raw data, the parent explicitly posts its conclusion.
	d.send(t, sessionID, `{"title":"Web notification fixture","provider":"faux","modelId":"echo","message":"raw child data"}`)
	child := waitTimelineNotice(t, sessionWatch, "Reviewed child conclusion", false)
	d.waitSettled(t, sessionID)
	conn := sessionDB(t, d)
	reloaded := d.watch(t, sessionID)
	waitTimelineNotice(t, reloaded, child.Text, true)
	var outbox api.OutboxMessageList
	d.must(t, http.MethodGet, sessionPath(sessionID, "outbox"), nil, &outbox)
	if len(outbox.Messages) != 0 {
		t.Fatalf("Web-only notice entered the channel Outbox: %+v", outbox)
	}

	// A Web-started Monitor posts its own notice to Main, not to the Session.
	d.setModel(t, "tool")
	d.send(t, mainID, `monitor_set {"delaySeconds":60,"prompt":"notify_user {\"content\":\"Monitor conclusion\"}"}`)
	d.waitSettled(t, mainID)
	if _, err := conn.ExecContext(t.Context(), `UPDATE monitors SET due_at = ? WHERE session_id = ?`, time.Now().UnixMilli(), mainID); err != nil {
		t.Fatal(err)
	}
	monitor := waitTimelineNotice(t, mainWatch, "Monitor conclusion", false)
	for _, event := range mainWatch.tr.noticeEvents {
		if len(event.Notifications) > 0 && !slices.Equal(event.Notifications, []api.TimelineNotification{monitor}) {
			t.Fatalf("Main received another conversation's notice: %+v", event)
		}
	}
	var foreign api.HistoryPage
	d.must(t, http.MethodGet, sessionPath(mainID, "history?beforeNotification="+url.QueryEscape(child.Id)), nil, &foreign)
	if foreign.Notifications == nil || len(*foreign.Notifications) != 0 {
		t.Fatalf("a cursor from another conversation was accepted: %+v", foreign)
	}

	// Given more notices than fit in the snapshot, the earliest real child conclusion is paged.
	if _, err := conn.ExecContext(t.Context(), `WITH RECURSIVE fixture(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM fixture WHERE n < 31)
		INSERT INTO web_notifications (id, event_id, bot_id, session_id, source_submission_id, body, created_at)
		SELECT 'fixture-'||n, 'tool:fixture-'||n, bot_id, session_id, source_submission_id, 'Fixture notice', created_at+n
		FROM web_notifications CROSS JOIN fixture WHERE id = ?`, child.Id); err != nil {
		t.Fatal(err)
	}
	w := d.watch(t, sessionID)
	w.until(t, "bounded notification snapshot", func(tr *transcript) bool { return len(tr.noticeEvents) > 0 })
	initial := w.tr.noticeEvents[0]
	if len(initial.Notifications) != 30 || initial.NextBeforeNotification == "" || !initial.Initial {
		t.Fatalf("notification snapshot did not paginate: %+v", initial)
	}
	var page api.HistoryPage
	d.must(t, http.MethodGet, sessionPath(sessionID, "history?before=10000&limit=1&beforeNotification="+url.QueryEscape(initial.NextBeforeNotification)), nil, &page)
	if page.Notifications == nil || len(*page.Notifications) != 1 || page.NextBeforeNotification == nil {
		t.Fatalf("first older page: %+v", page)
	}
	cursor := *page.NextBeforeNotification
	page = api.HistoryPage{}
	d.must(t, http.MethodGet, sessionPath(sessionID, "history?limit=1&beforeNotification="+url.QueryEscape(cursor)), nil, &page)
	if page.Notifications == nil || !slices.Equal(*page.Notifications, []api.TimelineNotification{child}) || page.NextBeforeNotification != nil {
		t.Fatalf("oldest page did not contain the original Web notice: %+v", page)
	}
}

func TestUserCannotReadTimelineNotificationsUnderAnotherOrArchivedBot(t *testing.T) {
	t.Parallel()
	// Given a second bot with its own Web notice.
	s := startBots(t)
	bot := s.session(s.b.ID)
	base := fmt.Sprintf("/api/bots/%s/sessions/%d", s.b.ID, bot.id)
	s.d.must(t, http.MethodPut, base+"/settings", map[string]string{"provider": "faux", "modelId": "tool", "thinkingLevel": "off"}, nil)
	w := bot.watch()
	w.until(t, "initial notice page", func(tr *transcript) bool { return len(tr.noticeEvents) == 1 })
	bot.send(`notify_user {"content":"Second bot's notice"}`)
	notice := waitTimelineNotice(t, w, "Second bot's notice", false)
	defaultBase := sessionPath(bot.id, "")
	for _, path := range []string{"events", "history?beforeNotification=" + url.QueryEscape(notice.Id), "outbox"} {
		s.d.must(t, http.MethodGet, defaultBase+path, nil, nil, http.StatusNotFound)
	}

	// When the owner archives that bot, its own endpoints no longer authorize it.
	archiveCoder(t, s.d)
	for _, path := range []string{"/events", "/history?beforeNotification=" + url.QueryEscape(notice.Id), "/outbox"} {
		s.d.must(t, http.MethodGet, base+path, nil, nil, http.StatusNotFound)
	}
	deadline := time.After(10 * time.Second)
	for {
		select {
		case event, open := <-w.events:
			if !open {
				return
			}
			if len(event.Notifications) != 0 {
				t.Fatalf("archived bot still emitted notices: %+v", event)
			}
		case <-deadline:
			t.Fatal("archived bot's existing stream did not close")
		}
	}
}
