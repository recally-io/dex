package app_test

import (
	"slices"
	"strings"
	"testing"
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
