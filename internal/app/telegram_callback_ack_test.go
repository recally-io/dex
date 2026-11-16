package app_test

import (
	"strings"
	"testing"
)

func TestUserTelegramCallbackAcknowledgementFailureKeepsPollingSafe(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name      string
		failure   platformFailure
		permanent bool
	}{
		{"rejected", platformFailure{Code: 400}, true},
		{"forbidden", platformFailure{Code: 403}, true},
		{"service failure", platformFailure{Code: 500}, false},
		{"flood control", platformFailure{Code: 429, Retry: 1}, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			// Given a real signed Status button and two failing acknowledgements.
			data := t.TempDir()
			f := newFakeTelegram(t)
			startTelegram(t, f, data, 42)
			f.message(1, 42, 7, "/menu")
			menu := f.next(t)
			status := f.button(t, menu.ID, "Status")
			f.mu.Lock()
			f.failures["answerCallbackQuery"] = []platformFailure{scenario.failure, scenario.failure}
			f.mu.Unlock()

			// When the button press is followed by an ordinary message.
			f.callback(2, 42, 7, 0, menu.ID, status)
			f.message(3, 42, 7, "after the button")

			// Then the later input enters and the button's command runs once.
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
					// A definite rejection passes the update at once. A temporary failure retries
					// the same update, so both failures are used before the later input enters.
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
