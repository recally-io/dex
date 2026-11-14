package app_test

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"
)

func holdTelegramAlbum(t *testing.T, d *dex) *sql.DB {
	t.Helper()
	conn := controlWriteDB(t, d.dataDir(t))
	execSQL(t, conn, "CREATE TRIGGER hold_album_seal BEFORE UPDATE OF state ON telegram_album_members WHEN NEW.state='sealed' BEGIN SELECT RAISE(ABORT,'hold album seal'); END")
	return conn
}

func waitTelegramAlbumStoredAndACKed(t *testing.T, f *fakeTelegram, conn *sql.DB) {
	t.Helper()
	eventually(t, "durable album members before polling ACK", func() (bool, any) {
		stored := count(t, conn, "SELECT count(*) FROM telegram_album_members WHERE state='collecting'")
		inputs := count(t, conn, "SELECT count(*) FROM submissions WHERE kind='message'")
		offset := f.pollOffset.Load()
		return stored == 2 && inputs == 0 && offset >= 3, []int64{int64(stored), int64(inputs), offset}
	})
}

func TestUserTelegramAlbumSurvivesACKAndRestartWithoutRedownload(t *testing.T) {
	t.Parallel()
	// Given two persisted member uploads that Telegram has ACKed, before the album is sealed.
	data := t.TempDir()
	f := newFakeTelegram(t)
	f.files["one.txt"], f.files["two.txt"] = "one", "two"
	d := startTelegram(t, f, data, 42)
	conn := holdTelegramAlbum(t, d)
	f.albumFiles("restart-album", 1, "one.txt", "two.txt")
	waitTelegramAlbumStoredAndACKed(t, f, conn)
	d.stop()
	execSQL(t, conn, "DROP TRIGGER hold_album_seal")
	// When the app restarts, SQLite restores the same members even though Telegram already accepted the offset.
	startDex(t, data, f.srv.URL)
	answer := f.next(t).Text
	if !strings.Contains(answer, "caption one.txt") || !strings.Contains(answer, "caption two.txt") {
		t.Fatalf("recovered album=%q", answer)
	}
	eventually(t, "upload ownership transferred to one native input", func() (bool, any) {
		transferred := count(t, conn, "SELECT count(*) FROM telegram_album_members WHERE state='transferred' AND input_json='{}'")
		inputs := count(t, conn, "SELECT count(*) FROM submissions WHERE kind='message'")
		return transferred == 2 && inputs == 1, []int{transferred, inputs}
	})
	if n := uploadCount(t, data); n != 2 {
		t.Fatalf("recovery duplicated uploads: %d", n)
	}
	downloads := 0
	for {
		select {
		case event := <-f.events:
			if event.Method == "getFile" {
				downloads++
			}
		default:
			if downloads != 2 {
				t.Fatalf("album downloaded %d files", downloads)
			}
			return
		}
	}
}

func TestUserLateTelegramAlbumMemberBecomesExplicitFollowup(t *testing.T) {
	t.Parallel()
	// Given an album whose first two files have already entered the native engine.
	f := newFakeTelegram(t)
	f.files["one.txt"], f.files["two.txt"], f.files["late.txt"] = "one", "two", "late"
	d := startTelegram(t, f, t.TempDir(), 42)
	f.albumFiles("late-album", 1, "one.txt", "two.txt")
	_ = f.next(t)
	// When one more member arrives after the first batch, it must not mutate or rerun that batch.
	f.albumFiles("late-album", 3, "late.txt")
	answer := f.next(t).Text
	if !strings.Contains(answer, "Late album continuation") || !strings.Contains(answer, "caption late.txt") || strings.Contains(answer, "caption one.txt") {
		t.Fatalf("late member=%q", answer)
	}
	conn := controlWriteDB(t, d.dataDir(t))
	if n := count(t, conn, "SELECT count(*) FROM submissions WHERE kind='message'"); n != 2 {
		t.Fatalf("late member created %d native intents", n)
	}
}

func TestUserStopDiscardsUnsubmittedTelegramAlbumAndItsUploads(t *testing.T) {
	t.Parallel()
	// Given a durable album that has not reached native admission.
	f := newFakeTelegram(t)
	f.files["one.txt"], f.files["two.txt"] = "one", "two"
	d := startTelegram(t, f, t.TempDir(), 42)
	conn := holdTelegramAlbum(t, d)
	f.albumFiles("stopped-album", 1, "one.txt", "two.txt")
	waitTelegramAlbumStoredAndACKed(t, f, conn)
	// When the user stops Main, its staged members are cancelled in the same control transaction.
	f.message(3, 42, 42, "/stop")
	if got := f.next(t).Text; !strings.Contains(got, "Stop requested") {
		t.Fatalf("stop acknowledgement=%q", got)
	}
	eventually(t, "cancelled album uploads released", func() (bool, any) { n := uploadCount(t, d.dataDir(t)); return n == 0, n })
	execSQL(t, conn, "DROP TRIGGER hold_album_seal")
	f.message(4, 42, 42, "after stop")
	if got := f.next(t).Text; got != "echo: after stop" {
		t.Fatalf("old album ran after Stop: %q", got)
	}
	if n := count(t, conn, "SELECT count(*) FROM submissions WHERE kind='message'"); n != 1 {
		t.Fatalf("stopped album created %d inputs", n)
	}
}

func TestUserRevokedTelegramAlbumDoesNotResumeOnReauthorization(t *testing.T) {
	t.Parallel()
	// Given a durable, unsubmitted album owned by the current authorized sender.
	f := newFakeTelegram(t)
	f.files["one.txt"], f.files["two.txt"] = "one", "two"
	d := startTelegram(t, f, t.TempDir(), 42)
	conn := holdTelegramAlbum(t, d)
	f.albumFiles("revoked-album", 1, "one.txt", "two.txt")
	waitTelegramAlbumStoredAndACKed(t, f, conn)
	// When access is revoked and later granted again, the old staged input remains discarded.
	d.must(t, http.MethodPut, "/api/agents/default/channels/telegram", map[string]any{"allowedUsers": []int64{99}}, nil)
	eventually(t, "revoked album uploads removed", func() (bool, any) { n := uploadCount(t, d.dataDir(t)); return n == 0, n })
	execSQL(t, conn, "DROP TRIGGER hold_album_seal")
	d.must(t, http.MethodPut, "/api/agents/default/channels/telegram", map[string]any{"allowedUsers": []int64{42}}, nil)
	f.message(3, 42, 42, "new permission")
	if got := f.next(t).Text; got != "echo: new permission" {
		t.Fatalf("revoked album resumed: %q", got)
	}
	if n := count(t, conn, "SELECT count(*) FROM telegram_album_members WHERE state='discarded' AND input_json='{}'"); n != 2 {
		t.Fatalf("revoked members=%d", n)
	}
}
