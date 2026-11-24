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
	// Given separate Main and Chat streams, neither of which has a notification.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "spawn-notify")
	chatID := d.newSession(t).ID
	mainID := d.mainChat(t)
	chatWatch, mainWatch := d.watch(t, chatID), d.watch(t, mainID)
	for _, w := range []*watcher{chatWatch, mainWatch} {
		w.until(t, "empty initial notification page", func(tr *transcript) bool {
			return len(tr.noticeEvents) == 1 && tr.noticeEvents[0].Initial && len(tr.noticeEvents[0].Notifications) == 0
		})
	}

	// When a Web-started child returns raw data, the parent explicitly posts its conclusion.
	d.send(t, chatID, `{"title":"Web notification fixture","provider":"faux","modelId":"echo","message":"raw child data"}`)
	child := waitTimelineNotice(t, chatWatch, "Reviewed child conclusion", false)
	d.waitSettled(t, chatID)
	if replay := waitTimelineNotice(t, d.watch(t, chatID), child.Text, true); replay != child {
		t.Fatalf("reconnect changed the Outbox projection: %+v != %+v", replay, child)
	}
	var outbox api.OutboxMessageList
	d.must(t, http.MethodGet, chatPath(chatID, "outbox"), nil, &outbox)
	if !slices.ContainsFunc(outbox.Messages, func(m api.OutboxMessage) bool { return m.Id == child.Id && m.Body == child.Text }) {
		t.Fatalf("notice does not reference its durable Outbox row: %+v", outbox)
	}

	// A Web-started Monitor posts its own notice to Main, not to the Chat.
	d.setModel(t, "tool")
	d.send(t, mainID, `monitor_set_schedule {"delaySeconds":60,"prompt":"notify_user {\"content\":\"Monitor conclusion\"}"}`)
	d.waitSettled(t, mainID)
	conn := sessionDB(t, d)
	if _, err := conn.ExecContext(t.Context(), `UPDATE monitors SET due_at = ? WHERE conversation_id = ?`, time.Now().UnixMilli(), mainID); err != nil {
		t.Fatal(err)
	}
	monitor := waitTimelineNotice(t, mainWatch, "Monitor conclusion", false)
	for _, event := range mainWatch.tr.noticeEvents {
		if len(event.Notifications) > 0 && !slices.Equal(event.Notifications, []api.TimelineNotification{monitor}) {
			t.Fatalf("Main received another conversation's notice: %+v", event)
		}
	}
	var foreign api.HistoryPage
	d.must(t, http.MethodGet, chatPath(mainID, "history?beforeNotification="+url.QueryEscape(child.Id)), nil, &foreign)
	if foreign.Notifications == nil || len(*foreign.Notifications) != 0 {
		t.Fatalf("a cursor from another conversation was accepted: %+v", foreign)
	}

	// Given more notices than fit in the snapshot, the earliest real child conclusion is paged.
	if _, err := conn.ExecContext(t.Context(), `WITH RECURSIVE fixture(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM fixture WHERE n < 31)
		INSERT INTO outbox_messages (id, dedup_key, agent_id, conversation_id, source_submission_id, kind, body, status, created_at)
		SELECT 'fixture-'||n, 'tool:fixture-'||n, agent_id, conversation_id, source_submission_id, kind, 'Fixture notice', status, created_at+n
		FROM outbox_messages CROSS JOIN fixture WHERE id = ?`, child.Id); err != nil {
		t.Fatal(err)
	}
	w := d.watch(t, chatID)
	w.until(t, "bounded notification snapshot", func(tr *transcript) bool { return len(tr.noticeEvents) > 0 })
	initial := w.tr.noticeEvents[0]
	if len(initial.Notifications) != 30 || initial.NextBeforeNotification == "" || !initial.Initial {
		t.Fatalf("notification snapshot did not paginate: %+v", initial)
	}
	var page api.HistoryPage
	d.must(t, http.MethodGet, chatPath(chatID, "history?before=10000&limit=1&beforeNotification="+url.QueryEscape(initial.NextBeforeNotification)), nil, &page)
	if page.Notifications == nil || len(*page.Notifications) != 1 || page.NextBeforeNotification == nil {
		t.Fatalf("first older page: %+v", page)
	}
	cursor := *page.NextBeforeNotification
	page = api.HistoryPage{}
	d.must(t, http.MethodGet, chatPath(chatID, "history?limit=1&beforeNotification="+url.QueryEscape(cursor)), nil, &page)
	if page.Notifications == nil || !slices.Equal(*page.Notifications, []api.TimelineNotification{child}) || page.NextBeforeNotification != nil {
		t.Fatalf("oldest page did not contain the original Outbox entry: %+v", page)
	}
}

func TestUserCannotReadTimelineNotificationsUnderAnotherOrArchivedBot(t *testing.T) {
	t.Parallel()
	// Given a second bot with its own Web notice.
	s := startBots(t)
	bot := s.chat(s.b.ID)
	base := fmt.Sprintf("/api/agents/%s/chats/%d", s.b.ID, bot.id)
	s.d.must(t, http.MethodPut, base+"/settings", map[string]string{"provider": "faux", "modelId": "tool", "thinkingLevel": "off"}, nil)
	w := bot.watch()
	w.until(t, "initial notice page", func(tr *transcript) bool { return len(tr.noticeEvents) == 1 })
	bot.send(`notify_user {"content":"Second bot's notice"}`)
	notice := waitTimelineNotice(t, w, "Second bot's notice", false)
	defaultBase := chatPath(bot.id, "")
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
