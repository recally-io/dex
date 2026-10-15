package app_test

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestUserMaintenanceQueuesWorkUntilReleaseOrRestart(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprintf("restart=%t", restart), func(t *testing.T) {
			// Given an idle real worker under a maintenance lease.
			data := t.TempDir()
			d := startDex(t, data, "")
			d.setModel(t, "bash")
			d.must(t, http.MethodPost, "/api/maintenance", nil, nil)
			// When the user submits tool work, it is stored but must not execute.
			marker := filepath.Join(t.TempDir(), "executed")
			id := d.newChat(t, fmt.Sprintf("touch '%s'; echo released", marker))
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				d.must(t, http.MethodPost, "/api/maintenance", nil, nil)
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("tool escaped maintenance: %v", err)
				}
			}
			chats := d.chats(t)
			if len(chats) != 1 || !chats[0].Busy {
				t.Fatalf("message was not queued: %+v", chats)
			}
			// Then release, or a process restart, dispatches the stored work.
			if restart {
				d.stop()
				d = startDex(t, data, "")
			} else {
				d.must(t, http.MethodDelete, "/api/maintenance", nil, nil)
			}
			d.watch(t, id).until(t, "queued work after maintenance", answered("bash: released"))
			if _, err := os.Stat(marker); err != nil {
				t.Fatal(err)
			}
		})
	}
}
