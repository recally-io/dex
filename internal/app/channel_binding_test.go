package app_test

import (
	"net/http"
	"testing"
)

func TestUserDoesNotReceiveCancelledOutboxAfterPairingSameTelegramPeer(t *testing.T) {
	// Given a final reply whose Telegram send fails temporarily.
	f := newFakeTelegram(t)
	f.failSend.Store(true)
	dir := t.TempDir()
	d := startTelegram(t, f, dir, 42)
	f.message(1, 42, 42, "old reply")
	conn := controlWriteDB(t, dir)
	eventually(t, "old reply awaits delivery", func() (bool, any) {
		n := count(t, conn, `SELECT count(*) FROM outbox_messages WHERE body = 'echo: old reply' AND status = 'pending' AND attempts > 0`)
		return n == 1, n
	})

	// When the owner disconnects and reconnects the same bot and peer.
	const path = "/api/agents/default/channels/telegram"
	d.must(t, http.MethodDelete, path, nil, nil, http.StatusOK)
	f.failSend.Store(false)
	d.must(t, http.MethodPut, path, map[string]any{"token": fakeToken, "allowedUsers": []int64{42}}, nil)

	// Then the old delivery is failed and the new message receives its own reply.
	if n := count(t, conn, `SELECT count(*) FROM outbox_messages WHERE body = 'echo: old reply' AND status = 'failed'`); n != 1 {
		t.Fatalf("cancelled Outbox entries = %d, want 1", n)
	}
	f.message(2, 42, 42, "new reply")
	if sent := f.next(t); sent.Text != "echo: new reply" || sent.ChatID != 42 {
		t.Fatalf("got %+v, want the new reply only", sent)
	}
}
