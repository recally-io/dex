package app_test

import (
	"encoding/json/v2"
	"strings"
	"testing"
)

func TestUserOldFrozenTelegramHTMLStaysHTMLAfterUpgrade(t *testing.T) {
	t.Parallel()
	// Given a settled reply frozen by an older version, before any send succeeds.
	data := t.TempDir()
	f := newFakeTelegram(t)
	f.failSend.Store(true)
	d := startTelegram(t, f, data, 42)
	f.message(1, 42, 42, "old answer")
	conn := controlWriteDB(t, data)
	eventually(t, "settled frozen reply awaits retry", func() (bool, any) {
		return count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:1' AND rendered_reply<>'' AND channel_retry_at>0") == 1, nil
	})
	d.stop()
	old := `<pre>Old | Table
Dex | 2</pre>`
	frozen, _ := json.Marshal([]map[string]string{{"text": old}})
	if _, err := conn.ExecContext(t.Context(), "UPDATE submissions SET rendered_reply=? WHERE source_key='telegram:123:1'", string(frozen)); err != nil {
		t.Fatal(err)
	}
	// When the new adapter restarts, it sends the exact old payload and does not re-render it.
	f.failSend.Store(false)
	d = startDex(t, data, f.srv.URL)
	got := f.next(t)
	if got.Text != old || got.RichHTML != "" || got.ParseMode != "HTML" {
		t.Fatalf("old frozen payload changed: %+v", got)
	}
	var delivery string
	eventually(t, "historical text confirmed", func() (bool, any) {
		err := conn.QueryRowContext(t.Context(), "SELECT id FROM submissions WHERE source_key='telegram:123:1' AND delivered_at IS NOT NULL").Scan(&delivery)
		return err == nil, delivery
	})
	// Its stored kind also keeps a plain edit plain; the new tool reply is rich.
	d.setModel(t, "tool")
	args, _ := json.Marshal(map[string]any{"action": "edit", "deliveryId": delivery, "index": 0, "text": "**literal legacy**"})
	f.message(2, 42, 42, "telegram_message "+string(args))
	answer := f.next(t)
	event := f.nextEvent(t, "editMessageText")
	if event.RichHTML != "" || event.Text != "**literal legacy**" || answer.RichHTML == "" {
		t.Fatalf("legacy control crossed formats: edit=%+v answer=%+v", event, answer)
	}
}

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
