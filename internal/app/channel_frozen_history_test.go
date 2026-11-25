package app_test

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

func TestUserResumesFrozenFeishuTextAtTheSameDestination(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	f := newFakeFeishu(t)
	d := startFeishu(t, f, data)
	pairFeishu(t, d, f)
	main := d.mainChat(t)
	d.stop()

	// Given a stored text part, independent of the current Part writer.
	const frozen = `{"text":"Frozen plain text"}`
	conn := controlWriteDB(t, data)
	if _, err := conn.ExecContext(t.Context(), `INSERT INTO outbox_messages
		(id, event_id, agent_id, conversation_id, kind, peer, channel_context, body, rendered_message, created_at)
        VALUES ('history-text', 'history-text', 'default', ?, 'proactive', 'feishu:owner', '{"accountId":"app"}', 'Do not render this text', ?, 1)`, main, frozen); err != nil {
		t.Fatal(err)
	}

	// When the controller restarts, then Feishu uses the saved text and the same delivery identity.
	startFeishu(t, f, data)
	f.waitReply(t, "Frozen plain text")
	digest := sha256.Sum256([]byte("history-text:0"))
	want := fmt.Sprintf("%x", digest[:24])
	f.mu.Lock()
	var uuid string
	if len(f.uuids) > 0 {
		uuid = f.uuids[len(f.uuids)-1]
	}
	f.mu.Unlock()
	if uuid != want {
		t.Fatalf("delivery identity = %q, want %q", uuid, want)
	}
	eventually(t, "unchanged frozen Feishu part marked delivered", func() (bool, any) {
		n := count(t, conn, `SELECT count(*) FROM outbox_messages WHERE id = 'history-text' AND status = 'sent'
			AND rendered_message = '{"text":"Frozen plain text"}' AND json_array_length(remote_ids) = 1`)
		return n == 1, n
	})
}
