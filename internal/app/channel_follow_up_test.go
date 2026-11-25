package app_test

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/channel"
)

func TestUserReceivesReviewedTelegramConclusionOnCanonicalCompletion(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	d.setModel(t, "spawn-notify")
	main := d.mainChat(t)
	conn := sessionDB(t, d)

	// Given a Telegram request that opts in and a child that explicitly reports its findings.
	f.message(1, 42, 7, childJSON(t, map[string]any{
		"title": "Explicit report", "message": `session_report {"content":"private verified findings"}`,
		"provider": "faux", "modelId": "tool", "follow_up": true,
	}))
	waitTaskChannelText(t, f, "spawn:")

	// When Main reviews the canonical result and calls notify_user.
	work := waitExecution(t, conn, main, "Explicit report", "done")
	waitResultSubmission(t, conn, work.ResultID, "done")
	final := followUpTelegramSend(t, f, "Reviewed channel conclusion")

	// Then the report is the result, not a separate turn without a channel grant.
	if n := count(t, conn, `SELECT count(*) FROM submissions WHERE conversation_id=? AND id LIKE 'report:%'`, main); n != 0 {
		t.Fatalf("explicit child final created %d standalone report wakes, want none", n)
	}
	if n := count(t, conn, `SELECT count(*) FROM submissions WHERE conversation_id=? AND id LIKE 'result:%' AND json_extract(content, '$."task-result".answer')='private verified findings'`, main); n != 1 {
		var content, disposition string
		var activation int64
		_ = conn.QueryRowContext(t.Context(), `SELECT s.content, f.native_activation_id FROM submissions s JOIN channel_follow_ups f ON f.result_id=s.id WHERE s.id=?`, work.ResultID).Scan(&content, &activation)
		_ = conn.QueryRowContext(t.Context(), `SELECT result FROM routine_tool_operations WHERE source_request_id=?`, work.Turn).Scan(&disposition)
		t.Fatalf("canonical results with staged report content = %d, want one: content=%s activation=%d disposition=%q", n, content, activation, disposition)
	}
	assertFollowUpTelegramReceipt(t, conn, work.ResultID, final)
}

func followUpTelegramSend(t *testing.T, f *fakeTelegram, text string) sentMessage {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case m := <-f.sent:
			if strings.Contains(m.Text, text) {
				return m
			}
		case <-deadline:
			t.Fatalf("Telegram did not receive %q", text)
		}
	}
}

func assertFollowUpTelegramReceipt(t *testing.T, conn *sql.DB, resultID string, platform sentMessage) {
	t.Helper()
	eventually(t, "confirmed Telegram receipt for the reviewed conclusion", func() (bool, any) {
		var state, peer, ids string
		err := conn.QueryRowContext(t.Context(), `SELECT f.state, o.peer, o.remote_ids FROM outbox_messages o
JOIN channel_follow_ups f ON o.event_id='follow-up-final:'||f.delegation_id
WHERE o.source_submission_id=? AND o.status='sent'`, resultID).Scan(&state, &peer, &ids)
		var remotes []channel.RemoteMessage
		decode := json.Unmarshal([]byte(ids), &remotes)
		return err == nil && decode == nil && state == "notified" && peer == fmt.Sprintf("telegram:%d", platform.ChatID) &&
			len(remotes) == 1 && remotes[0].ID == fmt.Sprint(platform.ID), []any{state, peer, ids, platform.ID, err}
	})
}

func TestUserOptedInProgressReportStillWakesOnlyTheParent(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	d.setModel(t, "spawn-notify")
	conn := sessionDB(t, d)
	main := d.mainChat(t)
	// Given a promised delegation with a progress report but no explicit final report.
	const title = "Progress keeps its wake"
	f.message(1, 42, 42, childJSON(t, map[string]any{
		"title": title, "message": `session_report {"content":"private progress note","final":false}`,
		"provider": "faux", "modelId": "tool", "follow_up": true,
	}))
	followUpTelegramSend(t, f, "spawn:")
	// When the scheduled progress report and native completion both reach Main, only the
	// canonical completion carries a grant. It uses the native answer, not the progress note.
	work := waitExecution(t, conn, main, title, "done")
	final := waitResultSubmission(t, conn, work.ResultID, "done")
	if body := resultBody(t, final.content); body["answer"] != work.Report || strings.Contains(final.content, "private progress note") {
		t.Fatalf("progress changed the native answer fallback: %+v, %v", work, body)
	}
	assertFollowUpTelegramReceipt(t, conn, work.ResultID, followUpTelegramSend(t, f, "Reviewed channel conclusion"))
	if n := count(t, conn, `SELECT count(*) FROM submissions WHERE conversation_id=? AND id LIKE 'report:%' AND channel_context='{}' AND instr(content,'channel-follow-up')=0 AND instr(content,'private progress note')>0`, main); n != 1 {
		t.Fatalf("ordinary progress wakes = %d, want one without a channel grant", n)
	}
	if n := count(t, conn, `SELECT count(*) FROM outbox_messages WHERE source_submission_id LIKE 'report:%'`); n != 0 {
		t.Fatalf("progress triggered %d outbound notifications", n)
	}
}

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
				err := conn.QueryRowContext(t.Context(), `SELECT f.state,o.status,o.peer FROM channel_follow_ups f JOIN outbox_messages o ON o.event_id='follow-up-final:'||f.delegation_id WHERE f.result_id=? AND o.delivered_at IS NOT NULL`, work.ResultID).Scan(&state, &delivery, &peer)
				return err == nil && state == "notified" && delivery == "sent" && peer == e.peer, []any{state, delivery, peer, err}
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
		n := count(t, conn, `SELECT count(*) FROM outbox_messages WHERE source_submission_id = ? AND status = 'failed' AND delivered_at IS NULL`, work.ResultID)
		return n == 1, n
	})
	var outbox api.OutboxMessageList
	d.must(t, http.MethodGet, chatPath(main, "outbox"), nil, &outbox)
	if !slices.ContainsFunc(outbox.Messages, func(m api.OutboxMessage) bool {
		return m.Body == conclusion && m.Peer == "telegram:7" && m.Status == api.OutboxMessageStatusFailed
	}) {
		t.Fatalf("owner lost the revoked delivery record: %+v", outbox)
	}
	close(release)
	sent := followUpTelegramSend(t, f, conclusion)
	eventually(t, "platform receipt for the already in-flight conclusion", func() (bool, any) {
		var peer, remotes, state, disposition string
		err := conn.QueryRowContext(t.Context(), `SELECT d.peer, d.remote_ids, f.state, d.disposition
FROM outbox_delivery_diagnostics d JOIN channel_follow_ups f ON f.result_id=d.source_submission_id WHERE f.result_id=?`, work.ResultID).Scan(&peer, &remotes, &state, &disposition)
		var receipts []channel.RemoteMessage
		decode := json.Unmarshal([]byte(remotes), &receipts)
		return err == nil && decode == nil && state == "cancelled" && disposition == "platform_confirmed" && peer == "telegram:7" &&
			len(receipts) == 1 && receipts[0].ID == fmt.Sprint(sent.ID), []any{peer, remotes, state, disposition, sent.ID, err}
	})
	d.setModel(t, "echo")
	f.message(3, 42, 7, "in-flight replay barrier")
	followUpTelegramSend(t, f, "echo: in-flight replay barrier")
	select {
	case <-inFlight:
		t.Fatal("already confirmed revoked conclusion entered Telegram again")
	default:
	}

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
