package app_test

import "testing"

func TestUserTelegramIgnoresNonPrivateMessagesAndButtons(t *testing.T) {
	t.Parallel()

	// Given a paired sender with a valid private menu button.
	data := t.TempDir()
	f := newFakeTelegram(t)
	startTelegram(t, f, data, 42)
	f.message(1, 42, 42, "/menu")
	menu := f.next(t)
	button := f.button(t, menu.ID, "Status")

	// When Telegram sends a group message, a group command, and a topic button.
	f.mu.Lock()
	for i, sessionType := range []string{"group", "supergroup"} {
		id := int64(i + 2)
		f.updates = append(f.updates, map[string]any{"update_id": id, "message": map[string]any{
			"message_id": id, "from": map[string]any{"id": 42},
			"chat": map[string]any{"id": -100 - i, "type": sessionType},
			"text": []string{"ignored group input", "/clear"}[i],
		}})
	}
	f.mu.Unlock()
	f.callback(4, 42, -101, 7, menu.ID, button)
	f.message(5, 42, 42, "private input")

	// Then polling advances without executing or replying to non-private updates.
	eventually(t, "non-private updates are acknowledged by polling", func() (bool, any) {
		offset := f.pollOffset.Load()
		return offset > 5, offset
	})
	if got := f.next(t); got.ChatID != 42 || got.Text != "echo: private input" {
		t.Fatalf("reply = %+v", got)
	}
	conn := controlWriteDB(t, data)
	var receipts int
	if err := conn.QueryRowContext(t.Context(), `
SELECT (SELECT count(*) FROM submissions WHERE origin LIKE 'telegram:-%')
     + (SELECT count(*) FROM channel_commands WHERE peer LIKE 'telegram:-%')
     + (SELECT count(*) FROM telegram_callbacks WHERE consumed_at IS NOT NULL)
`).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("non-private work receipts = %d, err = %v", receipts, err)
	}
}
