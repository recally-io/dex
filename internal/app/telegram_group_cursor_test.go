package app_test

import "testing"

func TestUserTelegramGroupFallbackConfirmationFailureDoesNotRegroup(t *testing.T) {
	t.Parallel()
	// Given a committed fallback transition, then remote acceptance without a stored member receipt.
	data := t.TempDir()
	f := newFakeTelegram(t)
	f.failures["sendMediaGroup"] = []platformFailure{{Code: 400}}
	d := startTelegram(t, f, data, 42)
	d.setModel(t, "files")
	conn := controlWriteDB(t, data)
	execSQL(t, conn, "CREATE TRIGGER hold_member_confirmation BEFORE UPDATE OF channel_group_cursor ON submissions WHEN NEW.channel_group_cursor=1 BEGIN SELECT RAISE(ABORT,'hold member confirmation'); END")
	telegramSource(t, data, "one.txt", []byte("one"))
	telegramSource(t, data, "two.txt", []byte("two"))
	f.message(1, 42, 7, `["one.txt","two.txt"]`)
	_ = nextTelegramGroup(t, f.groupAttempts)
	unknown := nextTelegramMedia(t, f.mediaSent)
	eventually(t, "fallback mode survives the failed member transaction", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:1' AND channel_group_fallback=1 AND channel_group_cursor=0 AND delivered_parts=1 AND json_array_length(channel_remote_ids)=1 AND channel_retry_at>0")
		return n == 1, n
	})
	d.stop()
	execSQL(t, conn, "DROP TRIGGER hold_member_confirmation")
	// When restored, only that unconfirmed member may repeat; the rejected group cannot run again.
	startDex(t, data, f.srv.URL)
	replay := nextTelegramMedia(t, f.mediaSent)
	if replay.Name != unknown.Name || replay.Body != unknown.Body {
		t.Fatalf("wrong fallback replay: %+v -> %+v", unknown, replay)
	}
	last := nextTelegramMedia(t, f.mediaSent)
	if last.Name == unknown.Name {
		t.Fatalf("member cursor did not advance: %+v", last)
	}
	eventually(t, "fallback cursor and IDs completed together", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:1' AND delivered_at IS NOT NULL AND channel_group_cursor=0 AND channel_group_fallback=0 AND json_array_length(channel_remote_ids)=3")
		return n == 1, n
	})
	select {
	case group := <-f.groupAttempts:
		t.Fatalf("fallback regrouped: %+v", group)
	default:
	}
}

func TestUserTelegramGroupFallbackStartsOnlyAfterModeCommit(t *testing.T) {
	t.Parallel()
	// Given a group rejection whose durable fallback transition cannot commit.
	data := t.TempDir()
	f := newFakeTelegram(t)
	for range 10 {
		f.failures["sendMediaGroup"] = append(f.failures["sendMediaGroup"], platformFailure{Code: 400})
	}
	d := startTelegram(t, f, data, 42)
	d.setModel(t, "files")
	conn := controlWriteDB(t, data)
	execSQL(t, conn, "CREATE TRIGGER hold_fallback_mode BEFORE UPDATE OF channel_group_fallback ON submissions WHEN NEW.channel_group_fallback=1 BEGIN SELECT RAISE(ABORT,'hold fallback mode'); END")
	telegramSource(t, data, "one.txt", []byte("one"))
	telegramSource(t, data, "two.txt", []byte("two"))
	f.message(1, 42, 7, `["one.txt","two.txt"]`)
	_ = nextTelegramGroup(t, f.groupAttempts)
	eventually(t, "mode write failure retains an unconfirmed group", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:1' AND channel_group_fallback=0 AND delivered_parts=1 AND channel_retry_at>0")
		return n == 1, n
	})
	select {
	case single := <-f.mediaAttempts:
		t.Fatalf("fallback ran before its mode committed: %+v", single)
	default:
	}
	// When the write is repaired, fallback is permitted and all member receipts complete.
	execSQL(t, conn, "DROP TRIGGER hold_fallback_mode")
	for range 2 {
		_ = nextTelegramMedia(t, f.mediaSent)
	}
	eventually(t, "repaired mode transition completed", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:1' AND delivered_at IS NOT NULL AND json_array_length(channel_remote_ids)=3")
		return n == 1, n
	})
}
