package app_test

import (
	"encoding/json/v2"
	"strings"
	"testing"
)

func TestUserRichTelegramEditCannotCreateMultipleMessages(t *testing.T) {
	t.Parallel()
	// Given a confirmed rich reply selected by its owned delivery ID.
	data := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, data, 42)
	f.message(1, 42, 42, "original")
	_ = f.next(t)
	conn := controlWriteDB(t, data)
	var delivery string
	eventually(t, "confirmed rich message", func() (bool, any) {
		err := conn.QueryRowContext(t.Context(), "SELECT id FROM submissions WHERE source_key='telegram:123:1' AND delivered_at IS NOT NULL").Scan(&delivery)
		return err == nil, delivery
	})
	d.setModel(t, "tool")
	// When an edit would exceed 500 blocks, no remote edit or extra split send is performed.
	args, _ := json.Marshal(map[string]any{"action": "edit", "deliveryId": delivery, "index": 0, "text": strings.Repeat("a\n\n", 501)})
	f.message(2, 42, 42, "telegram_message "+string(args))
	answer := f.next(t)
	if !strings.Contains(answer.Text, "rich edit must fit one message") {
		t.Fatalf("oversized edit=%+v", answer)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, request := range f.requests {
		if request.Method == "editMessageText" {
			t.Fatalf("oversized edit reached platform: %+v", request)
		}
	}
}
