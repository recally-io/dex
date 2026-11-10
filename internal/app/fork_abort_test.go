package app_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"uuid"
)

func TestUserCannotForkBeforePendingAbortAcknowledgement(t *testing.T) {
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "bash")
	id := d.mainChat(t)
	path := fmt.Sprintf("/api/agents/default/chats/%d", id)
	d.send(t, id, taskGate(t, dir, "fork-abort"))
	waitTaskFile(t, filepath.Join(dir, "fork-abort.pid"))
	var history struct {
		Items []struct {
			Kind    string `json:"kind"`
			EntryID int64  `json:"entryId"`
		} `json:"items"`
	}
	d.must(t, http.MethodGet, path+"/history?before=9007199254740991", nil, &history)
	var entry int64
	for _, item := range history.Items {
		if item.Kind == "user" {
			entry = item.EntryID
			break
		}
	}
	if entry == 0 {
		t.Fatal("no entry")
	}
	d.stop()
	rw, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "control.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	if _, err = rw.ExecContext(t.Context(), "UPDATE conversations SET abort_pending=1 WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	if _, err = rw.ExecContext(t.Context(), `CREATE TRIGGER hold_abort_ack BEFORE UPDATE OF abort_pending ON conversations WHEN NEW.abort_pending=0 BEGIN SELECT RAISE(FAIL, 'abort ack unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	d = startDex(t, dir, "")
	key := uuid.NewV7().String()
	status, raw := d.call(t, http.MethodPost, path+"/fork", map[string]any{"id": key, "entryId": entry})
	if status < 500 {
		t.Fatalf("fork passed abort failure: %d %s", status, raw)
	}
	var n int
	if err = rw.QueryRowContext(t.Context(), "SELECT count(*) FROM fork_operations WHERE operation_id=?", key).Scan(&n); err != nil || n != 0 {
		t.Fatalf("saved premature fork %d %v", n, err)
	}
	if _, err = rw.ExecContext(t.Context(), `DROP TRIGGER hold_abort_ack`); err != nil {
		t.Fatal(err)
	}
	var child chatInfo
	d.must(t, http.MethodPost, path+"/fork", map[string]any{"id": key, "entryId": entry}, &child, http.StatusCreated)
	if child.Busy {
		t.Fatal("fork started execution")
	}
	data, err := os.ReadFile(filepath.Join(dir, "fork-abort.output"))
	if err != nil || string(data) != "real artifact\n" {
		t.Fatalf("original artifact=%q err=%v", data, err)
	}
	t.Logf("abort acknowledgement gated HTTP fork %s", key)
}
