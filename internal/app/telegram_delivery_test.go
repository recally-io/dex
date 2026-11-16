package app_test

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type telegramMedia struct {
	Method, Chat, Name, Body string
	At                       time.Time
}

func nextTelegramMedia(t *testing.T, messages <-chan telegramMedia) telegramMedia {
	t.Helper()
	select {
	case result := <-messages:
		return result
	case <-time.After(30 * time.Second):
		t.Fatal("no native Telegram file delivery")
		return telegramMedia{}
	}
}

func telegramSource(t *testing.T, data, name string, body []byte) {
	t.Helper()
	dir := filepath.Join(data, "agents", "default", "workspace")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), body, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestUserReceivesNativeTelegramFile(t *testing.T) {
	t.Parallel()
	// Given an authorized Telegram user and a file in the bot workspace.
	data := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, data, 42)
	d.setModel(t, "tool")
	telegramSource(t, data, "report.csv", []byte("name,count\ndex,2\n"))
	// When the agent explicitly delivers the file for this Telegram input.
	f.message(1, 42, 7, `deliver_file {"path":"report.csv"}`)
	// Then Telegram receives the bytes as a document, and Artifacts keeps its disk entry.
	media := nextTelegramMedia(t, f.mediaSent)
	if media.Method != "sendDocument" || media.Chat != "7" || media.Name != "report.csv" || media.Body != "name,count\ndex,2\n" {
		t.Fatalf("media=%+v", media)
	}
	if rows := listArtifacts(t, d, "default"); len(rows) != 1 || rows[0].Name != "report.csv" {
		t.Fatalf("artifacts=%+v", rows)
	}
	conn := controlWriteDB(t, data)
	eventually(t, "confirmed message IDs and released delivery payload", func() (bool, any) {
		confirmed := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:1' AND delivered_at IS NOT NULL AND json_array_length(channel_remote_ids)=2")
		payloads := count(t, conn, "SELECT count(*) FROM channel_reply_files")
		return confirmed == 1 && payloads == 0, []int{confirmed, payloads}
	})
}

func TestUserTelegramMediaRetryUsesQueuedBytesAfterRestart(t *testing.T) {
	t.Parallel()
	// Given a file whose first native Telegram delivery fails.
	data := t.TempDir()
	f := newFakeTelegram(t)
	f.failMedia.Store(true)
	d := startTelegram(t, f, data, 42)
	d.setModel(t, "tool")
	telegramSource(t, data, "report.txt", []byte("original bytes"))
	f.message(1, 42, 7, `deliver_file {"path":"report.txt"}`)
	_ = nextTelegramMedia(t, f.mediaAttempts)
	_ = f.next(t)
	// When the user renames and deletes the artifact, then puts different bytes at its old name.
	d.must(t, http.MethodPatch, artifactPath("default", "report.txt"), map[string]string{"name": "moved.txt"}, nil)
	d.must(t, http.MethodDelete, artifactPath("default", "moved.txt"), nil, nil)
	if err := os.WriteFile(filepath.Join(data, "agents", "default", "artifacts", "report.txt"), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	d.stop()
	f.failMedia.Store(false)
	startDex(t, data, f.srv.URL)
	// Then the unconfirmed part resumes from its snapshot, not the new artifact or workspace file.
	media := nextTelegramMedia(t, f.mediaSent)
	if media.Body != "original bytes" || media.Name != "report.txt" {
		t.Fatalf("retried=%+v", media)
	}
	select {
	case repeated := <-f.sent:
		t.Fatalf("confirmed text repeated after restart: %+v", repeated)
	default:
	}
}

func TestUserReceivesNativeTelegramPhoto(t *testing.T) {
	t.Parallel()
	// Given a valid PNG result in the workspace.
	data := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, data, 42)
	d.setModel(t, "tool")
	var body bytes.Buffer
	if err := png.Encode(&body, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	telegramSource(t, data, "image.png", body.Bytes())
	// When the model explicitly delivers it, Telegram gets a photo rather than a text URL.
	f.message(1, 42, 7, `deliver_file {"path":"image.png"}`)
	media := nextTelegramMedia(t, f.mediaSent)
	if media.Method != "sendPhoto" || media.Body != body.String() {
		t.Fatalf("photo=%+v", media)
	}
}

func TestUserWebArtifactDoesNotBroadcastToTelegram(t *testing.T) {
	t.Parallel()
	// Given the same bot with a Telegram connection and a separate Web conversation.
	data := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, data, 42)
	telegramSource(t, data, "private.txt", []byte("web only"))
	// When a Web turn delivers a file, it keeps the existing artifact behavior.
	_ = deliverArtifact(t, d, d.newSession(t).ID, "private.txt")
	// Then no native media send was triggered by that completed turn.
	select {
	case got := <-f.mediaAttempts:
		t.Fatalf("Web delivery broadcast to Telegram: %+v", got)
	default:
	}
}
