package app_test

import (
	"encoding/json/v2"
	"fmt"
	"html"
	"net/http"
	"strings"
	"testing"
)

func TestUserTelegramSilentPreviewAndReactionPreferencesAffectDelivery(t *testing.T) {
	t.Parallel()
	// Given explicit silent delivery, hidden link previews and no streaming preview.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	d.must(t, http.MethodPut, "/api/agents/default/channels/telegram", map[string]any{"token": "", "allowedUsers": []int64{42}, "options": map[string]any{"silent": true, "linkPreview": "off", "preview": "off", "reactions": true}}, nil)
	f.message(1, 42, 42, "https://example.invalid/hello")
	got := f.next(t)
	if !got.Silent || got.LinkPreview == nil || !got.LinkPreview.Disabled {
		t.Fatalf("presentation=%+v", got)
	}
	// Then processing and terminal reactions refer to the incoming message, without draft or typing calls.
	seen := []string{}
	eventually(t, "processing and terminal reactions", func() (bool, any) {
		for {
			select {
			case event := <-f.events:
				if event.Method == "sendMessageDraft" || event.Method == "sendChatAction" {
					t.Errorf("preview off still sent %s", event.Method)
				}
				if event.Method == "setMessageReaction" {
					if event.MessageID != 1 {
						t.Errorf("reaction target=%d", event.MessageID)
					}
					seen = append(seen, event.Text)
				}
			default:
				return len(seen) >= 2, seen
			}
		}
	})
	if seen[0] != "👀" || seen[len(seen)-1] != "👍" {
		t.Fatalf("reaction lifecycle=%v", seen)
	}
}

func TestUserTelegramEditedMessageIsANewCorrection(t *testing.T) {
	t.Parallel()
	// Given a message whose first native input and answer have already settled.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	f.message(1, 42, 42, "first wording")
	_ = f.next(t)
	// When Telegram delivers an edit, the original native history is not rewritten or rolled back.
	f.mu.Lock()
	f.updates = append(f.updates, map[string]any{"update_id": int64(2), "edited_message": map[string]any{"message_id": 1, "from": map[string]any{"id": 42}, "chat": map[string]any{"id": 42, "type": "private"}, "text": "corrected wording"}})
	f.mu.Unlock()
	if got := f.next(t).Text; !strings.Contains(got, "Correction to Telegram message 1") || !strings.Contains(got, "corrected wording") {
		t.Fatalf("edit=%q", got)
	}
	conn := controlWriteDB(t, d.dataDir(t))
	if n := count(t, conn, "SELECT count(*) FROM submissions WHERE kind='message'"); n != 2 {
		t.Fatalf("correction inputs=%d", n)
	}
}

func TestUserTelegramMessageToolsUseOnlyCurrentPeerDeliveries(t *testing.T) {
	t.Parallel()
	// Given a confirmed bot reply and the same sender's later Telegram tool turn.
	data := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, data, 42)
	f.message(1, 42, 42, "original")
	original := f.next(t)
	conn := controlWriteDB(t, data)
	var delivery string
	eventually(t, "original delivery confirmed", func() (bool, any) {
		err := conn.QueryRowContext(t.Context(), "SELECT id FROM submissions WHERE source_key='telegram:123:1' AND delivered_at IS NOT NULL").Scan(&delivery)
		return err == nil, delivery
	})
	d.setModel(t, "tool")
	actions := []struct{ action, text, emoji, method string }{{"edit", "replacement", "", "editMessageText"}, {"react", "", "👍", "setMessageReaction"}, {"copy", "", "", "copyMessage"}, {"forward", "", "", "forwardMessage"}, {"delete", "", "", "deleteMessage"}}
	var copied struct {
		DeliveryID string   `json:"deliveryId"`
		MessageIDs []string `json:"messageIds"`
	}
	for i, action := range actions {
		args, _ := json.Marshal(map[string]any{"action": action.action, "deliveryId": delivery, "index": 0, "text": action.text, "emoji": action.emoji})
		f.message(int64(i+2), 42, 42, "telegram_message "+string(args))
		answer := html.UnescapeString(f.next(t).Text)
		if !strings.Contains(answer, `"status":"done"`) {
			t.Fatalf("%s result=%q", action.action, answer)
		}
		if action.action == "copy" {
			if err := json.Unmarshal([]byte(strings.TrimPrefix(answer, "tool: ")), &copied); err != nil {
				t.Fatal(err)
			}
		}
		event := f.nextEvent(t, action.method)
		if event.MessageID != original.ID {
			t.Fatalf("%s changed target: %d != %d", action.action, event.MessageID, original.ID)
		}
	}
	f.message(30, 42, 42, `telegram_message {"action":"list"}`)
	if got := html.UnescapeString(f.next(t).Text); !strings.Contains(got, copied.DeliveryID) || copied.DeliveryID == "" {
		t.Fatalf("copied delivery missing from list: %q", got)
	}
	copiedArgs, _ := json.Marshal(map[string]any{"action": "delete", "deliveryId": copied.DeliveryID, "index": 0})
	f.message(31, 42, 42, "telegram_message "+string(copiedArgs))
	if got := f.next(t).Text; !strings.Contains(got, "done") {
		t.Fatalf("copied message delete=%q", got)
	}
	copiedEvent := f.nextEvent(t, "deleteMessage")
	if fmt.Sprint(copiedEvent.MessageID) != copied.MessageIDs[0] {
		t.Fatalf("copied delete target=%+v", copiedEvent)
	}
	// A user-supplied delivery ID cannot select another peer, even under the same bot.
	args, _ := json.Marshal(map[string]any{"action": "delete", "deliveryId": delivery, "index": 0})
	f.message(99, 42, 99, "telegram_message "+string(args))
	if got := f.next(t).Text; !strings.Contains(got, "outside the current sender and peer") {
		t.Fatalf("foreign peer action=%q", got)
	}
	if n := count(t, conn, "SELECT count(*) FROM telegram_effects WHERE kind IN ('edit','react','copy','forward','delete') AND state='done'"); n != len(actions)+1 {
		t.Fatalf("operation receipts=%d", n)
	}
}

func TestUserTelegramCaptionEditSelectsConfirmedFileMessage(t *testing.T) {
	t.Parallel()
	// Given a native file with a confirmed document message ID.
	data := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, data, 42)
	d.setModel(t, "tool")
	telegramSource(t, data, "report.txt", []byte("synthetic report"))
	f.message(1, 42, 42, `deliver_file {"path":"report.txt"}`)
	_ = nextTelegramMedia(t, f.mediaSent)
	_ = f.next(t)
	conn := controlWriteDB(t, data)
	var delivery string
	eventually(t, "native document delivery confirmed", func() (bool, any) {
		err := conn.QueryRowContext(t.Context(), "SELECT id FROM submissions WHERE source_key='telegram:123:1' AND delivered_at IS NOT NULL").Scan(&delivery)
		return err == nil, delivery
	})
	args, _ := json.Marshal(map[string]any{"action": "edit_caption", "deliveryId": delivery, "index": 1, "text": "revised caption"})
	f.message(2, 42, 42, "telegram_message "+string(args))
	if got := f.next(t).Text; !strings.Contains(got, "done") {
		t.Fatalf("caption tool=%q", got)
	}
	event := f.nextEvent(t, "editMessageCaption")
	if event.Text != "revised caption" {
		t.Fatalf("caption=%+v", event)
	}
	args, _ = json.Marshal(map[string]any{"action": "edit_caption", "deliveryId": delivery, "index": 1, "text": ""})
	f.message(3, 42, 42, "telegram_message "+string(args))
	if got := f.next(t).Text; !strings.Contains(got, "done") {
		t.Fatalf("caption clear=%q", got)
	}
	if got := f.nextEvent(t, "editMessageCaption"); got.Text != "" {
		t.Fatalf("caption not cleared: %+v", got)
	}
}

func TestUserWebCannotUseTelegramMessageTools(t *testing.T) {
	t.Parallel()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	d.setModel(t, "tool")
	id := d.newSession(t).ID
	d.send(t, id, `telegram_message {"action":"list"}`)
	// A tool schema is not authority to use a Telegram connection from Web.
	watch := d.watch(t, id)
	watch.until(t, "Web Telegram tool refused", func(tr *transcript) bool {
		for _, item := range tr.items {
			if item.Kind == "assistant" && strings.Contains(item.Text, "active Telegram Main input") {
				return true
			}
		}
		return false
	})
}
