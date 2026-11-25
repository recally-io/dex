package app_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestUserGetsTelegramAnswerDuringSlowInboundDownload(t *testing.T) {
	t.Parallel()
	// Given an answer held by a tool and a download held by the Bot API.
	f := newFakeTelegram(t)
	download := make(chan struct{})
	f.fileGate = download
	defer close(download)
	f.files["notes.txt"] = "notes"
	d := startTelegram(t, f, t.TempDir(), 42)
	d.setModel(t, "bash")
	gate := t.TempDir()
	ready, release := filepath.Join(gate, "ready"), filepath.Join(gate, "release")
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	f.message(1, 42, 7, fmt.Sprintf("touch %q; while [ ! -f %q ]; do sleep 0.02; done; printf first", ready, release))
	eventually(t, "tool waiting", func() (bool, any) {
		_, err := os.Stat(ready)
		return err == nil, err
	})
	// When the next inbound attachment starts a slow download.
	f.mu.Lock()
	f.updates = append(f.updates, map[string]any{"update_id": int64(2), "message": map[string]any{
		"from": map[string]any{"id": 42}, "chat": map[string]any{"id": 7, "type": "private"},
		"document": map[string]any{"file_id": "notes.txt", "file_name": "notes.txt"},
	}})
	f.mu.Unlock()
	f.nextEvent(t, "download")
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Then the existing answer arrives before the download is released.
	if got := f.next(t); got.Text != "bash: first" {
		t.Fatalf("answer during download = %+v", got)
	}
}
