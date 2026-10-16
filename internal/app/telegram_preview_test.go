package app_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUserQueuedTelegramMessageDoesNotTakeFirstPreview(t *testing.T) {
	// Given a tool run that waits until the next message is queued.
	f := newFakeTelegram(t)
	f.rejectDraft = true
	d := startTelegram(t, f, t.TempDir(), 42)
	d.setModel(t, "bash")
	gateDir := t.TempDir()
	ready := filepath.Join(gateDir, "ready")
	release := filepath.Join(gateDir, "release")
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	answer := "bash: " + strings.TrimSpace(strings.Repeat("alpha ", 400))
	command := fmt.Sprintf("touch %q; while [ ! -f %q ]; do sleep 0.02; done; printf '%%s' %q", ready, release, strings.Repeat("alpha ", 400))
	f.message(1, 42, 7, command)
	eventually(t, "first tool waiting", func() (bool, any) {
		_, err := os.Stat(ready)
		return err == nil, err
	})
	f.nextEvent(t, "sendChatAction")

	// When a second message arrives before the first answer starts.
	f.message(2, 42, 7, "printf second")
	f.nextEvent(t, "sendChatAction")
	d.watch(t, d.mainChat(t)).until(t, "second message queued", func(tr *transcript) bool {
		for _, item := range tr.items {
			if item.Kind == "user" && item.Text == "printf second" && item.Queued {
				return true
			}
		}
		return false
	})
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	// Then the first final answer replaces the preview that showed its text.
	preview := f.next(t)
	if preview.ParseMode != "" || !strings.HasPrefix(answer, preview.Text) {
		t.Fatalf("first answer has no plain preview: mode=%q text=%q", preview.ParseMode, preview.Text)
	}
	created := f.nextEvent(t, "sendMessage")
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event := <-f.events:
			if event.Text != answer {
				continue
			}
			if event.Method != "editMessageText" || event.MessageID != created.MessageID {
				t.Fatalf("first final answer used %s id=%d; preview id=%d showed the same answer", event.Method, event.MessageID, created.MessageID)
			}
			return
		case <-deadline.C:
			t.Fatal("first final answer did not arrive")
		}
	}
}

func TestUserGetsTelegramAnswerDuringSlowInboundDownload(t *testing.T) {
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

func TestUserCanStopTelegramDuringPreviewRequest(t *testing.T) {
	// Given a preview request that the Bot API has not completed.
	f := newFakeTelegram(t)
	gate := make(chan struct{})
	f.draftGate = gate
	defer close(gate)
	d := startTelegram(t, f, t.TempDir(), 42)
	f.message(1, 42, 7, strings.Repeat("stream ", 400))
	f.nextEvent(t, "draftStarted")
	// When the user stops dex.
	stopped := make(chan struct{})
	go func() { d.stop(); close(stopped) }()
	// Then shutdown cancels the request and joins the preview.
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown did not cancel the preview request")
	}
}
