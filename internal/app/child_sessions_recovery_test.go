package app_test

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// controlWriteDB opens control.sqlite for writing, to break one write of the control process.
func controlWriteDB(t *testing.T, dir string) *sql.DB {
	t.Helper()
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "control.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

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

func TestUserParentSteerOfUserRunDeliversOneAnswer(t *testing.T) {
	t.Parallel()
	// Given a child run that the user started and a parent steer that joined it.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := newParent(t, d)
	child := spawnChild(t, d, parent, "User run", "echo initial", "bash")
	waitResults(t, conn, parent, 1)
	d.send(t, child.conversation(t), taskGate(t, dir, "user"))
	waitTaskFile(t, filepath.Join(dir, "user.pid"))
	var ref childRef
	toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": "echo parent steer", "mode": "steer"}), &ref)
	// When the run ends.
	releaseResultGate(t, dir, "user")
	// Then the parent steer delivers the shared answer once.
	waitChildTurnsEnded(t, conn, child.conversation(t), 3)
	if rows := resultRows(t, conn, parent); len(rows) != 2 {
		t.Fatalf("results: %+v", rows)
	}
}

func TestUserResultInsertFailureKeepsInputPending(t *testing.T) {
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

func TestUserUnrelatedUniqueConflictKeepsInputPending(t *testing.T) {
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := controlWriteDB(t, dir)
	parent := newParent(t, d)
	execSQL(t, conn, `CREATE UNIQUE INDEX test_result_collision ON submissions ((1)) WHERE kind = 'task_result'`)
	execSQL(t, conn, fmt.Sprintf(`INSERT INTO submissions (id, conversation_id, content, attachments, origin, kind, status, created_at) VALUES ('collision', %d, 'other', 'null', 'web', 'task_result', 'done', 1)`, parent))
	child := spawnChild(t, d, parent, "Unique conflict", "echo answer", "bash")
	d.watch(t, child.conversation(t)).until(t, "native answer", answeredWith("bash: answer"))
	d.stop()
	turns := childTurns(t, conn, child.conversation(t))
	if len(turns) != 1 || turns[0].status != "pending" {
		t.Fatalf("unrelated unique conflict committed the receipt: %+v", turns)
	}
	execSQL(t, conn, `DROP INDEX test_result_collision`)
	startDex(t, dir, "")
	waitChildTurnsEnded(t, conn, child.conversation(t), 1)
	if rows := resultRows(t, conn, parent); len(rows) != 2 {
		t.Fatalf("retry did not deliver the result: %+v", rows)
	}
}

func TestUserFirstSettledParentInputOwnsSharedAnswer(t *testing.T) {
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

func TestUserIdenticalAnswersFromSeparateRunsEachDeliver(t *testing.T) {
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := newParent(t, d)
	child := spawnChild(t, d, parent, "Repeated answer", "echo same", "bash")
	waitResults(t, conn, parent, 1)
	var ref childRef
	toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": "echo same"}), &ref)
	rows := waitResults(t, conn, parent, 2)
	var first, second int64
	if err := conn.QueryRowContext(t.Context(), `SELECT native_answer_id FROM submissions WHERE id = ?`, rows[0].id).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(t.Context(), `SELECT native_answer_id FROM submissions WHERE id = ?`, rows[1].id).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if first == 0 || second == 0 || first == second || resultBody(t, rows[0].content)["answer"] != resultBody(t, rows[1].content)["answer"] {
		t.Fatalf("identical answers have invalid identities: %d, %d, %+v", first, second, rows)
	}
}

func TestUserSteerToAnIdleChildSendsItsOwnResult(t *testing.T) {
	t.Parallel()
	// Given a child that has finished its first turn.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := newParent(t, d)
	child := spawnChild(t, d, parent, "Idle", "echo first", "bash")
	waitResults(t, conn, parent, 1)
	// When the parent sends a message with mode steer.
	var ref childRef
	toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": "echo second", "mode": "steer"}), &ref)
	// Then the message starts its own run and sends its own result.
	rows := waitResults(t, conn, parent, 2)
	if resultBody(t, rows[1].content)["answer"] != "bash: second" {
		t.Fatalf("second result: %+v", rows[1])
	}
}

func TestUserParentStopLeavesAnIndependentChildTurn(t *testing.T) {
	t.Parallel()
	// Given a child that finished a parent turn, and then runs a turn that the user started.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := newParent(t, d)
	child := spawnChild(t, d, parent, "Reused child", "echo initial", "bash")
	waitResults(t, conn, parent, 1)
	d.send(t, child.conversation(t), taskGate(t, dir, "independent"))
	waitTaskFile(t, filepath.Join(dir, "independent.pid"))
	// When the user stops the parent chat.
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/abort", parent), nil, nil)
	// Then the turn of the user in the child keeps running.
	if turns := childTurns(t, conn, child.conversation(t)); turns[1].status != "pending" {
		t.Fatalf("the stop of the parent ended the turn of the user: %+v", turns[1])
	}
	releaseResultGate(t, dir, "independent")
	waitDoneChildTurns(t, conn, child.conversation(t), 2)
}

func TestUserParentStopEndsWhatTheParentStartedInAChild(t *testing.T) {
	t.Parallel()
	// Given a child that runs a parent turn.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := newParent(t, d)
	child := spawnChild(t, d, parent, "Parent turn", taskGate(t, dir, "parent-turn"), "bash")
	waitTaskFile(t, filepath.Join(dir, "parent-turn.pid"))
	// When the user stops the parent chat.
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/abort", parent), nil, nil)
	// Then the turn ends as stopped, and the child sends no result.
	turns := waitChildTurnsEnded(t, conn, child.conversation(t), 1)
	if turns[0].status != "unanswered" || turns[0].reason != "stopped" {
		t.Fatalf("turn: %+v", turns[0])
	}
	if rows := resultRows(t, conn, parent); len(rows) != 0 {
		t.Fatalf("results: %+v", rows)
	}
}
