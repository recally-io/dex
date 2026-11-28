package app_test

import (
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/sysops"
)

func TestUserTelegramAlbumMemberRetriesBeforeAcknowledgement(t *testing.T) {
	t.Parallel()
	for _, transport := range []string{"polling", "webhook"} {
		t.Run(transport, func(t *testing.T) {
			t.Parallel()
			// Given a database that rejects the album member after download.
			data := t.TempDir()
			f := newFakeTelegram(t)
			f.files["member.txt"] = "album member"
			d := startTelegram(t, f, data, 42)
			conn := controlWriteDB(t, data)
			execSQL(t, conn, `CREATE TRIGGER reject_album_member BEFORE INSERT ON telegram_album_members
WHEN NEW.source_key='telegram:123:1' BEGIN SELECT RAISE(ABORT, 'album member insert failed'); END`)
			members := func() int {
				return count(t, conn, `SELECT count(*) FROM telegram_album_members WHERE source_key='telegram:123:1'`)
			}
			pending := func() int {
				return count(t, conn, `SELECT count(*) FROM channel_webhook_queue WHERE platform='telegram' AND account='123' AND event_id='1' AND payload IS NOT NULL`)
			}

			// When Telegram sends the member, the failed write leaves it available for retry.
			if transport == "webhook" {
				const secret = "album-hook-secret"
				d.must(t, http.MethodPut, "/api/bots/default/channels/telegram", map[string]any{
					"allowedUsers": []int64{42}, "transport": "webhook",
					"webhook": map[string]string{"url": "https://hooks.example.test" + telegramHook, "secretToken": secret},
				}, nil)
				f.nextEvent(t, "setWebhook")
				postChannelHook(t, d, telegramHook, hookJSON(t, telegramAlbumUpdate("retry", 1, "member.txt")),
					map[string]string{"X-Telegram-Bot-Api-Secret-Token": secret}, http.StatusOK)
			} else {
				f.albumFiles("retry", 1, "member.txt")
			}
			eventually(t, "album download retried while its write fails", func() (bool, any) {
				if transport == "webhook" {
					if n := pending(); n != 1 {
						t.Fatalf("pending webhook rows = %d, want 1 while the write fails", n)
					}
				} else if offset := f.pollOffset.Load(); offset > 1 {
					t.Fatalf("polling offset = %d, acknowledged the failed update 1", offset)
				}
				n := f.fileRequests.Load()
				return n >= 2, n
			})
			if n := members(); n != 0 {
				t.Fatalf("stored album members = %d while the write fails", n)
			}

			// Then the same update stores one member and only then can intake finish.
			execSQL(t, conn, "DROP TRIGGER reject_album_member")
			eventually(t, "stored album member and completed intake", func() (bool, any) {
				n := members()
				if transport == "webhook" {
					finished := count(t, conn, `SELECT count(*) FROM channel_webhook_queue WHERE platform='telegram' AND account='123' AND event_id='1' AND payload IS NULL`)
					return n == 1 && finished == 1, []int{n, finished}
				}
				offset := f.pollOffset.Load()
				return n == 1 && offset >= 2, []int64{int64(n), offset}
			})
			if got := f.next(t); !strings.Contains(got.Text, "caption member.txt") {
				t.Fatalf("recovered album reply = %q", got.Text)
			}
		})
	}
}

func TestUserTelegramReplyRecoversAfterOutboxInsertFailure(t *testing.T) {
	// Given a database that rejects the reply insert during business settlement.
	// The process logger lets the test wait for a failed settlement, not just a native answer.
	logs := sysops.NewLogBuffer(100)
	prior := slog.Default()
	slog.SetDefault(slog.New(sysops.TeeHandler(slog.DiscardHandler, logs)))
	t.Cleanup(func() { slog.SetDefault(prior) })
	data := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, data, 42)
	conn := controlWriteDB(t, data)
	execSQL(t, conn, `CREATE TRIGGER reject_settlement_reply BEFORE INSERT ON outbox_messages
WHEN NEW.kind='reply' AND NEW.peer='telegram:42' BEGIN SELECT RAISE(ABORT, 'reply insert failed'); END`)

	// When native execution answers, the failed insert must also roll back the business receipt.
	f.message(1, 42, 42, "settlement retry")
	d.watch(t, d.mainSession(t)).until(t, "native answer", answered("echo: settlement retry"))
	eventually(t, "failed settlement reported", func() (bool, any) {
		entries := logs.Entries(100)
		return slices.ContainsFunc(entries, func(entry sysops.LogEntry) bool {
			return entry.Message == "reconcile execution receipt"
		}), entries
	})
	if n := count(t, conn, `SELECT count(*) FROM submissions WHERE source_key='telegram:123:1'
AND status='pending' AND admitted_at IS NOT NULL AND settled_at IS NULL AND answer IS NULL`); n != 1 {
		t.Fatalf("business receipt did not roll back after the reply insert failed: pending receipts = %d", n)
	}
	if rows, sends := count(t, conn, "SELECT count(*) FROM outbox_messages"), f.sendCount.Load(); rows != 0 || sends != 0 {
		t.Fatalf("outbox rows = %d, send attempts = %d before retry; want 0 for both", rows, sends)
	}

	// Then settlement retries the recorded outcome and delivers one reply.
	execSQL(t, conn, "DROP TRIGGER reject_settlement_reply")
	if got := f.next(t); got.ChatID != 42 || got.Text != "echo: settlement retry" {
		t.Fatalf("recovered reply = %+v", got)
	}
	eventually(t, "settled receipt with a confirmed reply", func() (bool, any) {
		n := count(t, conn, `SELECT count(*) FROM submissions s JOIN outbox_messages o ON o.source_submission_id=s.id
WHERE s.source_key='telegram:123:1' AND s.status='done' AND s.settled_at IS NOT NULL AND o.kind='reply' AND o.status='sent'`)
		return n == 1, n
	})
	d.stop()
	if rows, sends := count(t, conn, "SELECT count(*) FROM outbox_messages"), f.sendCount.Load(); rows != 1 || sends != 1 {
		t.Fatalf("outbox rows = %d, send attempts = %d after retry; want 1 for both", rows, sends)
	}
}
