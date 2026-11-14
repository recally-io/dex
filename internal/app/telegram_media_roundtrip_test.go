package app_test

import (
	"encoding/json/v2"
	"strings"
	"testing"
)

func TestUserTelegramVoiceUploadReturnsToItsOriginalPeer(t *testing.T) {
	t.Parallel()
	// Given an authorized user sending an Opus voice file to a bot with no speech service.
	data := t.TempDir()
	f := newFakeTelegram(t)
	body := "OggS\x00\x00\x00\x00\x00\x00OpusHeadoriginal voice bytes"
	f.files["voice.ogg"] = body
	d := startTelegram(t, f, data, 42)
	f.mu.Lock()
	f.updates = append(f.updates, map[string]any{"update_id": int64(1), "message": map[string]any{
		"message_id": 1, "from": map[string]any{"id": 42}, "chat": map[string]any{"id": 42, "type": "private"},
		"voice": map[string]any{"file_id": "voice.ogg", "duration": 3},
	}})
	f.mu.Unlock()
	if got := f.next(t).Text; !strings.Contains(got, "not configured") {
		t.Fatalf("voice was claimed transcribed: %q", got)
	}
	watch := d.watch(t, d.mainChat(t))
	path := ""
	watch.until(t, "voice upload in native input", func(tr *transcript) bool {
		for _, item := range tr.items {
			if item.Kind == "user" && len(item.Attachments) == 1 {
				path = item.Attachments[0].Path
				return true
			}
		}
		return false
	})
	// When a later native turn explicitly delivers that uploaded file, it uses the real artifact tool and sender.
	d.setModel(t, "tool")
	args, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	f.message(2, 42, 42, "deliver_file "+string(args))
	got := nextTelegramMedia(t, f.mediaSent)
	// Then the same peer receives the exact uploaded bytes as voice, without an STT/TTS or codec call.
	if got.Method != "sendVoice" || got.Chat != "42" || got.Name != "voice.ogg" || got.Body != body {
		t.Fatalf("voice round trip=%+v", got)
	}
}
