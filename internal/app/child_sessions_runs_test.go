package app_test

import (
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
)

func TestUserFailedSharedRunSendsEachResultAfterALaterRunStarted(t *testing.T) {
	t.Parallel()
	// Given a child run that a parent steer joined and that the model failed, while the control
	// database refused to settle the receipts of both inputs.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	write := controlWriteDB(t, dir)
	parent := newParent(t, d)
	child := spawnChild(t, d, parent, "Failure receipt", childJSON(t, map[string]any{"tool": "bash", "command": taskGate(t, dir, "failed")}), "spawn")
	waitTaskFile(t, filepath.Join(dir, "failed.pid"))
	execSQL(t, write, `CREATE TRIGGER hold_failed BEFORE UPDATE OF status ON submissions WHEN OLD.parent_request_id IS NOT NULL BEGIN SELECT RAISE(FAIL, 'hold receipt'); END`)
	var ref childRef
	toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": "invalid JSON fails the model", "mode": "steer"}), &ref)
	releaseResultGate(t, dir, "failed")
	d.watch(t, child.conversation(t)).until(t, "native failure", func(tr *transcript) bool {
		if tr.running {
			return false
		}
		for _, it := range tr.items {
			if it.Failure == "model_error" {
				return true
			}
		}
		return false
	})
	// When a later run starts. It replaces the run failure that the native live state shows.
	toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": childJSON(t, map[string]any{"tool": "bash", "command": taskGate(t, dir, "next")})}), &ref)
	waitTaskFile(t, filepath.Join(dir, "next.pid"))
	execSQL(t, write, `DROP TRIGGER hold_failed`)
	// Then each failed parent input gets its own result.
	eventually(t, "failed receipts settled", func() (bool, any) {
		turns := childTurns(t, write, child.conversation(t))
		return len(turns) == 3 && turns[0].status != "pending" && turns[1].status != "pending", turns
	})
	if rows := resultRows(t, write, parent); len(rows) != 2 {
		t.Fatalf("two failed parent inputs produced %d results after the next run started: %+v", len(rows), rows)
	}
	releaseResultGate(t, dir, "next")
}

func TestUserSharedRunSendsNoSecondResultAfter256LaterInputs(t *testing.T) {
	t.Parallel()
	// Given a run that a parent steer joined and that answered natively, while the control database
	// refused to settle the receipt of the steer.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	write := controlWriteDB(t, dir)
	parent := newParent(t, d)
	child := spawnChild(t, d, parent, "Old steer", taskGate(t, dir, "old"), "bash")
	waitTaskFile(t, filepath.Join(dir, "old.pid"))
	execSQL(t, write, `CREATE TRIGGER hold_steer BEFORE UPDATE OF status ON submissions WHEN OLD.parent_request_id IS NOT NULL AND OLD.when_busy = 'steer' BEGIN SELECT RAISE(FAIL, 'hold steer receipt'); END`)
	var ref childRef
	toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": "echo shared", "mode": "steer"}), &ref)
	releaseResultGate(t, dir, "old")
	waitResults(t, write, parent, 1)
	steer := childTurns(t, write, child.conversation(t))[1].id
	// And 256 later inputs that run and settle.
	d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/default/chats/%s/settings", child.ID), map[string]string{"provider": "faux", "modelId": "echo", "thinkingLevel": "off"}, nil)
	for i := range 256 {
		d.send(t, child.conversation(t), fmt.Sprintf("later-%03d", i))
	}
	d.watch(t, child.conversation(t)).until(t, "last later input answered", answeredWith("echo: later-255"))
	eventually(t, "256 later receipts settled", func() (bool, any) {
		turns := childTurns(t, write, child.conversation(t))
		done := 0
		for _, turn := range turns {
			if turn.status == "done" {
				done++
			}
		}
		return len(turns) == 258 && done == 257, fmt.Sprintf("turns=%d done=%d", len(turns), done)
	})
	// When dex restarts with the database working again.
	d.stop()
	execSQL(t, write, `DROP TRIGGER hold_steer`)
	startDex(t, dir, "")
	waitResultSubmission(t, write, steer, "done")
	// Then the old shared run still has one result.
	if rows := resultRows(t, write, parent); len(rows) != 1 {
		t.Fatalf("the old shared run produced %d results after 256 later inputs: %+v", len(rows), rows)
	}
}

func TestUserSteerThatStartsTheNextRunSendsItsOwnResult(t *testing.T) {
	t.Parallel()
	// Given a child run that is writing its final answer, so no tool step is left to take a steer.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := newParent(t, d)
	child := spawnChild(t, d, parent, "Late steer", taskGate(t, dir, "late")+"; head -c 8000 /dev/zero | tr '\\0' x", "bash")
	waitTaskFile(t, filepath.Join(dir, "late.pid"))
	watch := d.watch(t, child.conversation(t))
	releaseResultGate(t, dir, "late")
	watch.until(t, "final answer streaming", func(tr *transcript) bool {
		n := len(tr.items)
		return tr.running && n > 0 && tr.items[n-1].Kind == "assistant"
	})
	// When the parent steers it.
	var ref childRef
	toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": "echo late steer", "mode": "steer"}), &ref)
	// Then the steer starts its own run, after the first one, and sends its own result.
	rows := waitResults(t, conn, parent, 2)
	if resultBody(t, rows[1].content)["answer"] != "bash: late steer" {
		t.Fatalf("result of the steer: %+v", rows[1])
	}
}
