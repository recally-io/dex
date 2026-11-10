package app_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"uuid"
)

type compactView struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func TestUserRequestsCompactionWithStableReceipt(t *testing.T) {
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "echo")
	id := d.mainChat(t)
	db := taskResultDB(t, dir)
	path := fmt.Sprintf("/api/agents/default/chats/%d", id)
	key := uuid.NewV7().String()
	body := map[string]string{"id": key}
	var out compactView
	d.must(t, http.MethodPost, path+"/compactions", body, &out, http.StatusAccepted)
	if out.Status != "no_op" || out.ID != key {
		t.Fatalf("empty: %+v", out)
	}
	first := d.send(t, id, "short")
	waitResultSubmission(t, db, first, "done")
	d.must(t, http.MethodPost, path+"/compactions", body, &out, http.StatusAccepted)
	if out.Status != "no_op" {
		t.Fatalf("empty replay: %+v", out)
	}
	key = uuid.NewV7().String()
	body = map[string]string{"id": key}
	d.must(t, http.MethodPost, path+"/compactions", body, &out, http.StatusAccepted)
	eventually(t, "compaction no-op", func() (bool, any) {
		d.must(t, http.MethodGet, path+"/compactions/"+key, nil, &out)
		return out.Status == "no_op", out
	})
	d.stop()
	d = startDex(t, dir, "")
	d.must(t, http.MethodPost, path+"/compactions", body, &out, http.StatusAccepted)
	if out.Status != "no_op" {
		t.Fatalf("replay: %+v", out)
	}
	other := d.newSession(t)
	otherPath := fmt.Sprintf("/api/agents/default/chats/%d", other.ID)
	status, raw := d.call(t, http.MethodPost, otherPath+"/compactions", body)
	if status != http.StatusConflict {
		t.Fatalf("different chat=%d %s", status, raw)
	}
	status, raw = d.call(t, http.MethodGet, otherPath+"/compactions/"+key, nil)
	if status != http.StatusNotFound {
		t.Fatalf("foreign operation=%d %s", status, raw)
	}
	t.Logf("compaction receipt %s persisted", key)
}

func TestUserCompactionRecoversAfterNativeCommit(t *testing.T) {
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "echo")
	id := d.mainChat(t)
	db := taskResultDB(t, dir)
	first := d.send(t, id, "history")
	waitResultSubmission(t, db, first, "done")
	rw, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "control.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	if _, err = rw.ExecContext(t.Context(), `CREATE TRIGGER compact_ack_failure BEFORE UPDATE OF status ON compact_operations WHEN NEW.status='admitted' BEGIN SELECT RAISE(FAIL, 'compact ack unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	key := uuid.NewV7().String()
	path := fmt.Sprintf("/api/agents/default/chats/%d", id)
	body := map[string]string{"id": key}
	status, raw := d.call(t, http.MethodPost, path+"/compactions", body)
	if status < 500 {
		t.Fatalf("expected failure: %d %s", status, raw)
	}
	var state string
	if err = db.QueryRowContext(t.Context(), "SELECT status FROM compact_operations WHERE operation_id=?", key).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("receipt=%s err=%v", state, err)
	}
	if _, err = rw.ExecContext(t.Context(), `DROP TRIGGER compact_ack_failure`); err != nil {
		t.Fatal(err)
	}
	d.stop()
	d = startDex(t, dir, "")
	var out compactView
	d.must(t, http.MethodPost, path+"/compactions", body, &out, http.StatusAccepted)
	eventually(t, "recovered compaction", func() (bool, any) {
		d.must(t, http.MethodGet, path+"/compactions/"+key, nil, &out)
		return out.Status == "no_op", out
	})
	var taskID int64
	if err = db.QueryRowContext(t.Context(), "SELECT task_id FROM compact_operations WHERE operation_id=?", key).Scan(&taskID); err != nil || taskID == 0 {
		t.Fatalf("task=%d err=%v", taskID, err)
	}
	d.must(t, http.MethodPost, path+"/compactions", body, &out, http.StatusAccepted)
	var again int64
	if err = db.QueryRowContext(t.Context(), "SELECT task_id FROM compact_operations WHERE operation_id=?", key).Scan(&again); err != nil || again != taskID {
		t.Fatalf("replay task=%d want=%d err=%v", again, taskID, err)
	}
	t.Logf("recovered compaction task %d", taskID)
}

func TestUserCanCompactDuringActiveRun(t *testing.T) {
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "bash")
	db := taskResultDB(t, dir)
	id := d.mainChat(t)
	input := d.send(t, id, taskGate(t, dir, "compact-busy"))
	waitTaskFile(t, filepath.Join(dir, "compact-busy.pid"))
	key := uuid.NewV7().String()
	path := fmt.Sprintf("/api/agents/default/chats/%d", id)
	var out compactView
	d.must(t, http.MethodPost, path+"/compactions", map[string]string{"id": key}, &out, http.StatusAccepted)
	var chat chatInfo
	d.must(t, http.MethodGet, path, nil, &chat)
	if !chat.Busy {
		t.Fatal("compact stopped the active run")
	}
	releaseResultGate(t, dir, "compact-busy")
	waitResultSubmission(t, db, input, "done")
	eventually(t, "compaction settles", func() (bool, any) {
		d.must(t, http.MethodGet, path+"/compactions/"+key, nil, &out)
		return out.Status == "no_op" || out.Status == "applied", out
	})
	t.Logf("busy compaction %s settled %s", key, out.Status)
}

func TestUserAgentCompactReturnsReceiptInsideRun(t *testing.T) {
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "tool")
	db := taskResultDB(t, dir)
	id := d.mainChat(t)
	req := d.send(t, id, `compact_context {}`)
	waitResultSubmission(t, db, req, "done")
	var count int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM compact_operations WHERE conversation_id=? AND status='admitted'", id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("receipt count=%d err=%v", count, err)
	}
	t.Log("tool returned before its run boundary")
}
