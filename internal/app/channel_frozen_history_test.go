package app_test

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func TestUserResumesFrozenTelegramDeliveryFromBeforeThePackageSplit(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, data, 42)
	main := d.mainChat(t)
	d.stop()

	// Given pre-split storage values, with a confirmed first part and one confirmed fallback file.
	// These literals must not depend on the current renderer or JSON writer.
	const identity = `{"accountId":"123","senderId":"42"}`
	const command = `[{"rich":{"version":1,"html":"<p>Saved <b>command</b></p>"},"buttons":[{"text":"Saved button","data":"history-button"}]}]`
	const parts = `[{"rich":{"version":1,"html":"<p>Already sent</p>"}},{"group":[{"key":"first-file","name":"first.txt","mimeType":"text/plain","size":5},{"key":"second-file","name":"second.txt","mimeType":"text/plain","size":6}]},{"rich":{"version":1,"html":"<p>After album</p>"}}]`
	const savedIDs = `[{"id":"801","kind":"rich-v1"},{"id":"802","kind":"media"}]`
	conn := controlWriteDB(t, data)
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	seed(`INSERT INTO channel_commands (source_key, agent_id, peer, channel_key, command, completed, answer, channel_context, rendered_reply)
		VALUES ('history-command', 'default', 'telegram:42', 'main', '/help', 1, 'Do not render this command', ?, ?)`, identity, command)
	// A queued reply from a formerly allowed recipient must be settled without a send.
	seed(`INSERT INTO channel_commands (source_key, agent_id, peer, channel_key, command, completed, answer, channel_context, rendered_reply)
		VALUES ('history-revoked', 'default', 'telegram:43', 'main', '/help', 1, 'Do not send', '{"accountId":"123","senderId":"43"}', ?)`, command)
	seed(`INSERT INTO telegram_callbacks (id, agent_id, source_id, part, ordinal, peer, channel_context, conversation_id,
		execution_scope, execution_id, context_revision, label, object_id, action, expires_at)
		SELECT 'history-button', 'default', 'history-command', 0, 0, 'telegram:42', ?, id,
		execution_scope, coalesce(execution_id, 0), '', 'Saved button', '', '/help', 4102444800000 FROM conversations WHERE id = ?`, identity, main)
	seed(`INSERT INTO submissions (id, conversation_id, content, status, answer, origin, created_at, settled_at, channel_context,
		rendered_reply, delivered_parts, channel_group_fallback, channel_group_cursor, channel_remote_ids)
		VALUES ('history-reply', ?, 'original input', 'done', 'Do not render this reply', 'telegram:42', 1, 2, ?, ?, 1, 1, 1, ?)`, main, identity, parts, savedIDs)
	seed(`INSERT INTO outbox_messages (id, dedup_key, agent_id, conversation_id, source_submission_id, kind, peer, channel_context,
		body, rendered_parts, delivered_parts, group_fallback, group_cursor, remote_ids, created_at)
		VALUES ('history-reply', 'reply:history-reply', 'default', ?, 'history-reply', 'reply', 'telegram:42', ?,
		'Do not render this reply', ?, 1, 1, 1, ?, 2)`, main, identity, parts, savedIDs)
	seed(`INSERT INTO outbox_files (id, agent_id, operation_key, outbox_id, name, mime_type, body)
		VALUES ('first', 'default', 'first-file', 'history-reply', 'first.txt', 'text/plain', x'6669727374'),
		('second', 'default', 'second-file', 'history-reply', 'second.txt', 'text/plain', x'007365636fff')`)

	// When the controller restarts, then the saved command and only the missing parts are sent.
	startDex(t, data, f.srv.URL)
	ack := f.next(t)
	if ack.RichHTML != "<p>Saved <b>command</b></p>" || ack.ID <= 0 {
		t.Fatalf("frozen command: %+v", ack)
	}
	tail := f.next(t)
	if tail.RichHTML != "<p>After album</p>" || tail.ID <= ack.ID {
		t.Fatalf("frozen tail or send order changed: %+v", tail)
	}
	file := nextTelegramMedia(t, f.mediaSent)
	if file.Name != "second.txt" || file.Body != "\x00seco\xff" || file.Method != "sendDocument" || file.Chat != "42" {
		t.Fatalf("wrong fallback upload: %+v", file)
	}
	f.mu.Lock()
	buttons := f.keyboards[ack.ID]
	f.mu.Unlock()
	if len(buttons) != 1 || len(buttons[0]) != 1 || buttons[0][0].Text != "Saved button" || !strings.HasPrefix(buttons[0][0].Data, "d1.history-button.") {
		t.Fatalf("frozen buttons: %+v", buttons)
	}
	finalIDs := fmt.Sprintf(`[{"id":"801","kind":"rich-v1"},{"id":"802","kind":"media"},{"id":"%d","kind":"media"},{"id":"%d","kind":"rich-v1"}]`, file.ID, tail.ID)
	eventually(t, "recorded callback, outbox, and legacy cursor", func() (bool, any) {
		var result int
		err := conn.QueryRowContext(t.Context(), `SELECT count(*) FROM outbox_messages o
			JOIN submissions s ON s.id = o.source_submission_id
			JOIN channel_commands c ON c.source_key = 'history-command'
			JOIN telegram_callbacks b ON b.source_id = c.source_key
			WHERE o.id = 'history-reply' AND o.status = 'delivered' AND s.delivered_at IS NOT NULL AND c.delivered_at IS NOT NULL
			AND o.rendered_parts = ? AND s.rendered_reply = ? AND c.rendered_reply = ?
			AND o.delivered_parts = 3 AND s.delivered_parts = 3 AND o.group_cursor = 0 AND s.channel_group_cursor = 0
			AND o.group_fallback = 0 AND s.channel_group_fallback = 0
			AND o.remote_ids = ? AND s.channel_remote_ids = ? AND b.message_id = ?
			AND EXISTS (SELECT 1 FROM channel_commands WHERE source_key = 'history-revoked' AND delivered_at IS NOT NULL)`,
			parts, parts, command, finalIDs, finalIDs, fmt.Sprint(ack.ID)).Scan(&result)
		return err == nil && result == 1, fmt.Sprintf("matches=%d, err=%v", result, err)
	})
	if len(f.groupAttempts) != 0 || len(f.mediaAttempts) != 1 || f.sendCount.Load() != 2 {
		t.Fatalf("replayed confirmed parts or changed delivery method: group=%d media=%d rich=%d", len(f.groupAttempts), len(f.mediaAttempts), f.sendCount.Load())
	}
}

func TestUserResumesFrozenFeishuTextFromBeforeThePackageSplit(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	f := newFakeFeishu(t)
	d := startFeishu(t, f, data)
	pairFeishu(t, d, f)
	main := d.mainChat(t)
	d.stop()

	// Given a stored text part, independent of the current Part writer.
	const frozen = `[{"text":"Frozen plain text"}]`
	conn := controlWriteDB(t, data)
	if _, err := conn.ExecContext(t.Context(), `INSERT INTO outbox_messages
		(id, dedup_key, agent_id, conversation_id, kind, peer, body, rendered_parts, created_at)
		VALUES ('history-text', 'history-text', 'default', ?, 'proactive', 'feishu:owner', 'Do not render this text', ?, 1)`, main, frozen); err != nil {
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
		n := count(t, conn, `SELECT count(*) FROM outbox_messages WHERE id = 'history-text' AND status = 'delivered'
			AND delivered_parts = 1 AND rendered_parts = '[{"text":"Frozen plain text"}]' AND json_array_length(remote_ids) = 1`)
		return n == 1, n
	})
}
