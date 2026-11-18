package app_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUserQueuedTelegramMessageDoesNotTakeFirstPreview(t *testing.T) {
	t.Parallel()
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
	if preview.RichHTML == "" || !strings.HasPrefix(answer, preview.Text) {
		t.Fatalf("first answer has no rich preview: rich=%q text=%q", preview.RichHTML, preview.Text)
	}
	created := f.nextEvent(t, "sendRichMessage")
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

func TestUserCanStopTelegramDuringPreviewRequest(t *testing.T) {
	t.Parallel()
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

func TestUserUnknownRichFinalEditKeepsItsPreviewAndMethod(t *testing.T) {
	t.Parallel()
	// Given a rich preview created while the provider holds its finish event.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	finish := make(chan struct{})
	release := sync.OnceFunc(func() { close(finish) })
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"rich\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"rich\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"**final**\"},\"finish_reason\":null}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-finish:
		case <-r.Context().Done():
			return
		}
		_, _ = fmt.Fprint(w, "data: {\"id\":\"rich\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"rich\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(endpoint.Close)
	t.Cleanup(release)
	d.must(t, http.MethodPut, "/api/settings/providers/rich-preview", map[string]any{"api": "openai-completions", "baseUrl": endpoint.URL, "models": []string{"rich"}}, nil)
	d.must(t, http.MethodPut, "/api/agents/default", map[string]string{"name": "Dex", "provider": "rich-preview", "modelId": "rich", "thinkingLevel": "off"}, nil)
	d.must(t, http.MethodPut, "/api/agents/default/channels/telegram", map[string]any{"token": "", "allowedUsers": []int64{42}, "options": map[string]any{"preview": "edit"}}, nil)
	f.message(1, 42, 42, "make a rich answer")
	preview := f.next(t)
	if preview.RichHTML != "<p><b>final</b></p>" {
		t.Fatalf("preview=%+v", preview)
	}
	// When the final edit has an incomplete success response, it must remain unconfirmed.
	f.mu.Lock()
	f.failures["editMessageText"] = []platformFailure{{ShortResult: true}}
	f.mu.Unlock()
	release()
	conn := controlWriteDB(t, d.dataDir(t))
	eventually(t, "incomplete final edit is retried, not confirmed", func() (bool, any) {
		f.mu.Lock()
		attempts := 0
		for _, request := range f.requests {
			if request.Method == "editMessageText" {
				attempts++
			}
		}
		f.mu.Unlock()
		confirmed := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:1' AND delivered_parts>0")
		if attempts == 1 && confirmed > 0 {
			t.Fatal("incomplete final edit was confirmed")
		}
		retrying := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:1' AND channel_retry_at>0")
		return retrying > 0 || attempts >= 2, []int{attempts, confirmed, retrying}
	})
	final := f.next(t)
	if final.RichHTML != preview.RichHTML {
		t.Fatalf("final changed: %+v", final)
	}
	// Then retry edits the same ID, not a new send or an ordinary HTML fallback.
	f.mu.Lock()
	defer f.mu.Unlock()
	sends, edits := 0, 0
	for _, request := range f.requests {
		switch request.Method {
		case "sendRichMessage":
			sends++
		case "editMessageText":
			edits++
			if request.MessageID != preview.ID || request.RichHTML != preview.RichHTML {
				t.Fatalf("edit changed target/body: %+v", request)
			}
		default:
			t.Fatalf("unexpected preview/final method: %+v", request)
		}
	}
	if sends != 1 || edits != 2 {
		t.Fatalf("sends=%d edits=%d", sends, edits)
	}
}
