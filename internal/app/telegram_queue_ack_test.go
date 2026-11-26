package app_test

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestUserTelegramQueuedAcknowledgementRetriesWhileWaiting(t *testing.T) {
	t.Parallel()
	// Given a busy conversation and a Telegram rate limit on the first acknowledgement.
	data := t.TempDir()
	releaseRetry := make(chan struct{})
	gate := &reactionGate{calls: make(chan reactionCall, 20), retryAfter: 2, queuedTimes: make(chan time.Time, 20), holdQueuedRetry: releaseRetry}
	gate.limitNextQueued.Store(true)
	f := newFakeTelegram(t)
	f.reactGate = gate
	d := startTelegram(t, f, data, 42)
	d.setModel(t, "bash")
	d.send(t, d.mainSession(t), taskGate(t, data, "earlier"))
	waitTaskFile(t, filepath.Join(data, "earlier.pid"))

	// When Telegram sends the input and then repeats the update.
	f.message(1, 42, 42, "printf waiting")
	gate.expect(t, "1", "👀")
	f.message(1, 42, 42, "printf waiting")
	gate.expect(t, "1", "👀")
	first, second := <-gate.queuedTimes, <-gate.queuedTimes
	if elapsed := second.Sub(first); elapsed < 2*time.Second {
		t.Fatalf("retried before retry_after: %s", elapsed)
	}

	// Then an in-flight retry has an unknown outcome until Telegram accepts it.
	conn := controlWriteDB(t, data)
	if got, err := telegramQueueAck(t, conn, 1); err != nil || got != "unknown" {
		t.Fatalf("queued acknowledgement = %q, %v; want unknown", got, err)
	}
	close(releaseRetry)
	waitTelegramQueueAck(t, conn, 1, "accepted")
	releaseResultGate(t, data, "earlier")
	if got := f.next(t).Text; got != "bash: waiting" {
		t.Fatalf("reply = %q", got)
	}
	waitTelegramReaction(t, gate, "1", "👍")

	// When Dex restarts and Telegram replays the update, no old acknowledgement is sent.
	d.stop()
	startDex(t, data, f.srv.URL)
	f.message(1, 42, 42, "printf waiting")
	f.message(2, 42, 42, "printf next")
	if got := f.next(t).Text; got != "bash: next" {
		t.Fatalf("reply after restart = %q", got)
	}
	waitTelegramReaction(t, gate, "2", "👍")
	waitTelegramQueueAck(t, conn, 1, "accepted")
}

func TestUserTelegramQueuedAcknowledgementSkipsRetryAfterTurnStarts(t *testing.T) {
	t.Parallel()
	// Given a queued input whose acknowledgement hits a rate limit.
	data := t.TempDir()
	gate := &reactionGate{calls: make(chan reactionCall, 20), retryAfter: 4}
	gate.limitNextQueued.Store(true)
	f := newFakeTelegram(t)
	f.reactGate = gate
	d := startTelegram(t, f, data, 42)
	d.setModel(t, "bash")
	d.send(t, d.mainSession(t), taskGate(t, data, "earlier"))
	waitTaskFile(t, filepath.Join(data, "earlier.pid"))
	started := time.Now()
	f.message(1, 42, 42, taskGate(t, data, "incoming"))
	gate.expect(t, "1", "👀")
	conn := controlWriteDB(t, data)
	waitTelegramQueueAck(t, conn, 1, "failed")

	// When its turn starts during the wait, processing replaces the queued state.
	releaseResultGate(t, data, "earlier")
	waitTaskFile(t, filepath.Join(data, "incoming.pid"))
	gate.expect(t, "1", "🔥")
	// Then no queued retry can replace processing, even after the retry is due.
	select {
	case got := <-gate.calls:
		t.Fatalf("reaction while processing: %+v", got)
	case <-time.After(time.Until(started.Add(5 * time.Second))):
	}
	releaseResultGate(t, data, "incoming")
	_ = f.next(t)
	gate.expect(t, "1", "👍")
	waitTelegramQueueAck(t, conn, 1, "failed")
}

func TestUserTelegramQueuedAcknowledgementDoesNotReplayUnknownOutcome(t *testing.T) {
	t.Parallel()
	// Given Telegram can receive the request without returning a definite outcome.
	data := t.TempDir()
	gate := &reactionGate{calls: make(chan reactionCall, 20), unknownOutcome: true}
	gate.limitNextQueued.Store(true)
	f := newFakeTelegram(t)
	f.reactGate = gate
	d := startTelegram(t, f, data, 42)

	// When the first acknowledgement has an unknown outcome, replies still arrive.
	f.message(1, 42, 42, "unknown acknowledgement")
	gate.expect(t, "1", "👀")
	conn := controlWriteDB(t, data)
	waitTelegramQueueAck(t, conn, 1, "unknown")
	_ = f.next(t)
	eventually(t, "reply delivery confirmed before restart", func() (bool, any) {
		return count(t, conn, "SELECT count(*) FROM outbox_messages o JOIN submissions s ON s.id = o.source_submission_id WHERE s.source_key='telegram:123:1' AND o.kind = 'reply' AND o.status = 'sent'") == 1, nil
	})
	d.stop()

	// Then a restart and repeated update do not resend the request or erase uncertainty.
	startDex(t, data, f.srv.URL)
	f.message(1, 42, 42, "unknown acknowledgement")
	f.message(2, 42, 42, "after restart")
	if got := f.next(t).Text; got != "echo: after restart" {
		t.Fatalf("reply after restart = %q", got)
	}
	waitTelegramReaction(t, gate, "2", "👍")
	waitTelegramQueueAck(t, conn, 1, "unknown")
}

func telegramQueueAck(t *testing.T, conn *sql.DB, update int64) (string, error) {
	t.Helper()
	var outcome string
	err := conn.QueryRowContext(t.Context(), `SELECT coalesce(a.outcome, 'not_attempted') FROM submissions s
		LEFT JOIN telegram_queue_acknowledgements a ON a.submission_id = s.id
		WHERE s.source_key = 'telegram:123:' || ?`, update).Scan(&outcome)
	return outcome, err
}

func waitTelegramQueueAck(t *testing.T, conn *sql.DB, update int64, want string) {
	t.Helper()
	eventually(t, "queued acknowledgement "+want, func() (bool, any) {
		got, err := telegramQueueAck(t, conn, update)
		return err == nil && got == want, []any{got, err}
	})
}

func waitTelegramReaction(t *testing.T, gate *reactionGate, message, status string) {
	t.Helper()
	deadline := time.After(12 * time.Second)
	for {
		select {
		case got := <-gate.calls:
			if got.message != message {
				t.Fatalf("unexpected reaction: %+v", got)
			}
			if got.status == status {
				return
			}
			if got.status != "🔥" && (message != "2" || got.status != "👀") {
				t.Fatalf("unexpected reaction: %+v", got)
			}
		case <-deadline:
			t.Fatalf("no reaction for %s / %s", message, status)
		}
	}
}
