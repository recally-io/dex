package app_test

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestUserTelegramPhotoRejectionFallsBackToDocument(t *testing.T) {
	t.Parallel()
	// Given a photo that the Telegram photo endpoint rejects.
	data := t.TempDir()
	f := newFakeTelegram(t)
	f.failures["sendPhoto"] = []platformFailure{{Code: 400}}
	d := startTelegram(t, f, data, 42)
	d.setModel(t, "tool")
	var body bytes.Buffer
	if err := png.Encode(&body, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	telegramSource(t, data, "image.png", body.Bytes())
	// When the model delivers it, the document fallback keeps the original bytes and name.
	f.message(1, 42, 7, `deliver_file {"path":"image.png"}`)
	got := nextTelegramMedia(t, f.mediaSent)
	if got.Method != "sendDocument" || got.Name != "image.png" || got.Body != body.String() {
		t.Fatalf("fallback=%+v", got)
	}
}

func TestUserTelegramMediaFloodWaitSurvivesRestart(t *testing.T) {
	t.Parallel()
	// Given a native file send that receives a seven-second account flood wait.
	data := t.TempDir()
	f := newFakeTelegram(t)
	f.failures["sendDocument"] = []platformFailure{{Code: 429, Retry: 7}}
	d := startTelegram(t, f, data, 42)
	d.setModel(t, "tool")
	telegramSource(t, data, "report.txt", []byte("rate limited"))
	f.message(1, 42, 7, `deliver_file {"path":"report.txt"}`)
	conn := controlWriteDB(t, data)
	var until int64
	eventually(t, "durable account and reply retry windows", func() (bool, any) {
		err := conn.QueryRowContext(t.Context(), "SELECT retry_at FROM telegram_settings WHERE agent_id='default'").Scan(&until)
		n := count(t, conn, "SELECT count(*) FROM submissions WHERE channel_retry_at>0")
		return err == nil && until > 0 && n == 1, until
	})
	// When control restarts, it must not send again before the stored retry window.
	d.stop()
	startDex(t, data, f.srv.URL)
	got := nextTelegramMedia(t, f.mediaSent)
	if got.At.UnixMilli() < until || got.Body != "rate limited" {
		t.Fatalf("media sent at %d before %d: %+v", got.At.UnixMilli(), until, got)
	}
	select {
	case first := <-f.mediaCalls:
		if got.At.Sub(first) < 7*time.Second {
			t.Fatalf("retry delay=%s", got.At.Sub(first))
		}
	default:
		t.Fatal("missing API attempt")
	}
}

func TestUserRevokedTelegramFileDoesNotResumeAfterReauthorization(t *testing.T) {
	t.Parallel()
	// Given an unconfirmed file whose first send fails.
	data := t.TempDir()
	f := newFakeTelegram(t)
	f.failMedia.Store(true)
	d := startTelegram(t, f, data, 42)
	d.setModel(t, "tool")
	telegramSource(t, data, "private.txt", []byte("revoked payload"))
	f.message(1, 42, 7, `deliver_file {"path":"private.txt"}`)
	_ = nextTelegramMedia(t, f.mediaAttempts)
	// When the owner revokes the sender, its payload is removed in the settings transaction.
	d.must(t, http.MethodPut, "/api/agents/default/channels/telegram", map[string]any{"allowedUsers": []int64{99}}, nil)
	conn := controlWriteDB(t, data)
	if n := count(t, conn, "SELECT count(*) FROM channel_reply_files"); n != 0 {
		t.Fatalf("retained revoked payloads=%d", n)
	}
	// Reauthorization permits new work, not the revoked pending delivery.
	d.must(t, http.MethodPut, "/api/agents/default/channels/telegram", map[string]any{"allowedUsers": []int64{42}}, nil)
	f.failMedia.Store(false)
	telegramSource(t, data, "current.txt", []byte("new work"))
	f.message(2, 42, 7, `deliver_file {"path":"current.txt"}`)
	got := nextTelegramMedia(t, f.mediaSent)
	if got.Name != "current.txt" || got.Body != "new work" {
		t.Fatalf("revoked file resumed: %+v", got)
	}
	eventually(t, "retired revoked delivery", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:1' AND channel_revoked=1 AND delivered_at IS NOT NULL")
		return n == 1, n
	})
	select {
	case old := <-f.mediaSent:
		t.Fatalf("revoked file sent later: %+v", old)
	default:
	}
}

func TestUserMissingTelegramSnapshotIsNotReopenedFromArtifacts(t *testing.T) {
	t.Parallel()
	// Given an unconfirmed file whose snapshot is lost after a failed API call.
	data := t.TempDir()
	f := newFakeTelegram(t)
	f.failMedia.Store(true)
	d := startTelegram(t, f, data, 42)
	d.setModel(t, "tool")
	telegramSource(t, data, "old.txt", []byte("original artifact"))
	f.message(1, 42, 7, `deliver_file {"path":"old.txt"}`)
	_ = nextTelegramMedia(t, f.mediaAttempts)
	d.stop()
	conn := controlWriteDB(t, data)
	execSQL(t, conn, "DELETE FROM channel_reply_files")
	// When control restarts, a later file can deliver, but the missing bytes are not reloaded by name.
	f.failMedia.Store(false)
	d = startDex(t, data, f.srv.URL)
	telegramSource(t, data, "next.txt", []byte("next file"))
	f.message(2, 42, 7, `deliver_file {"path":"next.txt"}`)
	got := nextTelegramMedia(t, f.mediaSent)
	if got.Name != "next.txt" {
		t.Fatalf("reopened missing snapshot: %+v", got)
	}
	eventually(t, "explicit missing snapshot failure", func() (bool, any) {
		var reason string
		err := conn.QueryRowContext(t.Context(), "SELECT channel_delivery_error FROM submissions WHERE source_key='telegram:123:1'").Scan(&reason)
		return err == nil && strings.Contains(reason, "snapshot"), reason
	})
	if rows := listArtifacts(t, d, "default"); len(rows) != 2 {
		t.Fatalf("artifacts changed: %+v", rows)
	}
}
