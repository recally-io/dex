package app_test

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
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

func TestUserFailedSharedRunSendsOnlyTheStarterResult(t *testing.T) {
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
	// Then the parent gets one failed result, the result of the starter.
	turns := waitChildTurnsEnded(t, conn, child.conversation(t), 2)
	rows := resultRows(t, conn, parent)
	if len(rows) != 1 || rows[0].id != "result:"+turns[0].id {
		t.Fatalf("results %+v for turns %+v", rows, turns)
	}
	if body := resultBody(t, rows[0].content); body["outcome"] != "failed" {
		t.Fatalf("result: %v", body)
	}
}

func TestUserSteerThatJoinedARunOfTheUserSendsNoResult(t *testing.T) {
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
	// Then no result follows: the starter is a user turn, and the steer is covered by its run.
	waitChildTurnsEnded(t, conn, child.conversation(t), 3)
	if rows := resultRows(t, conn, parent); len(rows) != 1 {
		t.Fatalf("results: %+v", rows)
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
