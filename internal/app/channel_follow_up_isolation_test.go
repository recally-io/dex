package app_test

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"uuid"

	"github.com/recally-io/dex/internal/sysops"
)

func TestUserOptedInDelegationsOwnSeparateChildActivationsAndPeers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, dir, 42, 43)
	d.setModel(t, "spawn-notify")
	main := d.mainChat(t)
	conn := sessionDB(t, d)
	var update int64
	send := func(peer int64, args map[string]any, reply string) {
		t.Helper()
		update++
		f.message(update, peer, peer, childJSON(t, args))
		if m := followUpTelegramSend(t, f, reply); m.ChatID != peer {
			t.Fatalf("reply reached %d, want original peer %d", m.ChatID, peer)
		}
	}
	bash := func(command string) string { return "bash " + childJSON(t, map[string]string{"command": command}) }
	const title = "Reused child"
	// Given an ordinary turn in a child, then requests from two distinct source peers.
	send(42, map[string]any{"title": title, "message": bash("printf ordinary-before"), "provider": "faux", "modelId": "tool"}, "spawn:")
	initial := waitExecution(t, conn, main, title, "done")
	waitResultSubmission(t, conn, initial.ResultID, "done")
	child, err := strconv.ParseInt(initial.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	rejectSteer := func(peer int64, wantTurns, wantGrants int) {
		t.Helper()
		send(peer, map[string]any{"tool": "session_send", "id": initial.ID, "message": "must not be stored", "mode": "steer", "follow_up": true}, "mode: queue")
		if n := count(t, conn, `SELECT count(*) FROM submissions WHERE conversation_id=?`, child); n != wantTurns {
			t.Fatalf("rejected steer wrote a child input: %d (want %d)", n, wantTurns)
		}
		if n := count(t, conn, `SELECT count(*) FROM channel_follow_ups WHERE child_id=?`, child); n != wantGrants {
			t.Fatalf("rejected steer wrote a commitment: %d (want %d)", n, wantGrants)
		}
	}
	rejectSteer(42, 1, 0)
	const firstGate = "first-protected-child"
	send(42, map[string]any{"tool": "session_send", "id": initial.ID, "message": bash(taskGate(t, dir, firstGate)), "mode": "queue", "follow_up": true}, "spawn:")
	waitTaskFile(t, filepath.Join(dir, firstGate+".pid"))
	first := waitExecution(t, conn, main, title, "running")
	rejectSteer(43, 2, 1)

	// When ordinary tasks try to steer the protected child, they retain ordinary source and
	// reporting semantics. The web steer becomes a native follow-up, never a steer attachment.
	web := uuid.NewV7().String()
	sendMode(t, d, child, web, `session_report {"content":"web-only milestone","final":false}`, "steer", http.StatusAccepted)
	d.watch(t, child).until(t, "Web steer converted into ordinary queued input", func(tr *transcript) bool {
		return slices.ContainsFunc(tr.items, func(it item) bool { return it.ID == web && it.Queued && !it.Steer })
	})
	const ordinary = `session_report {"content":"ordinary parent report"}`
	send(42, map[string]any{"tool": "session_send", "id": initial.ID, "message": ordinary, "mode": "steer"}, "spawn:")
	const secondReport = `session_report {"content":"second protected findings"}`
	send(43, map[string]any{"tool": "session_send", "id": initial.ID, "message": secondReport, "mode": "queue", "follow_up": true}, "spawn:")
	second := waitExecution(t, conn, main, title, "running")
	if second.Turn == first.Turn {
		t.Fatal("the second peer did not get its own delegation")
	}

	// A different child can finish first without taking either commitment of the reused child.
	const parallelTitle = "Parallel protected child"
	send(42, map[string]any{"title": parallelTitle, "message": `session_report {"content":"parallel protected findings"}`, "provider": "faux", "modelId": "tool", "follow_up": true}, "spawn:")
	parallel := waitExecution(t, conn, main, parallelTitle, "done")
	assertFollowUpTelegramReceipt(t, conn, parallel.ResultID, followUpTelegramSend(t, f, "Reviewed channel conclusion"))
	if got := resultSubmission(t, conn, first.Turn); got.status != "pending" {
		t.Fatalf("parallel result changed first delegation: %+v", got)
	}
	releaseResultGate(t, dir, firstGate)
	for _, work := range []execution{first, second} {
		waitResultSubmission(t, conn, work.ResultID, "done")
	}
	waitResultSubmission(t, conn, web, "done")
	for _, work := range []execution{first, second} {
		var peer string
		var activation int64
		if err := conn.QueryRowContext(t.Context(), `SELECT p.origin, f.native_activation_id FROM channel_follow_ups f JOIN submissions p ON p.id=f.parent_request_id WHERE f.delegation_id=?`, work.Turn).Scan(&peer, &activation); err != nil || activation == 0 {
			t.Fatalf("no saved activation for %s: %d, %v", work.Turn, activation, err)
		}
		wantPeer, wantAnswer := "telegram:42", "tool: gate released"
		if work.Turn == second.Turn {
			wantPeer, wantAnswer = "telegram:43", "second protected findings"
		}
		if body := resultBody(t, resultSubmission(t, conn, work.ResultID).content); peer != wantPeer || body["answer"] != wantAnswer {
			t.Fatalf("crossed delegation: peer=%s want=%s result=%v", peer, wantPeer, body)
		}
	}
	if n := count(t, conn, `SELECT count(DISTINCT native_activation_id) FROM channel_follow_ups WHERE child_id=?`, child); n != 2 {
		t.Fatalf("two opted-in requests shared a native activation: %d", n)
	}
	seen := map[int64]bool{}
	results := map[int64]string{42: first.ResultID, 43: second.ResultID}
	for range results {
		m := followUpTelegramSend(t, f, "Reviewed channel conclusion")
		id := results[m.ChatID]
		if id == "" || seen[m.ChatID] {
			t.Fatalf("destination reused or expanded: %+v", m)
		}
		seen[m.ChatID] = true
		assertFollowUpTelegramReceipt(t, conn, id, m)
	}
	// And only ordinary progress and final reports produce standalone wakes; no authority leaked.
	if n := count(t, conn, `SELECT count(*) FROM submissions WHERE conversation_id=? AND id LIKE 'report:%' AND (instr(content,'web-only milestone')>0 OR instr(content,'ordinary parent report')>0)`, main); n != 2 {
		t.Fatalf("ordinary milestones/reports changed: %d", n)
	}
	if n := count(t, conn, `SELECT count(*) FROM submissions WHERE conversation_id=? AND id LIKE 'report:%'`, main); n != 2 {
		t.Fatalf("standalone protected final wakes: got %d reports, expected only the two ordinary reports", n)
	}
	if n := count(t, conn, `SELECT count(*) FROM outbox_messages WHERE dedup_key LIKE 'follow-up-final:%'`); n != 3 {
		t.Fatalf("unexpected number of conclusions for the three grants: %d", n)
	}
}

func TestUserSteersMainWhileAChannelReviewWaits(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, dir, 42)
	d.setModel(t, "spawn-notify")
	main, conn := d.mainChat(t), sessionDB(t, d)
	const child = "child-before-steer"
	f.message(1, 42, 42, childJSON(t, map[string]any{
		"title": child, "message": taskGate(t, dir, child), "provider": "faux", "modelId": "bash", "follow_up": true,
	}))
	followUpTelegramSend(t, f, "spawn:")
	waitTaskFile(t, filepath.Join(dir, child+".pid"))
	bash := func(command string) string {
		return childJSON(t, map[string]any{"tool": "bash", "command": command})
	}
	const hold = "main-before-steer"
	first := d.send(t, main, bash(taskGate(t, dir, hold)))
	waitTaskFile(t, filepath.Join(dir, hold+".pid"))
	releaseResultGate(t, dir, child)
	result := waitExecution(t, conn, main, child, "done").ResultID
	waitResultSubmission(t, conn, result, "pending")

	// The older review holds queued work, but a steer still joins the running turn.
	queued, steer := uuid.NewV7().String(), uuid.NewV7().String()
	sendMode(t, d, main, queued, bash("printf queued-after-review"), "followUp", http.StatusAccepted)
	sendMode(t, d, main, steer, bash("printf steer-before-review"), "steer", http.StatusAccepted)
	w := d.watch(t, main)
	w.until(t, "steer in the native Inbox", queuedSteer(steer))
	if n := count(t, conn, `SELECT count(*) FROM submissions WHERE id IN (?,?) AND admitted_at IS NULL`, result, queued); n != 2 {
		t.Fatalf("review and queued input must both wait outside the Inbox; got %d pending", n)
	}
	releaseResultGate(t, dir, hold)
	for _, id := range []string{first, steer} {
		if got := waitResultSubmission(t, conn, id, "done").answer; got != "spawn: steer-before-review" {
			t.Fatalf("input %s did not share the steered answer: %q", id, got)
		}
	}
	waitResultSubmission(t, conn, result, "done")
	assertFollowUpTelegramReceipt(t, conn, result, followUpTelegramSend(t, f, "Reviewed channel conclusion"))
	if got := waitResultSubmission(t, conn, queued, "done").answer; got != "spawn: queued-after-review" {
		t.Fatalf("queued input did not have its own answer after the review: %q", got)
	}
}

func TestUserSamePeerCompletionsGetIndependentMainReviews(t *testing.T) {
	// This test reads the native generation diagnostic, so it does not run in parallel.
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprintf("precise_stop=%t", stop), func(t *testing.T) {
			logs := sysops.NewLogBuffer(1000)
			previous := slog.Default()
			slog.SetDefault(slog.New(sysops.TeeHandler(slog.DiscardHandler, logs)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			dir := t.TempDir()
			f := newFakeTelegram(t)
			d := startTelegram(t, f, dir, 42)
			d.setModel(t, "spawn-notify")
			conn := sessionDB(t, d)
			main := d.mainChat(t)
			// Given two opted-in requests from the same Telegram peer and an unrelated busy Main.
			for i, title := range []string{"first-child", "second-child"} {
				f.message(int64(i+1), 42, 42, childJSON(t, map[string]any{
					"title": title, "message": taskGate(t, dir, title), "provider": "faux", "modelId": "bash", "follow_up": true,
				}))
				followUpTelegramSend(t, f, "spawn:")
				waitTaskFile(t, filepath.Join(dir, title+".pid"))
			}
			const hold = "hold-main-reviews"
			foreground := d.send(t, main, childJSON(t, map[string]any{"tool": "bash", "command": taskGate(t, dir, hold)}))
			waitTaskFile(t, filepath.Join(dir, hold+".pid"))
			target := d.watch(t, main).activeRunTarget(t)
			var nativeMain int64
			if err := conn.QueryRowContext(t.Context(), "SELECT execution_id FROM conversations WHERE id=?", main).Scan(&nativeMain); err != nil {
				t.Fatal(err)
			}
			var results []string
			for _, title := range []string{"second-child", "first-child"} {
				releaseResultGate(t, dir, title)
				child := waitExecution(t, conn, main, title, "done")
				waitResultSubmission(t, conn, child.ResultID, "pending")
				results = append(results, child.ResultID)
			}
			const ordinary = "ordinary-same-peer"
			f.message(3, 42, 42, childJSON(t, map[string]any{"tool": "bash", "command": "printf " + ordinary}))
			var ordinaryID string
			eventually(t, "same-origin ordinary input is durably pending", func() (bool, any) {
				err := conn.QueryRowContext(t.Context(), `SELECT id FROM submissions WHERE origin='telegram:42' AND kind='message' AND status='pending' AND instr(content, ?) > 0`, ordinary).Scan(&ordinaryID)
				return err == nil, err
			})
			if n := count(t, conn, `SELECT count(*) FROM submissions WHERE id IN (?, ?) AND admitted_at IS NULL AND status='pending'`, results[0], results[1]); n != 2 {
				t.Fatalf("only %d reviews were held outside the native shared Inbox", n)
			}
			before := logs.Entries(1)[0].Seq

			// When Main becomes free, with or without a precise Web Stop of the unrelated turn.
			if stop {
				d.must(t, http.MethodPost, chatPath(main, "abort?runTarget="+url.QueryEscape(target)), nil, nil)
				waitResultSubmission(t, conn, foreground, "unanswered")
			} else {
				releaseResultGate(t, dir, hold)
				waitResultSubmission(t, conn, foreground, "done")
			}

			// Then the two separate native review activations each send their own conclusion.
			// The third activation is the ordinary input of exactly the same origin and peer.
			var lastRemote int64
			for _, id := range results {
				waitResultSubmission(t, conn, id, "done")
				sent := followUpTelegramSend(t, f, "Reviewed channel conclusion")
				if sent.ChatID != 42 || sent.ID == lastRemote {
					t.Fatalf("not a new receipt from the original peer: %+v", sent)
				}
				assertFollowUpTelegramReceipt(t, conn, id, sent)
				lastRemote = sent.ID
			}
			if reply := waitResultSubmission(t, conn, ordinaryID, "done"); !strings.Contains(reply.answer, ordinary) {
				t.Fatalf("ordinary reply was absorbed by a review: %+v", reply)
			}
			followUpTelegramSend(t, f, ordinary)
			pattern := regexp.MustCompile(fmt.Sprintf(`native_conversation_id=%d native_task_id=(\d+) native_submission_ids=\[([^]]*)\]`, nativeMain))
			tasks, inputs := map[string]bool{}, map[string]bool{}
			for _, entry := range logs.Entries(0) {
				if entry.Seq <= before || !strings.Contains(entry.Attrs, "phase=generation_started") {
					continue
				}
				if match := pattern.FindStringSubmatch(entry.Attrs); match != nil {
					if fields := strings.Fields(match[2]); len(fields) != 1 || inputs[match[2]] || tasks[match[1]] {
						t.Fatalf("not a fresh single-input Main activation: %s", entry.Attrs)
					}
					inputs[match[2]], tasks[match[1]] = true, true
				}
			}
			if len(tasks) != 3 {
				t.Fatalf("expected two reviews and one ordinary activation: tasks=%v inputs=%v", tasks, inputs)
			}
			if n := count(t, conn, `SELECT count(*) FROM submissions WHERE conversation_id=? AND kind='task_result'`, main); n != 2 {
				t.Fatalf("unexpected Main wake count: %d", n)
			}
			if n := count(t, conn, `SELECT count(*) FROM outbox_messages WHERE dedup_key LIKE 'follow-up-final:%'`); n != 2 {
				t.Fatalf("unexpected conclusion count: %d", n)
			}
		})
	}
}
