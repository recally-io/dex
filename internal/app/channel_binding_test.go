package app_test

import (
	"net/http"
	"path/filepath"
	"testing"
)

func TestUserDoesNotReceiveOldRunAfterPairingSameTelegramPeer(t *testing.T) {
	// Given a Telegram run waiting for a tool to finish.
	f := newFakeTelegram(t)
	dir := t.TempDir()
	d := startTelegram(t, f, dir, 42)
	d.setModel(t, "bash")
	f.message(1, 42, 42, taskGate(t, dir, "old-binding"))
	waitTaskFile(t, filepath.Join(dir, "old-binding.pid"))

	// When the owner disconnects and reconnects the same bot and peer.
	const path = "/api/agents/default/channels/telegram"
	d.must(t, http.MethodDelete, path, nil, nil, http.StatusOK)
	d.must(t, http.MethodPut, path, map[string]any{"token": fakeToken, "allowedUsers": []int64{42}}, nil)
	releaseResultGate(t, dir, "old-binding")

	// Then the old answer is unavailable for delivery and the new peer receives its own reply.
	conn := sessionDB(t, d)
	eventually(t, "old reply is not deliverable", func() (bool, any) {
		var status string
		err := conn.QueryRowContext(t.Context(), `SELECT o.status FROM outbox_messages o JOIN submissions s ON s.id = o.source_submission_id
JOIN channel_bindings b ON b.id = o.binding_id AND b.id = s.binding_id
WHERE s.origin = 'telegram:42' AND b.revoked_at IS NOT NULL`).Scan(&status)
		return err == nil && status == "revoked", status
	})
	f.message(2, 42, 42, "printf new-binding")
	if sent := f.next(t); sent.Text != "bash: new-binding" || sent.ChatID != 42 {
		t.Fatalf("got %+v, want the new reply only", sent)
	}
}
