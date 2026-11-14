package app_test

import (
	"strings"
	"testing"
)

func TestUserTelegramStructuredMessagesKeepTheirSuppliedFields(t *testing.T) {
	for _, input := range []struct {
		kind, want string
		value      any
	}{
		{"location", "latitude 1.5, longitude 2.5", map[string]any{"latitude": 1.5, "longitude": 2.5}},
		{"contact", "Example Person; phone +10000000000", map[string]any{"first_name": "Example", "last_name": "Person", "phone_number": "+10000000000"}},
		{"poll", "Blue (2 votes)", map[string]any{"question": "Choose a color", "options": []map[string]any{{"text": "Blue", "voter_count": 2}, {"text": "Green", "voter_count": 1}}}},
	} {
		t.Run(input.kind, func(t *testing.T) {
			t.Parallel()
			// Given a non-file Telegram message with explicit location, contact or poll fields.
			f := newFakeTelegram(t)
			startTelegram(t, f, t.TempDir(), 42)
			f.mu.Lock()
			f.updates = append(f.updates, map[string]any{"update_id": int64(1), "message": map[string]any{
				"message_id": 1, "from": map[string]any{"id": 42}, "chat": map[string]any{"id": 42, "type": "private"}, input.kind: input.value,
			}})
			f.mu.Unlock()
			// When it enters the normal native input path, the supplied fields remain readable text.
			if got := f.next(t).Text; !strings.Contains(got, input.want) {
				t.Fatalf("structured message lost: %q", got)
			}
		})
	}
}

func TestUserTelegramForwardedCommandRemainsQuotedContent(t *testing.T) {
	t.Parallel()
	// Given an authorized sender forwarding text that happens to name a command.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	f.mu.Lock()
	f.updates = append(f.updates, map[string]any{"update_id": int64(1), "message": map[string]any{
		"message_id": 1, "from": map[string]any{"id": 42}, "chat": map[string]any{"id": 42, "type": "private"}, "text": "/clear",
		"forward_origin": map[string]any{"type": "hidden_user", "sender_user_name": "Original sender"},
	}})
	f.mu.Unlock()
	// When the adapter handles it, the native input receives data instead of a control action.
	if got := f.next(t).Text; !strings.Contains(got, "Forwarded message:\n/clear") {
		t.Fatalf("forward ran as a command: %q", got)
	}
	conn := controlWriteDB(t, d.dataDir(t))
	if n := count(t, conn, "SELECT count(*) FROM channel_commands WHERE source_key='telegram:123:1'"); n != 0 {
		t.Fatalf("forward created %d control commands", n)
	}
}
