package app_test

import "testing"

func TestUserReceivesNativeTelegramAudioVoiceVideoAndAnimation(t *testing.T) {
	for _, media := range []struct{ name, body, method string }{
		{"music.mp3", "ID3\x04\x00\x00\x00\x00\x00\x00music", "sendAudio"},
		{"voice.ogg", "OggS\x00\x00\x00\x00\x00\x00OpusHeadvoice", "sendVoice"},
		{"clip.mp4", "\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom", "sendVideo"},
		{"music.m4a", "\x00\x00\x00\x18ftypM4A \x00\x00\x00\x00mp42isom", "sendAudio"},
		{"animation.gif", "GIF89aanimation", "sendAnimation"},
	} {
		t.Run(media.method, func(t *testing.T) {
			t.Parallel()
			// Given an explicit result file in a supported native media container.
			data := t.TempDir()
			f := newFakeTelegram(t)
			d := startTelegram(t, f, data, 42)
			d.setModel(t, "tool")
			telegramSource(t, data, media.name, []byte(media.body))
			// When the model delivers it, the shared outbox uses the native Telegram method.
			f.message(1, 42, 42, `deliver_file {"path":"`+media.name+`"}`)
			got := nextTelegramMedia(t, f.mediaSent)
			if got.Method != media.method || got.Name != media.name || got.Body != media.body {
				t.Fatalf("native media=%+v want=%s", got, media.method)
			}
		})
	}
}
