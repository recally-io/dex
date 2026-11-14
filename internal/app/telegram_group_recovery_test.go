package app_test

import (
	"fmt"
	"net/http"
	"testing"
)

func TestUserRejectedTelegramGroupResumesOnlyUnconfirmedFallbackFiles(t *testing.T) {
	t.Parallel()
	// Given a rejected group whose first fallback member succeeds and the next member fails.
	data := t.TempDir()
	f := newFakeTelegram(t)
	f.failures["sendMediaGroup"] = []platformFailure{{Code: 400}}
	f.mediaFailAfter.Store(1)
	d := startTelegram(t, f, data, 42)
	d.setModel(t, "files")
	for _, name := range []string{"one.txt", "two.txt", "three.txt"} {
		telegramSource(t, data, name, []byte("original "+name))
	}
	f.message(1, 42, 7, `["one.txt","two.txt","three.txt"]`)
	_ = nextTelegramGroup(t, f.groupAttempts)
	first := nextTelegramMedia(t, f.mediaSent)
	conn := controlWriteDB(t, data)
	eventually(t, "first fallback member durably confirmed", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:1' AND channel_group_fallback=1 AND channel_group_cursor=1 AND delivered_parts=1 AND json_array_length(channel_remote_ids)=2 AND channel_retry_at>0")
		return n == 1, n
	})
	// When artifacts are deleted and the app restarts, the frozen bytes and fallback mode still apply.
	for _, name := range []string{"one.txt", "two.txt", "three.txt"} {
		d.must(t, http.MethodDelete, artifactPath("default", name), nil, nil)
	}
	d.stop()
	f.mediaFailAfter.Store(0)
	startDex(t, data, f.srv.URL)
	seen := map[string]bool{first.Name: true}
	for range 2 {
		got := nextTelegramMedia(t, f.mediaSent)
		if seen[got.Name] || got.Body != "original "+got.Name || got.Method != "sendDocument" {
			t.Fatalf("fallback replay=%+v, seen=%v", got, seen)
		}
		seen[got.Name] = true
	}
	eventually(t, "completed fallback with all remote IDs and released bytes", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:1' AND delivered_at IS NOT NULL AND delivered_parts=2 AND channel_group_cursor=0 AND channel_group_fallback=0 AND json_array_length(channel_remote_ids)=4")
		return n == 1, n
	})
	select {
	case group := <-f.groupAttempts:
		t.Fatalf("replayed a rejected group: %+v", group)
	default:
	}
	if n := count(t, conn, "SELECT count(*) FROM channel_reply_files"); n != 0 {
		t.Fatalf("retained payloads=%d", n)
	}
}

func TestUserUnknownTelegramGroupOutcomeRetriesWholeSnapshotWithoutFallback(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		failure platformFailure
	}{
		{"lost_response", platformFailure{Disconnect: true}},
		{"incomplete_ids", platformFailure{ShortResult: true}},
		{"server_error", platformFailure{Code: 500}},
		{"http_status_overrides_body", platformFailure{HTTPStatus: 500, Code: 400}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			// Given an unconfirmed group outcome: the response is lost, incomplete or fails at the service.
			data := t.TempDir()
			f := newFakeTelegram(t)
			f.failures["sendMediaGroup"] = []platformFailure{scenario.failure}
			d := startTelegram(t, f, data, 42)
			d.setModel(t, "files")
			telegramSource(t, data, "one.txt", []byte("one"))
			telegramSource(t, data, "two.txt", []byte("two"))
			f.message(1, 42, 7, `["one.txt","two.txt"]`)
			first := nextTelegramGroup(t, f.groupAttempts)
			conn := controlWriteDB(t, data)
			eventually(t, "group remains unconfirmed without a fallback transition", func() (bool, any) {
				n := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:1' AND channel_retry_at>0 AND delivered_parts=1 AND channel_group_cursor=0 AND channel_group_fallback=0 AND json_array_length(channel_remote_ids)=1 AND delivered_at IS NULL AND channel_delivery_error LIKE '%unknown%'")
				return n == 1, n
			})
			_ = f.next(t)
			d.stop()
			if scenario.failure.Code == 0 {
				_ = nextTelegramGroup(t, f.groupSent)
			}
			// When the app restarts, it repeats only the unconfirmed group, never individual methods or text.
			startDex(t, data, f.srv.URL)
			got := nextTelegramGroup(t, f.groupSent)
			if len(got.Files) != len(first.Files) {
				t.Fatalf("retry=%+v", got)
			}
			for i, file := range got.Files {
				if file.Name != first.Files[i].Name || file.Body != first.Files[i].Body {
					t.Fatalf("changed snapshot: %+v -> %+v", first, got)
				}
			}
			eventually(t, "whole group confirmed", func() (bool, any) {
				n := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:1' AND delivered_at IS NOT NULL AND json_array_length(channel_remote_ids)=3")
				return n == 1, n
			})
			select {
			case single := <-f.mediaAttempts:
				t.Fatalf("ambiguous group fell back: %+v", single)
			default:
			}
			select {
			case text := <-f.sent:
				t.Fatalf("confirmed text repeated: %+v", text)
			default:
			}
		})
	}
}

func TestUserTelegramGroupConfirmationFailureReplaysOnlyUnconfirmedGroup(t *testing.T) {
	t.Parallel()
	// Given remote group acceptance followed by a failed local cursor/ID transaction.
	data := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, data, 42)
	d.setModel(t, "files")
	conn := controlWriteDB(t, data)
	execSQL(t, conn, "CREATE TRIGGER hold_group_confirmation BEFORE UPDATE OF delivered_parts ON submissions WHEN NEW.delivered_parts=2 BEGIN SELECT RAISE(ABORT,'hold group confirmation'); END")
	telegramSource(t, data, "one.txt", []byte("one"))
	telegramSource(t, data, "two.txt", []byte("two"))
	f.message(1, 42, 7, `["one.txt","two.txt"]`)
	first := nextTelegramGroup(t, f.groupSent)
	eventually(t, "no partial confirmation after failed transaction", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:1' AND delivered_parts=1 AND channel_retry_at>0 AND json_array_length(channel_remote_ids)=1")
		return n == 1, n
	})
	d.stop()
	execSQL(t, conn, "DROP TRIGGER hold_group_confirmation")
	// When restarted, the unknown local confirmation repeats the same group, not a prefix or fallback.
	startDex(t, data, f.srv.URL)
	again := nextTelegramGroup(t, f.groupSent)
	if len(first.Files) != len(again.Files) {
		t.Fatal("group membership changed")
	}
	for i := range first.Files {
		if fmt.Sprint(first.Files[i].Name, first.Files[i].Body) != fmt.Sprint(again.Files[i].Name, again.Files[i].Body) {
			t.Fatal("group payload changed")
		}
	}
	eventually(t, "receipt transaction repaired", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:1' AND delivered_at IS NOT NULL AND json_array_length(channel_remote_ids)=3")
		return n == 1, n
	})
}
