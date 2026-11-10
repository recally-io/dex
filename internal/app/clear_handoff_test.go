package app_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"uuid"
)

func TestUserClearsContextWithPassiveHandoff(t *testing.T) {
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "context")
	id := d.mainChat(t)
	db := taskResultDB(t, dir)
	first := d.send(t, id, "old context")
	waitResultSubmission(t, db, first, "done")
	path := fmt.Sprintf("/api/agents/default/chats/%d", id)
	resetID := uuid.NewV7().String()
	body := map[string]string{"id": resetID, "handoff": "keep this"}
	d.must(t, http.MethodPost, path+"/clear", body, nil, http.StatusAccepted)
	waitResultSubmission(t, db, resetID, "done")
	var count int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE conversation_id=?", id).Scan(&count); err != nil || count != 2 {
		t.Fatalf("handoff started input: count=%d err=%v", count, err)
	}
	d.must(t, http.MethodPost, path+"/clear", body, nil, http.StatusAccepted)
	status, raw := d.call(t, http.MethodPost, path+"/clear", map[string]string{"id": resetID, "handoff": "other"})
	if status != http.StatusConflict {
		t.Fatalf("changed handoff=%d %s", status, raw)
	}
	d.stop()
	d = startDex(t, dir, "")
	d.must(t, http.MethodPost, path+"/clear", body, nil, http.StatusAccepted)
	next := d.send(t, id, "continue")
	waitResultSubmission(t, db, next, "done")
	var answer string
	if err := db.QueryRowContext(t.Context(), "SELECT answer FROM submissions WHERE id=?", next).Scan(&answer); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer, "keep this") || !strings.Contains(answer, "continue") || strings.Contains(answer, "old context") {
		t.Fatalf("wrong new context: %s", answer)
	}
	var history struct {
		Items []struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		} `json:"items"`
	}
	d.must(t, http.MethodGet, path+"/history?before=9007199254740991&limit=100", nil, &history)
	found := false
	for _, item := range history.Items {
		if item.Text == "old context" {
			found = true
		}
	}
	if !found {
		t.Fatal("clear removed history")
	}
	t.Logf("reset=%s handoff was passive and survived restart", resetID)
}

func TestUserHandoffRecoversAfterNativeCommit(t *testing.T) {
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "context")
	id := d.mainChat(t)
	db := taskResultDB(t, dir)
	first := d.send(t, id, "old")
	waitResultSubmission(t, db, first, "done")
	rw, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "control.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	if _, err = rw.ExecContext(t.Context(), `CREATE TRIGGER reset_ack_failure BEFORE UPDATE OF admitted_at ON submissions WHEN NEW.kind='context_reset' BEGIN SELECT RAISE(FAIL, 'reset ack unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	key := uuid.NewV7().String()
	path := fmt.Sprintf("/api/agents/default/chats/%d", id)
	body := map[string]string{"id": key, "handoff": "durable handoff"}
	d.must(t, http.MethodPost, path+"/clear", body, nil, http.StatusAccepted)
	var status string
	var admitted sql.NullInt64
	if err = db.QueryRowContext(t.Context(), "SELECT status,admitted_at FROM submissions WHERE id=?", key).Scan(&status, &admitted); err != nil || status != "pending" || admitted.Valid {
		t.Fatalf("receipt=%s admitted=%v err=%v", status, admitted, err)
	}
	if _, err = rw.ExecContext(t.Context(), `DROP TRIGGER reset_ack_failure`); err != nil {
		t.Fatal(err)
	}
	d.stop()
	d = startDex(t, dir, "")
	waitResultSubmission(t, db, key, "done")
	d.must(t, http.MethodPost, path+"/clear", body, nil, http.StatusAccepted)
	next := d.send(t, id, "go")
	waitResultSubmission(t, db, next, "done")
	var answer string
	if err = db.QueryRowContext(t.Context(), "SELECT answer FROM submissions WHERE id=?", next).Scan(&answer); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer, "durable handoff") || strings.Contains(answer, "old") {
		t.Fatalf("recovered context: %s", answer)
	}
	var history struct {
		Items []struct {
			Kind     string `json:"kind"`
			Boundary string `json:"boundary"`
		} `json:"items"`
	}
	d.must(t, http.MethodGet, path+"/history?before=9007199254740991&limit=100", nil, &history)
	resets := 0
	for _, item := range history.Items {
		if item.Kind == "boundary" && item.Boundary == "reset" {
			resets++
		}
	}
	if resets != 1 {
		t.Fatalf("reset entries=%d", resets)
	}
	t.Logf("reset %s recovered once", key)
}
