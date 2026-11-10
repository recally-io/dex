package app_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"uuid"
)

func TestUserForksCommittedHistoryWithStableID(t *testing.T) {
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "echo")
	source := d.mainChat(t)
	input := d.send(t, source, "fork this history")
	db := taskResultDB(t, dir)
	waitResultSubmission(t, db, input, "done")
	path := fmt.Sprintf("/api/agents/default/chats/%d", source)
	var history struct {
		Items []struct {
			Kind    string `json:"kind"`
			EntryID int64  `json:"entryId"`
		} `json:"items"`
	}
	d.must(t, http.MethodGet, path+"/history?before=9007199254740991&limit=100", nil, &history)
	var entry int64
	for _, item := range history.Items {
		if item.Kind == "user" {
			entry = item.EntryID
			break
		}
	}
	if entry == 0 {
		t.Fatalf("no committed user entry: %+v", history)
	}
	op := uuid.NewV7().String()
	body := map[string]any{"id": op, "entryId": entry}
	var child chatInfo
	d.must(t, http.MethodPost, path+"/fork", body, &child, http.StatusCreated)
	if child.ID == 0 || child.ID == source {
		t.Fatalf("child: %+v", child)
	}
	var replay chatInfo
	d.must(t, http.MethodPost, path+"/fork", body, &replay, http.StatusCreated)
	if replay.ID != child.ID {
		t.Fatalf("duplicate child: %+v %+v", child, replay)
	}
	status, raw := d.call(t, http.MethodPost, path+"/fork", map[string]any{"id": op, "entryId": entry + 999999})
	if status != http.StatusConflict {
		t.Fatalf("conflict=%d %s", status, raw)
	}
	assertHidden := func(id int64) {
		t.Helper()
		target := fmt.Sprintf("/api/agents/default/chats/%d", id)
		status, raw := d.call(t, http.MethodGet, target, nil)
		if status != http.StatusNotFound {
			t.Fatalf("hidden fork GET=%d %s", status, raw)
		}
		status, raw = d.call(t, http.MethodPost, target+"/messages", map[string]string{"id": uuid.NewV7().String(), "text": "must not run"})
		if status != http.StatusNotFound {
			t.Fatalf("hidden fork message=%d %s", status, raw)
		}
		if empty := d.newSession(t); empty.ID == id {
			t.Fatalf("empty Chat reused hidden fork %d", id)
		}
	}
	rejectedID := uuid.NewV7().String()
	status, raw = d.call(t, http.MethodPost, path+"/fork", map[string]any{"id": rejectedID, "entryId": entry + 999999})
	if status != http.StatusNotFound {
		t.Fatalf("unknown entry=%d %s", status, raw)
	}
	var rejectedTarget int64
	if err := db.QueryRowContext(t.Context(), "SELECT target_conversation_id FROM fork_operations WHERE operation_id=? AND status='rejected'", rejectedID).Scan(&rejectedTarget); err != nil {
		t.Fatal(err)
	}
	assertHidden(rejectedTarget)
	var other struct {
		ID string `json:"id"`
	}
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "Other", "provider": "faux", "modelId": "echo", "thinkingLevel": "off"}, &other, http.StatusCreated)
	status, raw = d.call(t, http.MethodPost, fmt.Sprintf("/api/agents/%s/chats/%d/fork", other.ID, source), map[string]any{"id": uuid.NewV7().String(), "entryId": entry})
	if status != http.StatusNotFound {
		t.Fatalf("cross-agent fork=%d %s", status, raw)
	}
	// A failed control acknowledgement leaves the native operation to reconcile.
	rw, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "control.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	if _, err := rw.ExecContext(t.Context(), `CREATE TRIGGER fork_ack_failure BEFORE UPDATE OF execution_id ON conversations BEGIN SELECT RAISE(FAIL, 'fork ack unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	pendingID := uuid.NewV7().String()
	pendingBody := map[string]any{"id": pendingID, "entryId": entry}
	status, raw = d.call(t, http.MethodPost, path+"/fork", pendingBody)
	if status < 500 {
		t.Fatalf("expected acknowledgement failure: %d %s", status, raw)
	}
	var pendingStatus string
	var pendingTarget int64
	if err := db.QueryRowContext(t.Context(), "SELECT status, target_conversation_id FROM fork_operations WHERE operation_id = ?", pendingID).Scan(&pendingStatus, &pendingTarget); err != nil || pendingStatus != "pending" {
		t.Fatalf("pending receipt: %s %v", pendingStatus, err)
	}
	assertHidden(pendingTarget)
	if _, err := rw.ExecContext(t.Context(), `DROP TRIGGER fork_ack_failure`); err != nil {
		t.Fatal(err)
	}
	d.stop()
	d = startDex(t, dir, "")
	var recovered chatInfo
	d.must(t, http.MethodPost, path+"/fork", pendingBody, &recovered, http.StatusCreated)
	if recovered.ID == 0 || recovered.ID == child.ID {
		t.Fatalf("recovered fork: %+v", recovered)
	}
	d.must(t, http.MethodPost, path+"/fork", body, &replay, http.StatusCreated)
	if replay.ID != child.ID {
		t.Fatalf("restart created another child: %+v", replay)
	}
	t.Logf("source=%d entry=%d fork=%d operation=%s", source, entry, child.ID, op)
}
