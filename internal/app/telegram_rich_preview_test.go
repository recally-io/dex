package app_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

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
