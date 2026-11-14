package app_test

import (
	"testing"
	"time"
)

func TestUserTelegramFileRetriesKeepPeerOrderWithoutBlockingOtherPeers(t *testing.T) {
	t.Parallel()
	// Given a failed file with a future retry time, and a later file for the same peer.
	data := t.TempDir()
	f := newFakeTelegram(t)
	f.failMedia.Store(true)
	d := startTelegram(t, f, data, 42, 43)
	d.setModel(t, "tool")
	telegramSource(t, data, "first.txt", []byte("first"))
	telegramSource(t, data, "second.txt", []byte("second"))
	telegramSource(t, data, "other.txt", []byte("other peer"))
	f.message(1, 42, 42, `deliver_file {"path":"first.txt"}`)
	_ = nextTelegramMedia(t, f.mediaAttempts)
	conn := controlWriteDB(t, data)
	eventually(t, "first file waits for retry", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:1' AND channel_retry_at>0")
		return n == 1, n
	})
	if _, err := conn.ExecContext(t.Context(), "UPDATE submissions SET channel_retry_at=? WHERE source_key='telegram:123:1'", time.Now().Add(time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	f.failMedia.Store(false)
	f.message(2, 42, 42, `deliver_file {"path":"second.txt"}`)
	eventually(t, "second file is ready", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:2' AND status='done'")
		return n == 1, n
	})
	// When a different authorized peer requests a file, its delivery can pass the paused peer.
	f.message(3, 43, 43, `deliver_file {"path":"other.txt"}`)
	if got := nextTelegramMedia(t, f.mediaSent); got.Name != "other.txt" || got.Chat != "43" {
		t.Fatalf("later same-peer file bypassed retry: %+v", got)
	}
	// Then releasing the first retry sends both files in order to their original peer.
	execSQL(t, conn, "UPDATE submissions SET channel_retry_at=0 WHERE source_key='telegram:123:1'")
	for _, name := range []string{"first.txt", "second.txt"} {
		if got := nextTelegramMedia(t, f.mediaSent); got.Name != name || got.Chat != "42" {
			t.Fatalf("file=%+v want=%s", got, name)
		}
	}
}
