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
