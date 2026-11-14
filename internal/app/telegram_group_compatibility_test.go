package app_test

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"testing"
)

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

func TestUserLegacyTelegramFileCursorDoesNotRegroupAfterRestart(t *testing.T) {
	t.Parallel()
	// Given an older receipt with confirmed text but no frozen rendering.
	data := t.TempDir()
	f := newFakeTelegram(t)
	f.failMedia.Store(true)
	d := startTelegram(t, f, data, 42)
	d.setModel(t, "files")
	telegramSource(t, data, "one.txt", []byte("one"))
	telegramSource(t, data, "two.txt", []byte("two"))
	f.message(1, 42, 7, `["one.txt","two.txt"]`)
	_ = nextTelegramGroup(t, f.groupAttempts)
	conn := controlWriteDB(t, data)
	eventually(t, "text confirmed before group retry", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:1' AND delivered_parts=1 AND channel_retry_at>0")
		return n == 1, n
	})
	d.stop()
	execSQL(t, conn, "UPDATE submissions SET rendered_reply='' WHERE source_key='telegram:123:1'")
	// When restored, the old per-file cursor must retain its part boundaries.
	f.failMedia.Store(false)
	startDex(t, data, f.srv.URL)
	for range 2 {
		if got := nextTelegramMedia(t, f.mediaSent); got.Method != "sendDocument" {
			t.Fatalf("legacy part=%+v", got)
		}
	}
	eventually(t, "legacy per-file cursor completed", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:1' AND delivered_at IS NOT NULL AND delivered_parts=3")
		return n == 1, n
	})
	select {
	case group := <-f.groupAttempts:
		t.Fatalf("legacy receipt regrouped: %+v", group)
	default:
	}
}

func TestUserTelegramGroupFloodWaitSurvivesRestart(t *testing.T) {
	t.Parallel()
	// Given a media-group flood wait stored for both the account and its reply.
	data := t.TempDir()
	f := newFakeTelegram(t)
	f.failures["sendMediaGroup"] = []platformFailure{{Code: 429, Retry: 3}}
	d := startTelegram(t, f, data, 42)
	d.setModel(t, "files")
	telegramSource(t, data, "one.txt", []byte("one"))
	telegramSource(t, data, "two.txt", []byte("two"))
	f.message(1, 42, 7, `["one.txt","two.txt"]`)
	_ = nextTelegramGroup(t, f.groupAttempts)
	conn := controlWriteDB(t, data)
	var until int64
	eventually(t, "stored group cooldown", func() (bool, any) {
		err := conn.QueryRowContext(t.Context(), "SELECT retry_at FROM telegram_settings WHERE agent_id='default'").Scan(&until)
		n := count(t, conn, "SELECT count(*) FROM submissions WHERE channel_retry_at>0 AND channel_group_fallback=0")
		return err == nil && until > 0 && n == 1, until
	})
	d.stop()
	startDex(t, data, f.srv.URL)
	if got := nextTelegramGroup(t, f.groupSent); got.At.UnixMilli() < until {
		t.Fatalf("group retried at %d before %d", got.At.UnixMilli(), until)
	}
	select {
	case single := <-f.mediaAttempts:
		t.Fatalf("429 caused fallback: %+v", single)
	default:
	}
}

func TestUserRevokedTelegramGroupCannotResumeAfterReauthorization(t *testing.T) {
	t.Parallel()
	// Given a frozen unconfirmed group for the old authorization.
	data := t.TempDir()
	f := newFakeTelegram(t)
	f.failMedia.Store(true)
	d := startTelegram(t, f, data, 42)
	d.setModel(t, "files")
	telegramSource(t, data, "one.txt", []byte("one"))
	telegramSource(t, data, "two.txt", []byte("two"))
	f.message(1, 42, 7, `["one.txt","two.txt"]`)
	_ = nextTelegramGroup(t, f.groupAttempts)
	d.must(t, http.MethodPut, "/api/agents/default/channels/telegram", map[string]any{"allowedUsers": []int64{99}}, nil)
	d.must(t, http.MethodPut, "/api/agents/default/channels/telegram", map[string]any{"allowedUsers": []int64{42}}, nil)
	// When access is granted again, only the newly requested file is eligible.
	f.failMedia.Store(false)
	telegramSource(t, data, "current.txt", []byte("new authorization"))
	f.message(2, 42, 7, `["current.txt"]`)
	if got := nextTelegramMedia(t, f.mediaSent); got.Name != "current.txt" {
		t.Fatalf("old group resumed: %+v", got)
	}
	conn := controlWriteDB(t, data)
	eventually(t, "revoked group retired without file bytes", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:1' AND channel_revoked=1 AND delivered_at IS NOT NULL")
		return n == 1, n
	})
	select {
	case group := <-f.groupSent:
		t.Fatalf("revoked group sent: %+v", group)
	default:
	}
}
