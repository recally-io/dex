package app_test

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func execSQL(t *testing.T, conn *sql.DB, statement string) {
	t.Helper()
	if _, err := conn.ExecContext(t.Context(), statement); err != nil {
		t.Fatal(err)
	}
}

// waitChildTurnsEnded waits until the child has n turns and none is pending.
func waitChildTurnsEnded(t *testing.T, conn *sql.DB, child int64, n int) []childTurnRow {
	t.Helper()
	var turns []childTurnRow
	eventually(t, "child turns ended", func() (bool, any) {
		turns = childTurns(t, conn, child)
		ended := len(turns) == n
		for _, turn := range turns {
			ended = ended && turn.status != "pending"
		}
		return ended, turns
	})
	return turns
}

func TestUserRestartInterruptsARunWhoseAdmissionReceiptWasLost(t *testing.T) {
	t.Parallel()
	// Given a child run that crossed the native admission boundary, while the control database
	// refused the admission receipt.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	write := controlWriteDB(t, dir)
	parent := newParent(t, d)
	execSQL(t, write, `CREATE TRIGGER block_admit BEFORE UPDATE OF admitted_at ON submissions WHEN OLD.parent_request_id IS NOT NULL BEGIN SELECT RAISE(FAIL, 'admission receipt unavailable'); END`)
	child := spawnChild(t, d, parent, "Admission gap", taskGate(t, dir, "admission"), "bash")
	waitTaskFile(t, filepath.Join(dir, "admission.pid"))
	var admitted sql.NullInt64
	if err := write.QueryRowContext(t.Context(), `SELECT admitted_at FROM submissions WHERE conversation_id = ?`, child.conversation(t)).Scan(&admitted); err != nil || admitted.Valid {
		t.Fatalf("the receipt must be missing: %v %v", admitted, err)
	}
	// When dex restarts with the database working again.
	d.stop()
	execSQL(t, write, `DROP TRIGGER block_admit`)
	startDex(t, dir, "")
	releaseResultGate(t, dir, "admission")
	// Then the parent gets one interrupted result, not a done one.
	rows := waitResults(t, write, parent, 1)
	if body := resultBody(t, rows[0].content); body["outcome"] != "interrupted" {
		t.Fatalf("the run that the restart cut off was not interrupted: %v", body)
	}
}

func TestUserRestartWithdrawsQueuedInputsWhoseAdmissionReceiptsWereLost(t *testing.T) {
	t.Parallel()
	// Given a running child turn and two queued turns that reached the native queue, while the
	// control database refused their admission receipts.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	write := controlWriteDB(t, dir)
	parent := newParent(t, d)
	marker := filepath.Join(dir, "queued-two-ran")
	child := spawnChild(t, d, parent, "Queued gaps", taskGate(t, dir, "queued-gap"), "bash")
	waitTaskFile(t, filepath.Join(dir, "queued-gap.pid"))
	execSQL(t, write, `CREATE TRIGGER block_admit BEFORE UPDATE OF admitted_at ON submissions WHEN OLD.parent_request_id IS NOT NULL AND OLD.admitted_at IS NULL BEGIN SELECT RAISE(FAIL, 'queued admission receipt unavailable'); END`)
	var ref childRef
	for _, message := range []string{"echo queued-one", "echo queued-two > '" + marker + "'"} {
		toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": message}), &ref)
	}
	// When dex restarts with the database working again.
	d.stop()
	execSQL(t, write, `DROP TRIGGER block_admit`)
	d = startDex(t, dir, "")
	d.mainChat(t)
	// Then every queued input is withdrawn and counted, and none of them runs.
	turns := waitChildTurnsEnded(t, write, child.conversation(t), 3)
	rows := resultRows(t, write, parent)
	if len(rows) != 1 {
		t.Fatalf("results: %+v turns: %+v", rows, turns)
	}
	if body := resultBody(t, rows[0].content); body["outcome"] != "interrupted" || body["withdrawn"] != float64(2) {
		t.Fatalf("result after restart: %v", body)
	}
	for _, turn := range turns {
		if turn.status != "unanswered" {
			t.Fatalf("turn ran: %+v", turns)
		}
	}
	// And a new message still runs in the same child.
	toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": "echo back"}), &ref)
	if rows := waitResults(t, write, parent, 2); resultBody(t, rows[1].content)["answer"] != "bash: back" {
		t.Fatalf("answer after restart: %+v", rows[1])
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a withdrawn input ran: %v", err)
	}
}

func TestUserRestartSendsInterruptedResultForEachPlacedParentInput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := controlWriteDB(t, dir)
	parent := newParent(t, d)
	child := spawnChild(t, d, parent, "Interrupted batch", taskGate(t, dir, "first-interrupted"), "bash")
	waitTaskFile(t, filepath.Join(dir, "first-interrupted.pid"))
	var ref childRef
	toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": taskGate(t, dir, "steer-interrupted"), "mode": "steer"}), &ref)
	releaseResultGate(t, dir, "first-interrupted")
	waitTaskFile(t, filepath.Join(dir, "steer-interrupted.pid"))
	for _, message := range []string{"echo queued-one", "echo queued-two"} {
		toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": message}), &ref)
	}
	d.stop()
	d = startDex(t, dir, "")
	turns := waitChildTurnsEnded(t, conn, child.conversation(t), 4)
	rows := resultRows(t, conn, parent)
	if len(rows) != 2 {
		t.Fatalf("placed parent inputs need two results: turns=%+v results=%+v", turns, rows)
	}
	for _, row := range rows {
		body := resultBody(t, row.content)
		if body["outcome"] != "interrupted" || body["withdrawn"] != float64(2) {
			t.Fatalf("wrong interrupted result: %v", body)
		}
	}
	d.stop()
	startDex(t, dir, "")
	if rows := resultRows(t, conn, parent); len(rows) != 2 {
		t.Fatalf("second restart added results: %+v", rows)
	}
}

func TestUserRestartKeepsDoneAnswerSeparateFromInterruptedRun(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := controlWriteDB(t, dir)
	parent := newParent(t, d)
	execSQL(t, conn, `CREATE TRIGGER hold_child_receipts BEFORE UPDATE OF status ON submissions WHEN OLD.parent_request_id IS NOT NULL BEGIN SELECT RAISE(FAIL, 'hold receipts'); END`)
	child := spawnChild(t, d, parent, "Separate outcomes", "echo completed", "bash")
	d.watch(t, child.conversation(t)).until(t, "native first answer", answeredWith("bash: completed"))
	var ref childRef
	toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": taskGate(t, dir, "separate-first")}), &ref)
	waitTaskFile(t, filepath.Join(dir, "separate-first.pid"))
	toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": taskGate(t, dir, "separate-steer"), "mode": "steer"}), &ref)
	releaseResultGate(t, dir, "separate-first")
	waitTaskFile(t, filepath.Join(dir, "separate-steer.pid"))
	for _, message := range []string{"echo queued-one", "echo queued-two"} {
		toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": message}), &ref)
	}
	d.stop()
	execSQL(t, conn, `DROP TRIGGER hold_child_receipts`)
	d = startDex(t, dir, "")
	turns := waitChildTurnsEnded(t, conn, child.conversation(t), 5)
	rows := resultRows(t, conn, parent)
	counts := map[string]int{}
	for _, row := range rows {
		body := resultBody(t, row.content)
		outcome, _ := body["outcome"].(string)
		counts[outcome]++
		if outcome == "interrupted" && body["withdrawn"] != float64(2) {
			t.Fatalf("wrong withdrawal count: %v", body)
		}
	}
	if len(rows) != 3 || counts["done"] != 1 || counts["interrupted"] != 2 {
		t.Fatalf("outcomes merged: turns=%+v results=%+v", turns, rows)
	}
	d.stop()
	startDex(t, dir, "")
	if rows := resultRows(t, conn, parent); len(rows) != 3 {
		t.Fatalf("second restart added results: %+v", rows)
	}
}

func TestUserRestartAfterASharedAnswerSendsOneResult(t *testing.T) {
	t.Parallel()
	// Given a run that a parent steer joined and that answered natively, while the control database
	// refused to settle both receipts.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	write := controlWriteDB(t, dir)
	parent := newParent(t, d)
	child := spawnChild(t, d, parent, "Receipt gap", taskGate(t, dir, "receipt"), "bash")
	waitTaskFile(t, filepath.Join(dir, "receipt.pid"))
	execSQL(t, write, `CREATE TRIGGER block_settle BEFORE UPDATE OF status ON submissions WHEN OLD.parent_request_id IS NOT NULL BEGIN SELECT RAISE(FAIL, 'receipt unavailable'); END`)
	var ref childRef
	toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": "echo steer", "mode": "steer"}), &ref)
	releaseResultGate(t, dir, "receipt")
	d.watch(t, child.conversation(t)).until(t, "native shared answer", answeredWith("bash:"))
	if turns := childTurns(t, write, child.conversation(t)); len(turns) != 2 || turns[0].status != "pending" || turns[1].status != "pending" {
		t.Fatalf("both receipts must still be pending: %+v", turns)
	}
	// When dex restarts with the database working again.
	d.stop()
	execSQL(t, write, `DROP TRIGGER block_settle`)
	d = startDex(t, dir, "")
	d.mainChat(t)
	// Then the receipts settle and the parent gets the result of the starter only.
	turns := waitChildTurnsEnded(t, write, child.conversation(t), 2)
	rows := resultRows(t, write, parent)
	if len(rows) != 1 || rows[0].id != "result:"+turns[0].id {
		t.Fatalf("results %+v for turns %+v", rows, turns)
	}
}

func TestUserFailedSharedRunSendsOneResultPerParentInput(t *testing.T) {
	t.Parallel()
	// Given a child run that a parent steer joined and that the model fails.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := newParent(t, d)
	child := spawnChild(t, d, parent, "Failure", childJSON(t, map[string]any{"tool": "bash", "command": taskGate(t, dir, "failure")}), "spawn")
	waitTaskFile(t, filepath.Join(dir, "failure.pid"))
	var ref childRef
	toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": "invalid JSON makes the model fail", "mode": "steer"}), &ref)
	// When the run ends.
	releaseResultGate(t, dir, "failure")
	// Then each parent input gets its own failed result.
	turns := waitChildTurnsEnded(t, conn, child.conversation(t), 2)
	rows := resultRows(t, conn, parent)
	if len(rows) != 2 {
		t.Fatalf("results %+v for turns %+v", rows, turns)
	}
	for _, row := range rows {
		if body := resultBody(t, row.content); body["outcome"] != "failed" {
			t.Fatalf("result: %v", body)
		}
	}
}

func TestUserParentSteerOfUserRunSurvivesReceiptRetryAndRestart(t *testing.T) {
	t.Parallel()
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprintf("restart=%t", restart), func(t *testing.T) {
			dir := t.TempDir()
			d := startDex(t, dir, "")
			conn := controlWriteDB(t, dir)
			parent := newParent(t, d)
			child := spawnChild(t, d, parent, "Mixed receipt", "echo initial", "bash")
			waitResults(t, conn, parent, 1)
			d.send(t, child.conversation(t), taskGate(t, dir, "mixed-receipt"))
			waitTaskFile(t, filepath.Join(dir, "mixed-receipt.pid"))
			execSQL(t, conn, `CREATE TRIGGER hold_parent_steer BEFORE UPDATE OF status ON submissions WHEN OLD.parent_request_id IS NOT NULL AND OLD.when_busy = 'steer' BEGIN SELECT RAISE(FAIL, 'hold parent receipt'); END`)
			var ref childRef
			toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": "echo parent", "mode": "steer"}), &ref)
			releaseResultGate(t, dir, "mixed-receipt")
			d.watch(t, child.conversation(t)).until(t, "native answer", answeredWith("bash: parent"))
			eventually(t, "user receipt settled", func() (bool, any) {
				turns := childTurns(t, conn, child.conversation(t))
				return len(turns) == 3 && turns[1].status == "done" && turns[2].status == "pending", turns
			})
			if restart {
				d.stop()
			}
			execSQL(t, conn, `DROP TRIGGER hold_parent_steer`)
			if restart {
				startDex(t, dir, "")
			}
			turns := waitChildTurnsEnded(t, conn, child.conversation(t), 3)
			rows := waitResults(t, conn, parent, 2)
			if rows[1].id != "result:"+turns[2].id {
				t.Fatalf("parent steer did not own result: turns=%+v results=%+v", turns, rows)
			}
		})
	}
}

func TestUserParentSteerOfUserRunReportsFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := controlWriteDB(t, dir)
	parent := newParent(t, d)
	child := spawnChild(t, d, parent, "User failure", childJSON(t, map[string]any{"tool": "bash", "command": "echo initial"}), "spawn")
	waitResults(t, conn, parent, 1)
	d.send(t, child.conversation(t), childJSON(t, map[string]any{"tool": "bash", "command": taskGate(t, dir, "user-failure")}))
	waitTaskFile(t, filepath.Join(dir, "user-failure.pid"))
	var ref childRef
	toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": "invalid JSON fails the model", "mode": "steer"}), &ref)
	releaseResultGate(t, dir, "user-failure")
	turns := waitChildTurnsEnded(t, conn, child.conversation(t), 3)
	rows := resultRows(t, conn, parent)
	if len(rows) != 2 || rows[1].id != "result:"+turns[2].id || resultBody(t, rows[1].content)["outcome"] != "failed" {
		t.Fatalf("parent steer must report failure, user input must not: turns=%+v results=%+v", turns, rows)
	}
}

func TestUserParentSteerOfUserRunReportsInterruption(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := controlWriteDB(t, dir)
	parent := newParent(t, d)
	child := spawnChild(t, d, parent, "User interruption", "echo initial", "bash")
	waitResults(t, conn, parent, 1)
	d.send(t, child.conversation(t), taskGate(t, dir, "user-interrupted"))
	waitTaskFile(t, filepath.Join(dir, "user-interrupted.pid"))
	var ref childRef
	toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": taskGate(t, dir, "parent-interrupted"), "mode": "steer"}), &ref)
	releaseResultGate(t, dir, "user-interrupted")
	waitTaskFile(t, filepath.Join(dir, "parent-interrupted.pid"))
	d.stop()
	startDex(t, dir, "")
	turns := waitChildTurnsEnded(t, conn, child.conversation(t), 3)
	rows := resultRows(t, conn, parent)
	if len(rows) != 2 || rows[1].id != "result:"+turns[2].id || resultBody(t, rows[1].content)["outcome"] != "interrupted" {
		t.Fatalf("parent steer must report interruption, user input must not: turns=%+v results=%+v", turns, rows)
	}
}

func TestUserResultInsertFailureKeepsInputPending(t *testing.T) {
	t.Parallel()
	for _, trigger := range []string{
		`CREATE TRIGGER block_result BEFORE INSERT ON submissions WHEN NEW.kind = 'task_result' BEGIN SELECT RAISE(FAIL, 'result unavailable'); END`,
		`CREATE TRIGGER block_result BEFORE INSERT ON submissions WHEN NEW.kind = 'task_result' BEGIN SELECT RAISE(ABORT, 'other constraint'); END`,
	} {
		t.Run(trigger, func(t *testing.T) {
			dir := t.TempDir()
			d := startDex(t, dir, "")
			conn := controlWriteDB(t, dir)
			parent := newParent(t, d)
			execSQL(t, conn, trigger)
			child := spawnChild(t, d, parent, "Blocked result", "echo answer", "bash")
			d.watch(t, child.conversation(t)).until(t, "native answer", answeredWith("bash: answer"))
			d.stop()
			turns := childTurns(t, conn, child.conversation(t))
			if len(turns) != 1 || turns[0].status != "pending" || len(resultRows(t, conn, parent)) != 0 {
				t.Fatalf("result failure did not roll back settlement: %+v", turns)
			}
			execSQL(t, conn, `DROP TRIGGER block_result`)
			startDex(t, dir, "")
			waitChildTurnsEnded(t, conn, child.conversation(t), 1)
			if rows := resultRows(t, conn, parent); len(rows) != 1 {
				t.Fatalf("retry did not deliver exactly one result: %+v", rows)
			}
		})
	}
}

func TestUserFirstSettledParentInputOwnsSharedAnswer(t *testing.T) {
	t.Parallel()
	for _, blocked := range []int{0, 1} {
		t.Run(fmt.Sprintf("blocked-%d", blocked), func(t *testing.T) {
			dir := t.TempDir()
			d := startDex(t, dir, "")
			conn := controlWriteDB(t, dir)
			parent := newParent(t, d)
			child := spawnChild(t, d, parent, "Winner", taskGate(t, dir, "winner")+`; head -c 5000 /dev/zero | tr '\0' x`, "bash")
			waitTaskFile(t, filepath.Join(dir, "winner.pid"))
			var ref childRef
			toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": `head -c 5000 /dev/zero | tr '\0' x`, "mode": "steer"}), &ref)
			turns := childTurns(t, conn, child.conversation(t))
			if len(turns) != 2 {
				t.Fatalf("turns: %+v", turns)
			}
			execSQL(t, conn, fmt.Sprintf(`CREATE TRIGGER hold_winner BEFORE UPDATE OF status ON submissions WHEN OLD.id = '%s' BEGIN SELECT RAISE(FAIL, 'hold receipt'); END`, turns[blocked].id))
			releaseResultGate(t, dir, "winner")
			rows := waitResults(t, conn, parent, 1)
			winner := turns[1-blocked].id
			body := resultBody(t, rows[0].content)
			if rows[0].id != "result:"+winner || body["turn"] != winner {
				t.Fatalf("wrong result owner: %+v, turns=%+v", rows[0], turns)
			}
			var resultOrigin, sourceOrigin string
			if err := conn.QueryRowContext(t.Context(), `SELECT result.origin, source.origin FROM submissions result JOIN submissions turn ON turn.id = ? JOIN submissions source ON source.id = turn.parent_request_id WHERE result.id = ?`, winner, rows[0].id).Scan(&resultOrigin, &sourceOrigin); err != nil {
				t.Fatal(err)
			}
			if resultOrigin != sourceOrigin {
				t.Fatalf("winner origin %q, result origin %q", sourceOrigin, resultOrigin)
			}
			answer, _ := body["answer"].(string)
			if !strings.Contains(answer, "turn "+winner+", offset 4000") {
				t.Fatalf("cut answer does not point to the winner: %q", answer)
			}
			execSQL(t, conn, `DROP TRIGGER hold_winner`)
			waitChildTurnsEnded(t, conn, child.conversation(t), 2)
			if rows := resultRows(t, conn, parent); len(rows) != 1 {
				t.Fatalf("shared answer produced extra results: %+v", rows)
			}
		})
	}
}
