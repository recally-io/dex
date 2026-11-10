package app_test

import (
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"uuid"
)

func TestUserHandoffRefusesBusyWithoutSavingIntent(t *testing.T) {
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "bash")
	db := taskResultDB(t, dir)
	id := d.mainChat(t)
	input := d.send(t, id, taskGate(t, dir, "busy-handoff"))
	waitTaskFile(t, filepath.Join(dir, "busy-handoff.pid"))
	key := uuid.NewV7().String()
	url := fmt.Sprintf("/api/agents/default/chats/%d/clear", id)
	body := map[string]string{"id": key, "handoff": "later"}
	status, raw := d.call(t, http.MethodPost, url, body)
	if status != http.StatusConflict {
		t.Fatalf("busy clear=%d %s", status, raw)
	}
	var count int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE id=?", key).Scan(&count); err != nil || count != 0 {
		t.Fatalf("busy clear saved receipt count=%d err=%v", count, err)
	}
	releaseResultGate(t, dir, "busy-handoff")
	waitResultSubmission(t, db, input, "done")
	d.must(t, http.MethodPost, url, body, nil, http.StatusAccepted)
	waitResultSubmission(t, db, key, "done")
	t.Logf("busy refusal kept no receipt; idle retry %s succeeded", key)
}
