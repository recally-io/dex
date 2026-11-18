package app_test

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type telegramGroup struct {
	Files []telegramMedia
	IDs   []string
	At    time.Time
}

func (f *fakeTelegram) serveMediaGroup(w http.ResponseWriter, r *http.Request, failure platformFailure) {
	if err := r.ParseMultipartForm(21 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	var media []struct {
		Type    string `json:"type"`
		Media   string `json:"media"`
		Caption string `json:"caption"`
	}
	if err := json.Unmarshal([]byte(r.FormValue("media")), &media); err != nil || len(media) < 2 || len(media) > 10 {
		http.Error(w, "invalid media group", http.StatusBadRequest)
		return
	}
	group := telegramGroup{At: time.Now()}
	for _, item := range media {
		name, ok := strings.CutPrefix(item.Media, "attach://")
		if !ok {
			http.Error(w, "missing attachment", http.StatusBadRequest)
			return
		}
		file, header, err := r.FormFile(name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(io.LimitReader(file, (20<<20)+1))
		_ = file.Close()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		group.Files = append(group.Files, telegramMedia{Method: item.Type, Chat: r.FormValue("chat_id"), Topic: r.FormValue("message_thread_id"), Name: header.Filename, Body: string(body), At: group.At})
	}
	f.groupAttempts <- group
	if failure.Code != 0 || f.failMedia.Load() {
		code := failure.Code
		if code == 0 {
			code = 500
		}
		if failure.HTTPStatus != 0 {
			w.WriteHeader(failure.HTTPStatus)
		}
		_ = json.MarshalWrite(w, map[string]any{"ok": false, "error_code": code, "description": "test group failure", "parameters": map[string]any{"retry_after": failure.Retry}})
		return
	}
	var result []map[string]any
	f.mu.Lock()
	for range group.Files {
		f.nextID++
		group.IDs = append(group.IDs, fmt.Sprint(f.nextID))
		result = append(result, map[string]any{"message_id": f.nextID})
	}
	f.mu.Unlock()
	f.groupSent <- group
	if failure.Disconnect {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
		return
	}
	if failure.ShortResult {
		result = result[:1]
	}
	_ = json.MarshalWrite(w, map[string]any{"ok": true, "result": result})
}

func nextTelegramGroup(t *testing.T, messages <-chan telegramGroup) telegramGroup {
	t.Helper()
	select {
	case group := <-messages:
		return group
	case <-time.After(30 * time.Second):
		t.Fatal("no Telegram media group")
		return telegramGroup{}
	}
}

func TestUserReceivesNativeTelegramMediaGroup(t *testing.T) {
	t.Parallel()
	// Given two files explicitly delivered by one real native turn.
	data := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, data, 42)
	d.setModel(t, "files")
	telegramSource(t, data, "one.txt", []byte("first"))
	telegramSource(t, data, "two.txt", []byte("second"))
	f.message(1, 42, 7, `["one.txt","two.txt"]`)
	// When the reply is delivered, one native album confirms both remote message IDs.
	var group telegramGroup
	select {
	case group = <-f.groupSent:
	case single := <-f.mediaSent:
		t.Fatalf("album sent as separate %s: %s", single.Method, single.Name)
	case <-time.After(30 * time.Second):
		t.Fatal("no media group")
	}
	if len(group.Files) != 2 {
		t.Fatalf("group=%+v", group)
	}
	bodies := map[string]string{}
	for _, file := range group.Files {
		if file.Method != "document" || file.Chat != "7" {
			t.Fatalf("file=%+v", file)
		}
		bodies[file.Name] = file.Body
	}
	if bodies["one.txt"] != "first" || bodies["two.txt"] != "second" {
		t.Fatalf("bodies=%v", bodies)
	}
	conn := controlWriteDB(t, data)
	eventually(t, "one text and two group message confirmations", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:1' AND delivered_at IS NOT NULL AND delivered_parts=2 AND json_array_length(channel_remote_ids)=3")
		return n == 1, n
	})
}

func TestUserReceivesTelegramPhotoVideoAlbum(t *testing.T) {
	t.Parallel()
	// Given a valid photo and an MP4 result in one native turn.
	data := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, data, 42)
	d.setModel(t, "files")
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	telegramSource(t, data, "photo.png", imageBytes.Bytes())
	telegramSource(t, data, "video.mp4", []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"))
	f.message(1, 42, 7, `["photo.png","video.mp4"]`)
	group := nextTelegramGroup(t, f.groupSent)
	kinds := map[string]string{}
	for _, file := range group.Files {
		kinds[file.Name] = file.Method
	}
	if kinds["photo.png"] != "photo" || kinds["video.mp4"] != "video" {
		t.Fatalf("mixed visual album=%v", kinds)
	}
}

func TestUserUnsupportedTelegramAlbumKindsKeepNativeSingleMethods(t *testing.T) {
	t.Parallel()
	// Given voice and animation, which Telegram does not accept as InputMedia group types.
	data := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, data, 42)
	d.setModel(t, "files")
	telegramSource(t, data, "voice.ogg", []byte("OggS\x00\x00OpusHead"))
	telegramSource(t, data, "animation.gif", []byte("GIF89a\x02\x00\x02\x00"))
	f.message(1, 42, 7, `["voice.ogg","animation.gif"]`)
	methods := map[string]string{}
	for range 2 {
		file := nextTelegramMedia(t, f.mediaSent)
		methods[file.Name] = file.Method
	}
	if methods["voice.ogg"] != "sendVoice" || methods["animation.gif"] != "sendAnimation" {
		t.Fatalf("single native methods=%v", methods)
	}
	select {
	case group := <-f.groupAttempts:
		t.Fatalf("unsupported group=%+v", group)
	default:
	}
}
