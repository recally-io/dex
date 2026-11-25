package app_test

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/api"
)

func TestUserPromisedChannelFollowUpStaysInternalWithoutExplicitSend(t *testing.T) {
	t.Parallel()
	e := newMainEntry(t, "telegram")
	d := e.d
	conn := sessionDB(t, d)
	main := d.mainChat(t)
	d.setModel(t, "spawn")
	// Given an explicitly committed delegation from the channel.
	e.send(childJSON(t, map[string]any{"title": "Promised", "message": "private child findings", "provider": "faux", "modelId": "echo", "follow_up": true}))
	// When the parent writes only ordinary final text on the result.
	work := waitExecution(t, conn, main, "Promised", "done")
	waitResultSubmission(t, conn, work.ResultID, "done")
	var state string
	if err := conn.QueryRowContext(t.Context(), `SELECT state FROM channel_follow_ups WHERE result_id = ?`, work.ResultID).Scan(&state); err != nil || state != "missed_follow_up" {
		t.Fatalf("expected a recorded miss for the original result: state=%q err=%v", state, err)
	}
	if n := count(t, conn, `SELECT count(*) FROM submissions WHERE conversation_id=?`, main); n != 2 {
		t.Fatalf("expected the request and original result only; got %d turns", n)
	}
	// Then no internal draft or child findings reach the channel.
	if slices.ContainsFunc(e.sends(), func(m channelSend) bool {
		return strings.Contains(m.Text, "private child findings") || strings.Contains(m.Text, "channel-follow-up")
	}) {
		t.Fatalf("internal text was sent: %+v", e.sends())
	}
	if n := count(t, conn, `SELECT count(*) FROM outbox_messages WHERE source_submission_id = ?`, work.ResultID); n != 0 {
		t.Fatalf("internal work queued %d outward deliveries", n)
	}
}

func TestUserPromisedChannelFollowUpSendsOneConfirmedConclusion(t *testing.T) {
	t.Parallel()
	for _, platform := range []string{"telegram", "wechat", "feishu"} {
		t.Run(platform, func(t *testing.T) {
			e := newMainEntry(t, platform)
			d := e.d
			conn := sessionDB(t, d)
			main := d.mainChat(t)
			d.setModel(t, "spawn-notify")
			e.send(childJSON(t, map[string]any{"title": "Reviewed", "message": "private raw result", "provider": "faux", "modelId": "echo", "follow_up": true}))
			work := waitExecution(t, conn, main, "Reviewed", "done")
			waitResultSubmission(t, conn, work.ResultID, "done")
			e.wait(t, "Reviewed channel conclusion")
			eventually(t, "platform confirmation", func() (bool, any) {
				var state, delivery, peer string
				err := conn.QueryRowContext(t.Context(), `SELECT f.state,o.status,o.peer FROM channel_follow_ups f JOIN outbox_messages o ON o.dedup_key='follow-up-final:'||f.delegation_id WHERE f.result_id=? AND o.delivered_at IS NOT NULL`, work.ResultID).Scan(&state, &delivery, &peer)
				return err == nil && state == "notified" && delivery == "delivered" && peer == e.peer, []any{state, delivery, peer, err}
			})
			sent := 0
			for _, m := range e.sends() {
				if strings.Contains(m.Text, "private raw result") {
					t.Fatalf("sent raw result: %+v", m)
				}
				if m.Text == "Reviewed channel conclusion" {
					sent++
				}
			}
			if sent != 1 {
				t.Fatalf("expected one confirmed conclusion; got %d", sent)
			}
		})
	}
}

func TestUserSeesConfirmedFollowUpWhenStopRacesAnInFlightSend(t *testing.T) {
	t.Parallel()
	const conclusion = "Reviewed channel conclusion"
	inFlight, release := make(chan struct{}, 1), make(chan struct{})
	f := newFakeTelegram(t)
	f.holdRich = func(ctx context.Context, text string) {
		if text != conclusion {
			return
		}
		inFlight <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	d := startTelegram(t, f, t.TempDir(), 42)
	d.setModel(t, "spawn-notify")
	main := d.mainChat(t)
	conn := sessionDB(t, d)
	watch := d.watch(t, main)
	watch.until(t, "Main's initial notice page", func(tr *transcript) bool { return len(tr.noticeEvents) > 0 })
	f.message(1, 42, 7, childJSON(t, map[string]any{"title": "In-flight follow-up", "message": "child data", "provider": "faux", "modelId": "echo", "follow_up": true}))
	work := waitExecution(t, conn, main, "In-flight follow-up", "done")
	select {
	case <-inFlight:
	case <-time.After(15 * time.Second):
		t.Fatal("the conclusion never reached the platform fixture")
	}
	f.message(2, 42, 7, "/stop")
	eventually(t, "legacy channel Stop revokes the in-flight conclusion", func() (bool, any) {
		n := count(t, conn, `SELECT count(*) FROM outbox_messages WHERE source_submission_id = ? AND status = 'revoked' AND delivered_at IS NULL`, work.ResultID)
		return n == 1, n
	})
	var outbox api.OutboxMessageList
	d.must(t, http.MethodGet, chatPath(main, "outbox"), nil, &outbox)
	if !slices.ContainsFunc(outbox.Messages, func(m api.OutboxMessage) bool {
		return m.Body == conclusion && m.Peer == "telegram:7" && m.Status == api.OutboxMessageStatusRevoked
	}) {
		t.Fatalf("owner lost the revoked delivery record: %+v", outbox)
	}
	close(release)
	eventually(t, "platform receipt for the already in-flight conclusion", func() (bool, any) {
		var status, remotes string
		err := conn.QueryRowContext(t.Context(), `SELECT status, remote_ids FROM outbox_messages WHERE source_submission_id = ?`, work.ResultID).Scan(&status, &remotes)
		return err == nil && status == "delivered" && remotes != "[]", []any{status, remotes, err}
	})

	// A later Web notice proves the live stream progressed past the Telegram send.
	d.setModel(t, "tool")
	d.send(t, main, `notify_user {"content":"Web timeline only"}`)
	web := waitTimelineNotice(t, watch, "Web timeline only", false)
	for _, event := range watch.tr.noticeEvents {
		if len(event.Notifications) > 0 && !slices.Equal(event.Notifications, []api.TimelineNotification{web}) {
			t.Fatalf("channel notice leaked into live Web timeline: %+v", event)
		}
	}
	reloaded := d.watch(t, main)
	waitTimelineNotice(t, reloaded, web.Text, true)
	if !slices.Equal(reloaded.tr.noticeEvents[0].Notifications, []api.TimelineNotification{web}) {
		t.Fatalf("channel notice leaked after reconnect: %+v", reloaded.tr.noticeEvents)
	}
	var page api.HistoryPage
	d.must(t, http.MethodGet, chatPath(main, "history?beforeNotification="+url.QueryEscape(web.Id)), nil, &page)
	if page.Notifications == nil || len(*page.Notifications) != 0 {
		t.Fatalf("channel notice leaked in history: %+v", page)
	}
}
