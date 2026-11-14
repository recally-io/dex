package app_test

import (
	"strings"
	"testing"
)

func (f *fakeTelegram) albumFiles(group string, firstID int64, names ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, name := range names {
		id := firstID + int64(i)
		f.updates = append(f.updates, map[string]any{"update_id": id, "message": map[string]any{
			"message_id": id, "from": map[string]any{"id": 42}, "chat": map[string]any{"id": 42, "type": "private"}, "media_group_id": group, "caption": "caption " + name,
			"document": map[string]any{"file_id": name, "file_name": name},
		}})
	}
}

func TestUserTelegramAlbumBecomesOneNativeInput(t *testing.T) {
	t.Parallel()
	// Given two files that Telegram delivers as separate members of one media group.
	f := newFakeTelegram(t)
	f.files["one.txt"], f.files["two.txt"] = "first file", "second file"
	d := startTelegram(t, f, t.TempDir(), 42)
	f.albumFiles("album-one", 1, "one.txt", "two.txt")
	// When the adapter closes the collection window, one native input retains both captions and files.
	answer := f.next(t).Text
	if !strings.Contains(answer, "caption one.txt") || !strings.Contains(answer, "caption two.txt") {
		t.Fatalf("album split before native admission: %q", answer)
	}
	watch := d.watch(t, d.mainChat(t))
	watch.until(t, "one native album input", func(tr *transcript) bool {
		users := 0
		for _, item := range tr.items {
			if item.Kind != "user" {
				continue
			}
			users++
			if len(item.Attachments) != 2 {
				return false
			}
		}
		return users == 1
	})
}

func TestUserAnswersTelegramQuestionWithAlbumCaption(t *testing.T) {
	t.Parallel()
	// Given an open native Question sent to the album's peer.
	f := newFakeTelegram(t)
	f.files["one.txt"], f.files["two.txt"] = "one", "two"
	d := startTelegram(t, f, t.TempDir(), 42)
	d.setModel(t, "tool")
	f.message(1, 42, 42, askJSON)
	asked := f.next(t)
	main := d.mainChat(t)
	_ = d.awaitQuestion(t, main, "open")
	d.setModel(t, "echo")
	// When the user quotes the Question with an album and one exact option caption.
	f.mu.Lock()
	for i, name := range []string{"one.txt", "two.txt"} {
		id := int64(i + 2)
		msg := map[string]any{"message_id": id, "from": map[string]any{"id": 42}, "chat": map[string]any{"id": 42, "type": "private"}, "media_group_id": "question-album", "document": map[string]any{"file_id": name, "file_name": name}}
		if i == 0 {
			msg["caption"] = "Start a separate session"
			msg["reply_to_message"] = map[string]any{"message_id": asked.ID}
		}
		f.updates = append(f.updates, map[string]any{"update_id": id, "message": msg})
	}
	f.mu.Unlock()
	answer := f.next(t).Text
	if !strings.Contains(answer, "Answer to: How should I handle the banner flicker?") || strings.Contains(answer, "File 1 (one.txt):") {
		t.Fatalf("album changed the raw Question option: %q", answer)
	}
	d.awaitNoQuestion(t, main)
}

func TestUserTelegramAlbumCaptionCannotExecuteSharedCommand(t *testing.T) {
	t.Parallel()
	// Given a file whose album caption is also a shared command.
	f := newFakeTelegram(t)
	f.files["one.txt"] = "one"
	_ = startTelegram(t, f, t.TempDir(), 42)
	f.mu.Lock()
	f.updates = append(f.updates, map[string]any{"update_id": int64(1), "message": map[string]any{"message_id": int64(1), "from": map[string]any{"id": 42}, "chat": map[string]any{"id": 42, "type": "private"}, "media_group_id": "caption-command", "caption": "/clear", "document": map[string]any{"file_id": "one.txt", "file_name": "one.txt"}}})
	f.mu.Unlock()
	// When the batch arrives, the caption remains input data and does not reset Main.
	if got := f.next(t).Text; !strings.Contains(got, "echo: /clear") || !strings.Contains(got, "one.txt") {
		t.Fatalf("album caption executed a command: %q", got)
	}
}
