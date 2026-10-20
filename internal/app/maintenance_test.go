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

func TestUserUpdateWaitsForActiveTool(t *testing.T) {
	// Given a real bash tool that waits for an explicit release file.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "bash")
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	release := filepath.Join(dir, "release")
	command := fmt.Sprintf("touch '%s'; while [ ! -f '%s' ]; do sleep 0.02; done; echo finished", started, release)
	id := d.newChat(t, command)
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })
	eventually(t, "tool running", func() (bool, any) { _, err := os.Stat(started); return err == nil, err })
	// When an updater requests maintenance, the active tool makes acquire fail.
	d.must(t, http.MethodPost, "/api/maintenance", nil, nil, http.StatusConflict)
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	d.watch(t, id).until(t, "tool finishes", answered("bash: finished"))
	// Then the failed acquire holds no gate, and later work can run.
	d.send(t, id, "echo next")
	d.watch(t, id).until(t, "next run", answered("bash: next"))
	eventually(t, "maintenance acquired", func() (bool, any) {
		status, body := d.call(t, http.MethodPost, "/api/maintenance", nil)
		return status == 200, body
	})
	d.must(t, http.MethodDelete, "/api/maintenance", nil, nil)
}

func TestUserMaintenanceRejectsWorkUntilReleaseOrRestart(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprintf("restart=%t", restart), func(t *testing.T) {
			// Given an idle native runtime under maintenance and an empty Chat.
			data := t.TempDir()
			d := startDex(t, data, "")
			d.setModel(t, "bash")
			conversation := d.newSession(t)
			d.must(t, http.MethodPost, "/api/maintenance", nil, nil)
			marker := filepath.Join(t.TempDir(), "executed")
			requestID := uuid.NewV7().String()
			path := fmt.Sprintf("/api/agents/default/chats/%d/messages", conversation.ID)
			body := map[string]string{"id": requestID, "text": fmt.Sprintf("touch '%s'; echo released", marker)}

			// When submission is refused, it stores no intent that could execute later.
			d.must(t, http.MethodPost, path, body, nil, http.StatusConflict)
			conn, err := sql.Open("sqlite", "file:"+filepath.Join(data, "control.sqlite")+"?mode=ro")
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			var count int
			if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM submissions WHERE id = ?", requestID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("maintenance rejection left an executable receipt")
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("tool escaped maintenance: %v", err)
			}

			// Then the same input can be accepted once maintenance ends.
			if restart {
				d.stop()
				d = startDex(t, data, "")
			} else {
				d.must(t, http.MethodDelete, "/api/maintenance", nil, nil)
			}
			d.must(t, http.MethodPost, path, body, nil, http.StatusAccepted)
			d.watch(t, conversation.ID).until(t, "retried work after maintenance", answered("bash: released"))
			if _, err := os.Stat(marker); err != nil {
				t.Fatal(err)
			}
		})
	}
}
