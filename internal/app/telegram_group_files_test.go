package app_test

import (
	"encoding/base64"
	"html"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func (f *fakeTelegram) groupAlbum(group string, firstID, topic int64, files ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, file := range files {
		id := firstID + int64(i)
		message := map[string]any{"message_id": id, "from": map[string]any{"id": 42}, "chat": map[string]any{"id": int64(-100), "type": "supergroup", "is_forum": true}, "message_thread_id": topic, "media_group_id": group, "document": map[string]any{"file_id": file, "file_name": file}}
		if i == 0 {
			message["caption"] = "@dex_test_bot public album"
		}
		f.updates = append(f.updates, map[string]any{"update_id": id, "message": message})
	}
}

func TestUserTelegramGroupAlbumKeepsAllMembersInTopicAudience(t *testing.T) {
	// Given an album whose first member mentions the bot and later members have no trigger text.
	directory := t.TempDir()
	f := newFakeTelegram(t)
	f.files["one.txt"], f.files["two.txt"] = "first public file", "second public file"
	startTelegram(t, f, directory, 42)
	conn := controlWriteDB(t, directory)
	// When collection persists and admits the album, both files use one scope and one native input.
	f.groupAlbum("public-group-album", 1, 7, "one.txt", "two.txt")
	got := f.next(t)
	if got.ChatID != -100 || got.TopicID != 7 || !strings.Contains(got.Text, "one.txt") || !strings.Contains(got.Text, "two.txt") {
		t.Fatalf("group album split or leaked: %+v", got)
	}
	if n := count(t, conn, "SELECT count(*) FROM submissions s JOIN conversations c ON c.id=s.conversation_id WHERE c.execution_scope='tg_123_-100_7' AND s.kind='message'"); n != 1 {
		t.Fatalf("native album inputs=%d", n)
	}
	files, err := filepath.Glob(filepath.Join(directory, "agents", "default", "telegram-groups", "tg_123_-100_7", "resources", "uploads", "*", "*.txt"))
	if err != nil || len(files) != 2 || uploadCount(t, directory) != 0 {
		t.Fatalf("album ownership: group=%v private=%d err=%v", files, uploadCount(t, directory), err)
	}
}

func TestUserTelegramGroupPublishesOnlyAudienceFilesToItsTopic(t *testing.T) {
	// Given one explicitly published group file and a private bot workspace file.
	directory := t.TempDir()
	resources := filepath.Join(directory, "agents", "default", "telegram-groups", "tg_123_-100_7", "resources")
	workspace := filepath.Join(directory, "agents", "default", "workspace")
	for _, dir := range []string{resources, workspace} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(resources, "published.txt"), []byte("PUBLIC_GROUP_FILE"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "private.txt"), []byte("PRIVATE_FILE_MUST_NOT_BE_SHARED"), 0600); err != nil {
		t.Fatal(err)
	}
	f := newFakeTelegram(t)
	d := startTelegram(t, f, directory, 42)
	d.setModel(t, "direct")
	// When the group queues its published file, only its topic receives those frozen bytes.
	f.groupText(1, 42, -100, 7, `group_deliver {"path":"published.txt"}`)
	if got := f.next(t); !strings.Contains(got.Text, "queued") || got.ChatID != -100 || got.TopicID != 7 {
		t.Fatalf("group delivery receipt: %+v", got)
	}
	select {
	case file := <-f.mediaSent:
		if file.Chat != "-100" || file.Topic != "7" || file.Name != "published.txt" || file.Body != "PUBLIC_GROUP_FILE" {
			t.Fatalf("wrong public file destination: %+v", file)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("group file was not delivered")
	}
	// Then the generic private sharing path remains unavailable and does not copy private bytes.
	f.groupText(2, 42, -100, 7, `deliver_file {"path":"private.txt"}`)
	if got := f.next(t); strings.Contains(got.Text, "PRIVATE_FILE_MUST_NOT_BE_SHARED") || !strings.Contains(got.Text, "tool error") {
		t.Fatalf("private sharing path available: %+v", got)
	}
	artifacts, err := os.ReadDir(filepath.Join(directory, "agents", "default", "artifacts"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(artifacts) != 0 || f.mediaSuccesses.Load() != 1 {
		t.Fatalf("private artifact fallback: artifacts=%v sent=%d", artifacts, f.mediaSuccesses.Load())
	}
}

func TestUserTelegramGroupReadsPublishedImageWithoutPrivateFilesystem(t *testing.T) {
	// Given an explicitly published image in one audience, without a process or generic file tool.
	directory := t.TempDir()
	resources := filepath.Join(directory, "agents", "default", "telegram-groups", "tg_123_-100_7", "resources")
	if err := os.MkdirAll(resources, 0700); err != nil {
		t.Fatal(err)
	}
	const encoded = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jB1sAAAAASUVORK5CYII="
	body, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resources, "public.png"), body, 0600); err != nil {
		t.Fatal(err)
	}
	f := newFakeTelegram(t)
	d := startTelegram(t, f, directory, 42)
	d.setModel(t, "direct-content")
	// When the model reads that scoped resource, native tool content carries the image bytes.
	f.groupText(1, 42, -100, 7, `group_resource {"path":"public.png"}`)
	got := html.UnescapeString(f.next(t).Text)
	if !strings.Contains(got, `"type":"image"`) || !strings.Contains(got, `"mimeType":"image/png"`) || !strings.Contains(got, encoded) {
		t.Fatalf("image content missing at model boundary: %s", got)
	}
}

func TestUserTelegramGroupStopDiscardsOnlyOwnedPendingAlbums(t *testing.T) {
	// Given staged albums in private Main and two group topics, held before sealing.
	directory := t.TempDir()
	f := newFakeTelegram(t)
	f.files["a.txt"], f.files["b.txt"], f.files["private.txt"] = "topic7", "topic8", "private"
	startTelegram(t, f, directory, 42)
	conn := controlWriteDB(t, directory)
	execSQL(t, conn, "CREATE TRIGGER hold_group_albums BEFORE UPDATE OF state ON telegram_album_members WHEN NEW.state='sealed' BEGIN SELECT RAISE(FAIL,'hold album seal'); END")
	defer execSQL(t, conn, "DROP TRIGGER IF EXISTS hold_group_albums")
	f.groupAlbum("topic7", 1, 7, "a.txt")
	f.groupAlbum("topic8", 2, 8, "b.txt")
	f.albumFiles("private", 3, "private.txt")
	eventually(t, "three staged audiences", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM telegram_album_members WHERE state='collecting'")
		return n == 3, n
	})
	// When a group actor stops the current topic, neither another topic nor Main loses its pending file.
	f.groupText(4, 42, -100, 7, "/stop")
	eventually(t, "only current topic discarded", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM telegram_album_members WHERE peer='telegram:-100:7' AND state='discarded'")
		return n == 1, n
	})
	if n := count(t, conn, "SELECT count(*) FROM telegram_album_members WHERE state='collecting'"); n != 2 {
		t.Fatalf("stop crossed an album audience: collecting=%d", n)
	}
	// When private Main is stopped, the remaining group topic must still retain its staged album.
	f.message(5, 42, 42, "/stop")
	eventually(t, "private album discarded", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM telegram_album_members WHERE peer='telegram:42' AND state='discarded'")
		return n == 1, n
	})
	if n := count(t, conn, "SELECT count(*) FROM telegram_album_members WHERE peer='telegram:-100:8' AND state='collecting'"); n != 1 {
		t.Fatalf("Main stop crossed into group album: collecting=%d", n)
	}
	eventually(t, "discarded group upload removed from its own root", func() (bool, any) {
		paths, err := filepath.Glob(filepath.Join(directory, "agents", "default", "telegram-groups", "tg_123_-100_7", "resources", "uploads", "*", "a.txt"))
		return err == nil && len(paths) == 0, paths
	})
	files, err := filepath.Glob(filepath.Join(directory, "agents", "default", "telegram-groups", "tg_123_-100_8", "resources", "uploads", "*", "b.txt"))
	if err != nil || len(files) != 1 {
		t.Fatalf("other topic upload removed: files=%v err=%v", files, err)
	}
}
