package app_test

import (
	"testing"
	"time"
)

func TestUserTelegramLaterAnswerWaitsForEarlierPendingOutcomeOfSamePeer(t *testing.T) {
	t.Parallel()
	// Given a first input whose native answer finishes but whose business outcome cannot be written yet.
	data := t.TempDir()
	f := newFakeTelegram(t)
	startTelegram(t, f, data, 42, 43)
	conn := controlWriteDB(t, data)
	execSQL(t, conn, "CREATE TRIGGER hold_first_outcome BEFORE UPDATE OF status ON submissions WHEN OLD.source_key='telegram:123:1' AND NEW.status!='pending' BEGIN SELECT RAISE(ABORT,'hold first outcome'); END")
	f.message(1, 42, 42, "hi")
	f.message(1, 42, 42, "hi") // Telegram delivers an unconfirmed update again.
	f.message(2, 42, 42, "after")
	eventually(t, "later input of the same peer is answered", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:2' AND status='done'")
		return n == 1, n
	})
	// When another peer asks while the first peer is held, its answer is delivered first.
	f.message(3, 43, 43, "other")
	if got := f.next(t); got.ChatID != 43 || got.Text != "echo: other" {
		t.Fatalf("first delivery=%+v, want the other peer's answer while the held peer waits", got)
	}
	// Then the held peer gets its answers in input order after the first outcome is written.
	execSQL(t, conn, "DROP TRIGGER hold_first_outcome")
	for _, want := range []string{"echo: hi", "echo: after"} {
		if got := f.next(t); got.ChatID != 42 || got.Text != want {
			t.Fatalf("delivery=%+v, want %q", got, want)
		}
	}
	if n := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:1'"); n != 1 {
		t.Fatalf("duplicated update created %d inputs", n)
	}
}

func TestUserTelegramSilentOrStoppedInputDoesNotBlockFollowingReply(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct{ name, release string }{
		{"revoked", "UPDATE submissions SET channel_revoked=1 WHERE source_key='telegram:123:1'"},
		{"delivered", `INSERT INTO outbox_messages (id, event_id, agent_id, conversation_id, source_submission_id, kind, peer, status, created_at, delivered_at)
SELECT s.id, 'reply:' || c.agent_id || ':' || s.id, c.agent_id, c.id, s.id, 'reply', s.origin, 'sent', s.created_at, 1
FROM submissions s JOIN conversations c ON c.id = s.conversation_id WHERE s.source_key='telegram:123:1'`},
		{"unanswered", "BEGIN IMMEDIATE; DROP TRIGGER hold_first_outcome; UPDATE submissions SET status='unanswered', answer=NULL, reason='stopped' WHERE source_key='telegram:123:1'; COMMIT"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			// Given a delayed first receipt and a completed later answer at the same peer.
			data := t.TempDir()
			f := newFakeTelegram(t)
			startTelegram(t, f, data, 42, 43)
			conn := controlWriteDB(t, data)
			execSQL(t, conn, "CREATE TRIGGER hold_first_outcome BEFORE UPDATE OF status ON submissions WHEN OLD.source_key='telegram:123:1' AND NEW.status!='pending' BEGIN SELECT RAISE(ABORT,'hold first outcome'); END")
			f.message(1, 42, 42, "hi")
			f.message(2, 42, 42, "after")
			eventually(t, "later answer is ready", func() (bool, any) {
				n := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:2' AND status='done'")
				return n == 1, n
			})
			// When recovery records that the first receipt no longer needs a reply.
			execSQL(t, conn, scenario.release)
			f.message(3, 43, 43, "other")
			// Then both peers can receive answers without waiting for that receipt.
			after, other := false, false
			for !after || !other {
				got := f.next(t)
				after = after || got.ChatID == 42 && got.Text == "echo: after"
				other = other || got.ChatID == 43 && got.Text == "echo: other"
			}
		})
	}
}

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
		n := count(t, conn, "SELECT count(*) FROM outbox_messages WHERE kind='file' AND body='first.txt' AND retry_at>0")
		return n == 1, n
	})
	if _, err := conn.ExecContext(t.Context(), "UPDATE outbox_messages SET retry_at=? WHERE kind='file' AND body='first.txt'", time.Now().Add(time.Minute).UnixMilli()); err != nil {
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
	execSQL(t, conn, "UPDATE outbox_messages SET retry_at=0 WHERE kind='file' AND body='first.txt'")
	for _, name := range []string{"first.txt", "second.txt"} {
		if got := nextTelegramMedia(t, f.mediaSent); got.Name != name || got.Chat != "42" {
			t.Fatalf("file=%+v want=%s", got, name)
		}
	}
}
