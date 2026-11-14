package app_test

import "testing"

func TestUserRejectedTelegramVideoKeepsOriginalBytesAsDocument(t *testing.T) {
	t.Parallel()
	// Given a file that Telegram cannot accept as a native video.
	data := t.TempDir()
	f := newFakeTelegram(t)
	f.failures["sendVideo"] = []platformFailure{{Code: 400}}
	d := startTelegram(t, f, data, 42)
	d.setModel(t, "tool")
	body := "\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"
	telegramSource(t, data, "clip.mp4", []byte(body))
	// When the model delivers it, a definitive rejection falls back without a codec or conversion service.
	f.message(1, 42, 42, `deliver_file {"path":"clip.mp4"}`)
	got := nextTelegramMedia(t, f.mediaSent)
	if got.Method != "sendDocument" || got.Name != "clip.mp4" || got.Body != body {
		t.Fatalf("media fallback changed bytes: %+v", got)
	}
}
