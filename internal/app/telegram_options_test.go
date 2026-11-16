package app_test

import (
	"net/http"
	"testing"
)

func TestUserTelegramPresentationPreferencesSurviveRestart(t *testing.T) {
	t.Parallel()
	// Given a configured bot whose default presentation has not been changed.
	data := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, data, 42)
	options := map[string]any{"silent": true, "linkPreview": "off", "preview": "off", "reactions": true}
	d.must(t, http.MethodPut, "/api/agents/default/channels/telegram", map[string]any{"token": "", "allowedUsers": []int64{42}, "options": options}, nil)
	// When the process restarts, presentation preferences remain separate from the token and allowlist.
	d.stop()
	d = startDex(t, data, f.srv.URL)
	var view struct {
		Telegram struct {
			Options struct {
				Silent      bool   `json:"silent"`
				LinkPreview string `json:"linkPreview"`
				Preview     string `json:"preview"`
				Reactions   bool   `json:"reactions"`
			} `json:"options"`
		} `json:"telegram"`
	}
	d.must(t, http.MethodGet, "/api/agents/default/channels", nil, &view)
	got := view.Telegram.Options
	if !got.Silent || got.LinkPreview != "off" || got.Preview != "off" || !got.Reactions {
		t.Fatalf("lost Telegram preferences: %+v", got)
	}
	// Existing clients that omit options must not reset the saved preferences.
	d.must(t, http.MethodPut, "/api/agents/default/channels/telegram", map[string]any{"token": "", "allowedUsers": []int64{42}}, nil)
	d.must(t, http.MethodGet, "/api/agents/default/channels", nil, &view)
	if !view.Telegram.Options.Silent {
		t.Fatal("legacy update reset presentation")
	}
}

func TestUserCannotEnableTelegramSpeechWithoutDataDisclosure(t *testing.T) {
	t.Parallel()
	// Given speech is off by default, provider selection alone does not authorize sending media.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	d.must(t, http.MethodPut, "/api/agents/default/channels/telegram", map[string]any{"token": "", "allowedUsers": []int64{42}, "options": map[string]any{"stt": map[string]any{"enabled": true, "provider": "private", "model": "transcriber"}}}, nil, http.StatusBadRequest)
}
