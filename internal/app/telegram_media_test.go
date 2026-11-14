package app_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUserSendsTelegramVoiceAudioVideoAnimationAndSticker(t *testing.T) {
	for _, kind := range []struct{ field, label, name string }{
		{"voice", "Voice", "voice.ogg"}, {"audio", "Audio", "music.mp3"}, {"video", "Video", "clip.mp4"},
		{"video_note", "Video note", "circle.mp4"}, {"animation", "Animation", "animation.gif"}, {"sticker", "Sticker", "animated.tgs"},
	} {
		t.Run(kind.field, func(t *testing.T) {
			t.Parallel()
			// Given a media type that Telegram supplies as one file.
			f := newFakeTelegram(t)
			f.files[kind.name] = "opaque media bytes"
			data := t.TempDir()
			d := startTelegram(t, f, data, 42)
			// When an authorized user sends it, the file and its type enter the native transcript.
			f.mu.Lock()
			f.updates = append(f.updates, map[string]any{"update_id": int64(1), "message": map[string]any{
				"message_id": 1, "from": map[string]any{"id": 42}, "chat": map[string]any{"id": 42, "type": "private"}, "caption": "inspect",
				kind.field: map[string]any{"file_id": kind.name, "file_name": kind.name, "duration": 3, "emoji": "🙂", "is_animated": true},
			}})
			f.mu.Unlock()
			answer := f.next(t).Text
			if !strings.Contains(answer, kind.label) {
				t.Fatalf("media type lost: %q", answer)
			}
			if kind.field == "voice" && !strings.Contains(answer, "not configured") {
				t.Fatalf("voice claimed transcription without a provider: %q", answer)
			}
			watch := d.watch(t, d.mainChat(t))
			watch.until(t, "media in native input", func(tr *transcript) bool {
				for _, item := range tr.items {
					if item.Kind == "user" && len(item.Attachments) == 1 {
						return true
					}
				}
				return false
			})
			// Then the actual bytes remain readable through the bot-scoped upload path.
			for _, item := range watch.tr.items {
				if item.Kind != "user" || len(item.Attachments) != 1 {
					continue
				}
				file := item.Attachments[0]
				body, err := os.ReadFile(filepath.Join(data, "agents", "default", "workspace", file.Path))
				if err != nil || string(body) != "opaque media bytes" {
					t.Fatalf("media=%+v bytes=%q err=%v", file, body, err)
				}
			}
		})
	}
}

func TestUserTelegramQuoteAndForwardKeepTheirContextWithoutGrantingAuthority(t *testing.T) {
	t.Parallel()
	// Given an authorized sender quoting and forwarding content from another identity.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	f.mu.Lock()
	f.updates = append(f.updates, map[string]any{"update_id": int64(1), "message": map[string]any{
		"message_id": 1, "from": map[string]any{"id": 42}, "chat": map[string]any{"id": 42, "type": "private"}, "text": "explain this",
		"quote":            map[string]any{"text": "selected quotation"},
		"reply_to_message": map[string]any{"message_id": 77, "from": map[string]any{"id": 99, "first_name": "Quoted author"}, "text": "original quotation", "document": map[string]any{"file_id": "do-not-download"}},
		"forward_origin":   map[string]any{"type": "hidden_user", "sender_user_name": "Forwarded author"},
	}})
	f.mu.Unlock()
	// When the agent receives the input, it can see the supplied quote and forward context.
	answer := f.next(t).Text
	for _, want := range []string{"selected quotation", "Quoted author", "Forwarded author", "explain this"} {
		if !strings.Contains(answer, want) {
			t.Fatalf("missing %q in %q", want, answer)
		}
	}
	// Then only the real sender owns the request, and quoted attachments were not downloaded.
	conn := controlWriteDB(t, d.dataDir(t))
	if n := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:1' AND json_extract(channel_context,'$.senderId')='42'"); n != 1 {
		t.Fatalf("source identity count=%d", n)
	}
	for {
		select {
		case event := <-f.events:
			if event.Method == "getFile" {
				t.Fatal("quoted attachment was downloaded")
			}
		default:
			return
		}
	}
}

func TestUserGetsTelegramGFMWithoutUnsupportedHTML(t *testing.T) {
	t.Parallel()
	// Given Markdown that needs the installed Goldmark GFM nodes and Telegram-specific rendering.
	f := newFakeTelegram(t)
	startTelegram(t, f, t.TempDir(), 42)
	// When the model returns tasks, a table, ordered items, strikeout and a code language.
	f.message(1, 42, 42, "Formatting:\n\n- [x] finished\n- [ ] waiting\n\n| Name | Count |\n| --- | --- |\n| Dex | 2 |\n\n3. third\n4. fourth\n\n~~removed~~\n\n```go\nfmt.Println(1)\n```")
	got := f.next(t)
	for _, want := range []string{"☑", "☐", "Name", "Dex", "3.", "4.", "<s>removed</s>", `class="language-go"`} {
		if !strings.Contains(got.Text, want) {
			t.Fatalf("missing %q: %s", want, got.Text)
		}
	}
	for _, tag := range []string{"<table", "<input", "<ul", "<p>"} {
		if strings.Contains(got.Text, tag) {
			t.Fatalf("unsupported Telegram HTML: %s", got.Text)
		}
	}
}
