package app_test

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/recally-io/dex/internal/sysops"
)

func TestUserFollowUpRecoversNativeAdmissionAndCanonicalWake(t *testing.T) {
	t.Parallel()
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprintf("restart_at_missing_receipt=%t", restart), func(t *testing.T) {
			dir := t.TempDir()
			f := newFakeTelegram(t)
			d := startTelegram(t, f, dir, 42)
			d.setModel(t, "spawn-notify")
			conn := sessionDB(t, d)
			main := d.mainChat(t)
			// Given a lost control receipt after the native admission of the child. Neither the
			// control submission ID nor PiG's generation task ID is a native admission receipt.
			execSQL(t, conn, `CREATE TRIGGER block_follow_up_admit BEFORE UPDATE OF admitted_at ON submissions
WHEN OLD.parent_request_id IS NOT NULL BEGIN SELECT RAISE(FAIL, 'control admission receipt unavailable'); END`)
			const title = "Report despite receipt gap"
			f.message(1, 42, 42, childJSON(t, map[string]any{
				"title": title, "message": `session_report {"content":"saved report across receipt gap"}`,
				"provider": "faux", "modelId": "tool", "follow_up": true,
			}))
			followUpTelegramSend(t, f, "spawn:")
			child := waitExecution(t, conn, main, title, "running")
			childID, err := strconv.ParseInt(child.ID, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			d.watch(t, childID).until(t, "native report committed and run ended", func(tr *transcript) bool {
				return !tr.running && slices.ContainsFunc(tr.items, func(it item) bool {
					return it.Name == "session_report" && it.Status == "done" && strings.Contains(it.Output, "staged")
				})
			})
			var activation int64
			var admitted sql.NullInt64
			var report string
			if err := conn.QueryRowContext(t.Context(), `SELECT f.native_activation_id, f.staged_report, s.admitted_at FROM channel_follow_ups f JOIN submissions s ON s.id=f.delegation_id WHERE f.delegation_id=?`, child.Turn).Scan(&activation, &report, &admitted); err != nil || activation == 0 || admitted.Valid || report != "saved report across receipt gap" {
				t.Fatalf("missing native/receipt gap: activation=%d report=%q admitted=%v err=%v", activation, report, admitted, err)
			}
			if n := count(t, conn, `SELECT count(*) FROM submissions WHERE id=?`, child.ResultID); n != 0 {
				t.Fatalf("canonical result committed before control could settle the original request: %d", n)
			}

			// When reconciliation retries online or from a cold start, it uses the same request
			// and activation. In the online case also restart with a committed result still
			// waiting for parent admission, behind a gated Main turn.
			if restart {
				d.stop()
				execSQL(t, conn, `DROP TRIGGER block_follow_up_admit`)
				startDex(t, dir, f.srv.URL)
			} else {
				const hold = "hold-canonical-admission"
				d.send(t, main, childJSON(t, map[string]any{"tool": "bash", "command": taskGate(t, dir, hold)}))
				waitTaskFile(t, filepath.Join(dir, hold+".pid"))
				execSQL(t, conn, `DROP TRIGGER block_follow_up_admit`)
				waitResultSubmission(t, conn, child.ResultID, "pending")
				if n := count(t, conn, `SELECT count(*) FROM submissions WHERE id=? AND admitted_at IS NULL`, child.ResultID); n != 1 {
					t.Fatalf("canonical result had already entered the native parent: %d unadmitted rows", n)
				}
				d.stop()
				releaseResultGate(t, dir, hold)
				startDex(t, dir, f.srv.URL)
			}
			waitResultSubmission(t, conn, child.Turn, "done")
			if n := count(t, conn, `SELECT count(*) FROM channel_follow_ups f JOIN submissions s ON s.id=f.delegation_id WHERE f.delegation_id=? AND f.native_activation_id=? AND f.staged_report IS NULL AND s.admitted_at IS NOT NULL`, child.Turn, activation); n != 1 {
				t.Fatalf("the original admission and stage were not reconciled: %d", n)
			}
			if n := count(t, conn, `SELECT count(*) FROM routine_tool_operations WHERE source_request_id=? AND action='stage_session_report'`, child.Turn); n != 1 {
				t.Fatalf("admission replay created %d report operations for the same child request", n)
			}
			if result := waitResultSubmission(t, conn, child.ResultID, "done"); resultBody(t, result.content)["answer"] != "saved report across receipt gap" {
				t.Fatalf("recovered wrong report: %+v", result)
			}
			assertFollowUpTelegramReceipt(t, conn, child.ResultID, followUpTelegramSend(t, f, "Reviewed channel conclusion"))
			if n := count(t, conn, `SELECT count(*) FROM submissions WHERE conversation_id=? AND kind='task_result'`, main); n != 1 {
				t.Fatalf("recovery created %d wakes", n)
			}
		})
	}
}

func TestUserCanonicalMainReviewRecoversItsMissingAdmissionReceipt(t *testing.T) {
	// Keep the native generation trace across both instances of the synthetic app.
	// The logger is process-wide, so this test is not parallel.
	logs := sysops.NewLogBuffer(1000)
	previous := slog.Default()
	slog.SetDefault(slog.New(sysops.TeeHandler(slog.DiscardHandler, logs)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	dir := t.TempDir()
	f := newFakeTelegram(t)
	const conclusion = "Reviewed channel conclusion"
	var sends atomic.Int64
	f.holdRich = func(_ context.Context, text string) {
		if text == conclusion {
			sends.Add(1)
		}
	}
	d := startTelegram(t, f, dir, 42)
	d.setModel(t, "spawn-notify")
	conn := sessionDB(t, d)
	main := d.mainChat(t)

	// Given SQLite refuses the admission receipt for the canonical review only.
	// Native admission, notify_user, and the dispatch receipt must still commit.
	execSQL(t, conn, `CREATE TRIGGER block_main_review_receipt BEFORE UPDATE OF admitted_at ON submissions
WHEN OLD.admitted_at IS NULL AND EXISTS (
    SELECT 1 FROM channel_follow_ups f WHERE f.result_id=OLD.id AND f.conversation_id=OLD.conversation_id
) BEGIN SELECT RAISE(FAIL, 'Main review admission receipt unavailable'); END`)
	const title = "Review without an admission receipt"
	f.message(1, 42, 42, childJSON(t, map[string]any{
		"title": title, "message": `session_report {"content":"canonical findings"}`,
		"provider": "faux", "modelId": "tool", "follow_up": true,
	}))
	followUpTelegramSend(t, f, "spawn:")
	work := waitExecution(t, conn, main, title, "done")
	platform := followUpTelegramSend(t, f, conclusion)
	assertFollowUpTelegramReceipt(t, conn, work.ResultID, platform)
	if n := count(t, conn, `SELECT count(*) FROM submissions WHERE id=? AND status='pending' AND admitted_at IS NULL`, work.ResultID); n != 1 {
		t.Fatalf("native Main sent a conclusion but the control receipt gap was not present: %d rows", n)
	}
	original := resultSubmission(t, conn, work.ResultID)
	if got := resultBody(t, original.content)["answer"]; got != "canonical findings" {
		t.Fatalf("unexpected canonical result: %v", got)
	}

	// Observe the native result entry and committed tool call, not just the Outbox dedup key.
	nativeReview := func(d *dex) [2]int64 {
		t.Helper()
		var entries [2][]int64
		d.watch(t, main).until(t, "native canonical review and completed notify_user", func(tr *transcript) bool {
			entries = [2][]int64{}
			for _, it := range tr.items {
				if it.ID == work.ResultID && it.Kind == "task" && !it.Queued && it.EntryID > 0 {
					entries[0] = append(entries[0], it.EntryID)
				}
				if it.Kind == "tool" && it.Name == "notify_user" && it.Status == "done" && it.EntryID > 0 {
					entries[1] = append(entries[1], it.EntryID)
				}
			}
			return !tr.running && len(entries[0]) > 0 && len(entries[1]) > 0
		})
		if len(entries[0]) != 1 || len(entries[1]) != 1 {
			t.Fatalf("expected one native result entry and one notify_user: %v", entries)
		}
		return [2]int64{entries[0][0], entries[1][0]}
	}
	beforeEntries := nativeReview(d)
	var nativeMain int64
	if err := conn.QueryRowContext(t.Context(), `SELECT execution_id FROM conversations WHERE id=?`, main).Scan(&nativeMain); err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(fmt.Sprintf(`native_conversation_id=%d native_task_id=(\d+) native_submission_ids=\[([^]]+)\]`, nativeMain))
	mainStarts := func() [][2]string {
		t.Helper()
		var starts [][2]string
		for _, entry := range logs.Entries(0) {
			if strings.Contains(entry.Attrs, "phase=generation_started") {
				if match := pattern.FindStringSubmatch(entry.Attrs); match != nil {
					starts = append(starts, [2]string{match[1], match[2]})
				}
			}
		}
		return starts
	}
	beforeStarts := mainStarts()
	if len(beforeStarts) != 2 || len(strings.Fields(beforeStarts[1][1])) != 1 {
		t.Fatalf("expected the original request and one single-input review (native task/submissions): %v", beforeStarts)
	}

	// When the fixture restarts, it reconciles the original request ID and permits ordinary work.
	d.stop()
	execSQL(t, conn, `DROP TRIGGER block_main_review_receipt`)
	d = startDex(t, dir, f.srv.URL)
	if result := waitResultSubmission(t, conn, work.ResultID, "done"); result.content != original.content {
		t.Fatalf("recovery changed the canonical result: %+v", result)
	}
	if n := count(t, conn, `SELECT count(*) FROM submissions WHERE id=? AND admitted_at IS NOT NULL`, work.ResultID); n != 1 {
		t.Fatalf("the original admission receipt was not repaired: %d rows", n)
	}
	if after := nativeReview(d); after != beforeEntries || !slices.Equal(mainStarts(), beforeStarts) {
		t.Fatalf("recovery re-admitted the review: entries %v -> %v; starts %v -> %v", beforeEntries, after, beforeStarts, mainStarts())
	}
	d.setModel(t, "echo")
	f.message(2, 42, 42, "ordinary after Main receipt recovery")
	followUpTelegramSend(t, f, "echo: ordinary after Main receipt recovery")
	if starts := mainStarts(); len(starts) != 3 || !slices.Equal(starts[:2], beforeStarts) || starts[2][0] == starts[1][0] || starts[2][1] == starts[1][1] {
		t.Fatalf("expected only a new ordinary native activation after recovery: %v", starts)
	}

	// Then the original commitment and remote receipt remain intact, without an extra wake or send.
	assertFollowUpTelegramReceipt(t, conn, work.ResultID, platform)
	if n := count(t, conn, `SELECT count(*) FROM channel_follow_ups`); n != 1 {
		t.Fatalf("recovery changed the commitment count: %d", n)
	}
	if n := count(t, conn, `SELECT count(*) FROM submissions WHERE conversation_id=? AND kind='task_result'`, main); n != 1 {
		t.Fatalf("recovery created extra Main wakes: %d", n)
	}
	if got := sends.Load(); got != 1 {
		t.Fatalf("reviewed conclusion reached Telegram %d times, want once", got)
	}
	if n := count(t, conn, `SELECT count(*) FROM outbox_messages WHERE kind='proactive'`); n != 1 {
		t.Fatalf("recovery added a conclusion to the Outbox: %d entries", n)
	}
}

func assertDeliveryDisposition(t *testing.T, conn *sql.DB, sourceID, want string) {
	t.Helper()
	eventually(t, "delivery diagnostic: "+want, func() (bool, any) {
		var disposition string
		err := conn.QueryRowContext(t.Context(), `SELECT disposition FROM outbox_delivery_diagnostics WHERE source_submission_id=? AND id IN (SELECT id FROM outbox_messages WHERE kind='proactive')`, sourceID).Scan(&disposition)
		return err == nil && disposition == want, []any{disposition, err}
	})
}

func TestUserPendingFollowUpRecoversOrIsRevokedBeforeTelegram(t *testing.T) {
	t.Parallel()
	for _, cancel := range []string{"recover", "stop", "clear", "route"} {
		t.Run(cancel, func(t *testing.T) {
			dir := t.TempDir()
			f := newFakeTelegram(t)
			var entries atomic.Int64
			f.holdRich = func(_ context.Context, text string) {
				if text == "Reviewed channel conclusion" {
					entries.Add(1)
				}
			}
			d := startTelegram(t, f, dir, 42)
			d.setModel(t, "spawn-notify")
			main := d.mainChat(t)
			conn := sessionDB(t, d)
			// Given a final Outbox write committed behind a durable dispatch gate. No
			// request has reached the platform; this is not an uncertain send.
			execSQL(t, conn, `CREATE TRIGGER hold_follow_up_dispatch AFTER INSERT ON outbox_messages WHEN NEW.event_id LIKE 'follow-up-final:%'
BEGIN UPDATE outbox_messages SET retry_at=9000000000000 WHERE id=NEW.id; END`)
			const title = "Held conclusion"
			f.message(1, 42, 42, childJSON(t, map[string]any{"title": title, "message": "private result", "provider": "faux", "modelId": "echo", "follow_up": true}))
			work := waitExecution(t, conn, main, title, "done")
			waitResultSubmission(t, conn, work.ResultID, "done")
			assertDeliveryDisposition(t, conn, work.ResultID, "unconfirmed")
			if n := entries.Load(); n != 0 {
				t.Fatalf("dispatch gate allowed %d platform entries", n)
			}

			// When cancellation wins (or no one cancels), restart and remove the gate.
			switch cancel {
			case "stop", "clear":
				f.message(2, 42, 42, "/"+cancel)
				assertDeliveryDisposition(t, conn, work.ResultID, "revoked_or_skipped_unconfirmed")
			case "route":
				for _, id := range []int64{99, 42} {
					d.must(t, http.MethodPut, "/api/agents/default/channels/telegram", map[string]any{"allowedUsers": []int64{id}}, nil)
				}
				assertDeliveryDisposition(t, conn, work.ResultID, "revoked_or_skipped_unconfirmed")
			}
			d.stop()
			execSQL(t, conn, `DROP TRIGGER hold_follow_up_dispatch; UPDATE outbox_messages SET retry_at=0 WHERE event_id LIKE 'follow-up-final:%'`)
			d = startDex(t, dir, f.srv.URL)
			if cancel == "recover" {
				assertFollowUpTelegramReceipt(t, conn, work.ResultID, followUpTelegramSend(t, f, "Reviewed channel conclusion"))
				assertDeliveryDisposition(t, conn, work.ResultID, "platform_confirmed")
			}
			// A new real dispatch follows the replay, proving the same peer is not simply offline.
			d.setModel(t, "echo")
			f.message(3, 42, 42, "dispatch barrier")
			followUpTelegramSend(t, f, "echo: dispatch barrier")
			want := int64(0)
			if cancel == "recover" {
				want = 1
			}
			if got := entries.Load(); got != want {
				t.Fatalf("follow-up platform entries after replay: got %d want %d", got, want)
			}
			if n := count(t, conn, `SELECT count(*) FROM outbox_messages WHERE source_submission_id=? AND event_id LIKE 'follow-up-final:%'`, work.ResultID); n != 1 {
				t.Fatalf("follow-up Outbox entries after replay: %d", n)
			}
		})
	}
}

func TestUserWebOnlyNoticeDoesNotEnterTheOutbox(t *testing.T) {
	t.Parallel()
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "tool")
	main := d.mainChat(t)
	conn := sessionDB(t, d)
	id := d.send(t, main, `notify_user {"content":"In-app only"}`)
	waitResultSubmission(t, conn, id, "done")
	if n := count(t, conn, `SELECT count(*) FROM outbox_messages WHERE source_submission_id = ?`, id); n != 0 {
		t.Fatalf("Web-only notice has an Outbox delivery: %d", n)
	}
	if n := count(t, conn, `SELECT count(*) FROM web_notifications WHERE source_submission_id = ?`, id); n != 1 {
		t.Fatalf("Web-only notice is missing: %d", n)
	}
}
