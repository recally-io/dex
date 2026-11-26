package app_test

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func (f *fakeTelegram) callback(updateID, sender, session, topic, remote int64, data string) {
	f.lastCallback.Store(updateID)
	f.mu.Lock()
	defer f.mu.Unlock()
	kind := "private"
	if session < 0 {
		kind = "supergroup"
	}
	f.updates = append(f.updates, map[string]any{"update_id": updateID, "callback_query": map[string]any{"id": fmt.Sprint(updateID), "from": map[string]any{"id": sender}, "data": data, "message": map[string]any{"message_id": remote, "from": map[string]any{"id": 123, "is_bot": true}, "chat": map[string]any{"id": session, "type": kind, "is_forum": topic > 0}, "message_thread_id": topic}}})
}

func (f *fakeTelegram) button(t *testing.T, message int64, text string) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.keyboards[message] {
		for _, button := range row {
			if strings.Contains(button.Text, text) {
				if len(button.Data) > 64 {
					t.Fatalf("callback exceeds Telegram's byte limit: %d", len(button.Data))
				}
				return button.Data
			}
		}
	}
	t.Fatalf("no %q button on message %d: %+v", text, message, f.keyboards[message])
	return ""
}

func (f *fakeTelegram) callbackResult(t *testing.T) platformEvent {
	t.Helper()
	id := f.lastCallback.Load()
	for range 30 {
		event := f.nextEvent(t, "answerCallbackQuery")
		if event.MessageID == id {
			return event
		}
	}
	t.Fatalf("callback %d had no matching acknowledgement", id)
	return platformEvent{}
}

func TestUserTelegramCallbackExpiryRevocationAndClearGenerationSurviveRestart(t *testing.T) {
	t.Parallel()
	// Given durable menu buttons on an existing private Main.
	data := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, data, 42)
	f.message(1, 42, 7, "private first input")
	f.next(t)
	f.message(2, 42, 7, "/menu")
	menu := f.next(t)
	status := f.button(t, menu.ID, "Status")
	stop := f.button(t, menu.ID, "Stop")
	clearButton := f.button(t, menu.ID, "Clear")
	conn := controlWriteDB(t, data)
	// Expiry rejects the immutable action even while the sender is paired.
	if _, err := conn.ExecContext(t.Context(), "UPDATE telegram_callbacks SET expires_at=? WHERE action='/status'", time.Now().Add(-time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	f.callback(3, 99, 7, 0, menu.ID, clearButton)
	if event := f.callbackResult(t); event.Text == "Accepted." {
		t.Fatal("another sender used the button")
	}
	f.callback(4, 42, 7, 0, menu.ID, status)
	if event := f.callbackResult(t); event.Text == "Accepted." {
		t.Fatal("expired callback executed")
	}
	// A current revocation permanently cancels that button; regrant does not revive it.
	execSQL(t, conn, "UPDATE telegram_settings SET allowed_users='43' WHERE bot_id='default'")
	f.callback(5, 42, 7, 0, menu.ID, stop)
	if event := f.callbackResult(t); event.Text == "Accepted." {
		t.Fatal("revoked callback executed")
	}
	execSQL(t, conn, "UPDATE telegram_settings SET allowed_users='42' WHERE bot_id='default'")
	f.callback(6, 42, 7, 0, menu.ID, stop)
	if event := f.callbackResult(t); event.Text == "Accepted." {
		t.Fatal("regrant revived canceled button")
	}
	f.callback(7, 42, 7, 0, menu.ID, clearButton)
	if event := f.callbackResult(t); event.Text == "Accepted." {
		t.Fatal("re-pairing revived an unused button")
	}
	f.message(8, 42, 7, "/menu")
	menu = f.next(t)
	clearButton = f.button(t, menu.ID, "Clear")
	eventually(t, "menu receipt", func() (bool, any) {
		n := count(t, conn, `SELECT count(*) FROM channel_commands WHERE source_key = 'telegram:123:8' AND delivered_at IS NOT NULL`)
		return n == 1, n
	})
	// After restart, an unused button on the new binding still targets its Main generation.
	d.stop()
	startTelegram(t, f, data, 42)
	f.callback(9, 42, 7, 0, menu.ID, clearButton)
	if event := f.callbackResult(t); event.Text != "Accepted." {
		t.Fatalf("durable callback lost on restart: %+v", event)
	}
	if got := f.next(t); got.ChatID != 7 || got.TopicID != 0 || got.Text != "Context cleared." {
		t.Fatalf("clear target=%+v", got)
	}
	// Another unconsumed button from the old generation cannot act on the new native conversation.
	stale := f.button(t, menu.ID, "Settings")
	f.callback(10, 42, 7, 0, menu.ID, stale)
	if event := f.callbackResult(t); event.Text == "Accepted." {
		t.Fatal("old generation callback acted after clear")
	}
}

func TestUserTelegramCallbackAcknowledgementFailureKeepsPollingSafe(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name      string
		failure   platformFailure
		permanent bool
	}{
		{"rejected", platformFailure{Code: 400}, true},
		{"service failure", platformFailure{Code: 500}, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			f := newFakeTelegram(t)
			startTelegram(t, f, data, 42)
			f.message(1, 42, 7, "/menu")
			menu := f.next(t)
			status := f.button(t, menu.ID, "Status")
			f.mu.Lock()
			f.failures["answerCallbackQuery"] = []platformFailure{scenario.failure, scenario.failure}
			f.mu.Unlock()

			f.callback(2, 42, 7, 0, menu.ID, status)
			f.message(3, 42, 7, "after the button")

			answers, entered := 0, false
			for answers == 0 || !entered {
				got := f.next(t)
				if strings.Contains(got.Text, "Current conversation") {
					answers++
				}
				if got.Text == "echo: after the button" {
					entered = true
					f.mu.Lock()
					left := len(f.failures["answerCallbackQuery"])
					f.mu.Unlock()
					if want := map[bool]int{true: 1, false: 0}[scenario.permanent]; left != want {
						t.Fatalf("unused acknowledgement failures=%d, want %d", left, want)
					}
				}
			}
			var intents, consumed int
			conn := controlWriteDB(t, data)
			if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM channel_commands WHERE source_key LIKE 'telegram-callback:%'").Scan(&intents); err != nil {
				t.Fatal(err)
			}
			if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM telegram_callbacks WHERE consumed_at IS NOT NULL").Scan(&consumed); err != nil {
				t.Fatal(err)
			}
			if answers != 1 || intents != 1 || consumed != 1 {
				t.Fatalf("status answers=%d intents=%d consumed buttons=%d, want one each", answers, intents, consumed)
			}
		})
	}
}
