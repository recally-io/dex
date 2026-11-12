package app_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestRound3GeneratePreRunPeersFixture(t *testing.T) {
	target := os.Getenv("DEX_REVIEW_FIXTURE")
	if target == "" {
		t.Skip("fixture generation is explicit")
	}
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := controlWriteDB(t, dir)
	parent := newParent(t, d)
	child := spawnChild(t, d, parent, "Before runPeers", taskGate(t, dir, "old-version"), "bash")
	waitTaskFile(t, filepath.Join(dir, "old-version.pid"))
	execSQL(t, conn, `CREATE TRIGGER hold_legacy BEFORE UPDATE OF status ON submissions WHEN OLD.parent_request_id IS NOT NULL BEGIN SELECT RAISE(FAIL, 'hold legacy receipt'); END`)
	var ref childRef
	toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": "echo shared legacy answer", "mode": "steer"}), &ref)
	releaseResultGate(t, dir, "old-version")
	d.watch(t, child.conversation(t)).until(t, "shared native answer", answeredWith("bash: shared legacy answer"))
	turns := childTurns(t, conn, child.conversation(t))
	if len(turns) != 2 || turns[0].status != "pending" || turns[1].status != "pending" {
		t.Fatalf("fixture precondition: %+v", turns)
	}
	d.stop()
	execSQL(t, conn, `DROP TRIGGER hold_legacy`)
	for _, relative := range []string{"control.sqlite", filepath.Join("agents", "default", "state.sqlite")} {
		dest := filepath.Join(target, relative)
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, relative)+"?mode=ro&_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.ExecContext(t.Context(), "VACUUM INTO ?", dest)
		_ = db.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Log("saved a shared native answer with two pending control receipts")
}

func TestRound3UpgradePreRunPeersSharedAnswer(t *testing.T) {
	source := os.Getenv("DEX_REVIEW_FIXTURE")
	if source == "" {
		t.Skip("fixture path is required")
	}
	dir := t.TempDir()
	for _, relative := range []string{"control.sqlite", filepath.Join("agents", "default", "state.sqlite")} {
		data, err := os.ReadFile(filepath.Join(source, relative))
		if err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(dir, relative)
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	d := startDex(t, dir, "")
	d.mainChat(t)
	conn := taskResultDB(t, dir)
	turns := waitChildTurnsEnded(t, conn, 2, 2)
	rows := resultRows(t, conn, 1)
	if len(rows) != 1 {
		t.Fatalf("pre-runPeers shared run produced %d results after upgrade: turns=%+v results=%+v", len(rows), turns, rows)
	}
	var childID, answerID sql.NullInt64
	if err := conn.QueryRowContext(t.Context(), `SELECT result_child_id, native_answer_id FROM submissions WHERE id = ?`, rows[0].id).Scan(&childID, &answerID); err != nil {
		t.Fatal(err)
	}
	if !childID.Valid || childID.Int64 != 2 || !answerID.Valid || answerID.Int64 == 0 {
		t.Fatalf("result lacks its native answer key: child=%v answer=%v", childID, answerID)
	}
	d.stop()
	startDex(t, dir, "")
	if rows := resultRows(t, conn, 1); len(rows) != 1 {
		t.Fatalf("second restart added results: %+v", rows)
	}
}
