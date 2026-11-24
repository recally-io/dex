package app_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestUserTelegramSilentPreviewAndReactionPreferencesAffectDelivery(t *testing.T) {
	t.Parallel()
	// Given explicit silent delivery and no streaming preview.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	d.must(t, http.MethodPut, "/api/agents/default/channels/telegram", map[string]any{"token": "", "allowedUsers": []int64{42}, "options": map[string]any{"silent": true, "preview": "off", "reactions": true}}, nil)
	f.message(1, 42, 42, "https://example.invalid/hello")
	got := f.next(t)
	if !got.Silent || got.RichHTML == "" {
		t.Fatalf("presentation=%+v", got)
	}
	// Then processing and terminal reactions refer to the incoming message, without draft or typing calls.
	seen := []string{}
	eventually(t, "processing and terminal reactions", func() (bool, any) {
		for {
			select {
			case event := <-f.events:
				if event.Method == "sendRichMessageDraft" || event.Method == "sendChatAction" {
					t.Errorf("preview off still sent %s", event.Method)
				}
				if event.Method == "setMessageReaction" {
					if event.MessageID != 1 {
						t.Errorf("reaction target=%d", event.MessageID)
					}
					seen = append(seen, event.Text)
				}
			default:
				return len(seen) >= 2 && seen[len(seen)-1] == "👍", seen
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

func TestUserTelegramPresentationPreferencesSurviveRestart(t *testing.T) {
	t.Parallel()
	// Given a configured bot whose default presentation has not been changed.
	data := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, data, 42)
	options := map[string]any{"silent": true, "preview": "off", "reactions": true}
	d.must(t, http.MethodPut, "/api/agents/default/channels/telegram", map[string]any{"token": "", "allowedUsers": []int64{42}, "options": options}, nil)
	// When the process restarts, presentation preferences remain separate from the token and allowlist.
	d.stop()
	d = startDex(t, data, f.srv.URL)
	var view struct {
		Telegram struct {
			Options struct {
				Silent    bool   `json:"silent"`
				Preview   string `json:"preview"`
				Reactions bool   `json:"reactions"`
			} `json:"options"`
		} `json:"telegram"`
	}
	d.must(t, http.MethodGet, "/api/agents/default/channels", nil, &view)
	got := view.Telegram.Options
	if !got.Silent || got.Preview != "off" || !got.Reactions {
		t.Fatalf("lost Telegram preferences: %+v", got)
	}
	// Existing clients that omit options must not reset the saved preferences.
	d.must(t, http.MethodPut, "/api/agents/default/channels/telegram", map[string]any{"token": "", "allowedUsers": []int64{42}}, nil)
	d.must(t, http.MethodGet, "/api/agents/default/channels", nil, &view)
	if !view.Telegram.Options.Silent {
		t.Fatal("omitted options reset presentation")
	}
}
