package app_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestUserTelegramPendingAttachmentReplayKeepsOneUpload(t *testing.T) {
	t.Parallel()
	// Given a stored attachment whose native admission is held across restart.
	f := newFakeTelegram(t)
	f.files["notes.txt"] = "remember the milk\n"
	data := t.TempDir()
	d := startTelegram(t, f, data, 42)
	d.setModel(t, "bash")
	var hold atomic.Bool
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hold.Load() {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"unused"}]}`))
	}))
	t.Cleanup(endpoint.Close)
	t.Cleanup(unblock)
	d.must(t, http.MethodPut, "/api/settings/providers/receipt-gate", map[string]any{"api": "openai-completions", "baseUrl": endpoint.URL}, nil)
	d.must(t, http.MethodPost, "/api/maintenance", nil, nil)
	marker := filepath.Join(data, "executions.txt")
	command := fmt.Sprintf("printf 'once\\n' >> '%s'; printf released; exit 0", marker)
	f.mu.Lock()
	f.updates = append(f.updates, map[string]any{"update_id": int64(1), "message": map[string]any{
		"from": map[string]any{"id": 42}, "chat": map[string]any{"id": 7, "type": "private"}, "caption": command,
		"document": map[string]any{"file_id": "notes.txt", "file_name": "notes.txt"},
	}})
	f.mu.Unlock()
	conn := sessionDB(t, d)
	pending := func() (bool, any) {
		var status string
		var admitted any
		err := conn.QueryRowContext(t.Context(), "SELECT status, admitted_at FROM submissions WHERE source_key IS NOT NULL").Scan(&status, &admitted)
		return err == nil && status == "pending" && admitted == nil, fmt.Sprintf("status=%s admitted=%v err=%v", status, admitted, err)
	}
	eventually(t, "stored unadmitted attachment", pending)
	f.nextEvent(t, "getFile")
	t.Log("artifact: before restart receipt=pending admitted_at=NULL uploads=1 downloads=1")
	d.stop()
	hold.Store(true)
	d = startDex(t, data, f.srv.URL)
	// When Telegram replays the same update before discovery releases admission.
	// A later command acknowledgement proves the repeated update has been processed.
	f.message(2, 42, 7, "/help")
	if got := f.next(t); !strings.Contains(got.Text, "/help") {
		t.Fatalf("replay barrier reply = %q", got.Text)
	}
	eventually(t, "receipt still awaits native admission", pending)
	entries, err := os.ReadDir(filepath.Join(data, "agents", "default", "workspace", "uploads"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("pending attachment replay stored %d uploads; want 1: %v", len(entries), err)
	}
	downloads := 1
	for len(f.events) > 0 {
		if ev := <-f.events; ev.Method == "getFile" {
			downloads++
		}
	}
	if downloads != 1 {
		t.Fatalf("pending attachment replay downloaded %d files; want 1", downloads)
	}
	var receipts int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE source_key IS NOT NULL").Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("source receipts=%d err=%v", receipts, err)
	}
	// Then release executes the stored intent once and retains its original attachment.
	unblock()
	if got := f.next(t); got.Text != "bash: released" {
		t.Fatalf("released answer = %q", got.Text)
	}
	raw, err := os.ReadFile(marker)
	if err != nil || string(raw) != "once\n" {
		t.Fatalf("native executions=%q err=%v", raw, err)
	}
	d.watch(t, d.mainChat(t)).until(t, "one native input", func(tr *transcript) bool {
		users := 0
		for _, item := range tr.items {
			if item.Kind == "user" {
				users++
				if len(item.Attachments) != 1 {
					t.Fatalf("native input attachments=%v", item.Attachments)
				}
			}
		}
		return users == 1
	})
	t.Logf("artifact: after replay uploads=%d downloads=%d receipts=%d; release native executions=1", len(entries), downloads, receipts)
}

func TestUserMovesTelegramBotWithoutReplayingOldInput(t *testing.T) {
	t.Parallel()
	// Given an attachment answered under the default agent and a second agent.
	f := newFakeTelegram(t)
	f.files["old.txt"] = "default attachment\n"
	data := t.TempDir()
	d := startTelegram(t, f, data, 42)
	f.mu.Lock()
	f.updates = append(f.updates, map[string]any{"update_id": int64(1), "message": map[string]any{
		"from": map[string]any{"id": 42}, "chat": map[string]any{"id": 7, "type": "private"}, "caption": "old input",
		"document": map[string]any{"file_id": "old.txt", "file_name": "old.txt"},
	}})
	f.mu.Unlock()
	if got := f.next(t); !strings.HasPrefix(got.Text, "echo: old input") {
		t.Fatalf("default answer=%q", got.Text)
	}
	oldChat := d.mainChat(t)
	oldWatch := d.watch(t, oldChat)
	oldWatch.until(t, "default answer before bot transfer", func(tr *transcript) bool {
		for _, item := range tr.items {
			if item.Kind == "assistant" && strings.HasPrefix(item.Text, "echo: old input") {
				return true
			}
		}
		return false
	})
	conn := sessionDB(t, d)
	var oldID, oldAttachments string
	if err := conn.QueryRowContext(t.Context(), "SELECT id,attachments FROM submissions WHERE source_key='telegram:123:1'").Scan(&oldID, &oldAttachments); err != nil {
		t.Fatal(err)
	}
	var beta apiAgent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "Beta", "provider": "faux", "modelId": "shout", "thinkingLevel": "off"}, &beta, http.StatusCreated)
	// When the saved credential moves, the new bot starts at offset zero.
	d.must(t, http.MethodDelete, "/api/agents/default/channels/telegram", nil, nil)
	base := "/api/agents/" + beta.ID
	d.must(t, http.MethodPut, base+"/channels/telegram", map[string]any{"token": fakeToken, "allowedUsers": []int64{42}}, nil)
	f.message(2, 42, 7, "new beta input")
	// Then the old global source is skipped and the next input reaches Beta.
	if got := f.next(t); got.Text != "shout: new beta input" {
		t.Fatalf("bot transfer answer=%q; want Beta's new input", got.Text)
	}
	var betaMain chatInfo
	d.must(t, http.MethodGet, base+"/chats/main", nil, &betaMain)
	betaWatch := d.watchPath(t, fmt.Sprintf("%s/chats/%d/events", base, betaMain.ID))
	betaWatch.until(t, "Beta answers only new input", answered("shout: new beta input"))
	// Read default again after Beta finishes so the assertion covers post-transfer history.
	oldWatch = d.watch(t, oldChat)
	oldWatch.until(t, "default history after bot transfer", func(tr *transcript) bool {
		for _, item := range tr.items {
			if item.Kind == "assistant" && strings.HasPrefix(item.Text, "echo: old input") {
				return true
			}
		}
		return false
	})
	for _, tr := range []*transcript{&oldWatch.tr, &betaWatch.tr} {
		users := 0
		for _, item := range tr.items {
			if item.Kind == "user" {
				users++
			}
		}
		if users != 1 {
			t.Fatalf("bot transfer native user inputs=%d; want 1 per agent", users)
		}
	}
	var receiptID, attachments, owner string
	if err := conn.QueryRowContext(t.Context(), "SELECT s.id,s.attachments,c.agent_id FROM submissions s JOIN conversations c ON c.id=s.conversation_id WHERE s.source_key='telegram:123:1'").Scan(&receiptID, &attachments, &owner); err != nil {
		t.Fatal(err)
	}
	if receiptID != oldID || attachments != oldAttachments || owner != "default" {
		t.Fatalf("old receipt changed: id=%s owner=%s attachments=%s", receiptID, owner, attachments)
	}
	var total int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions").Scan(&total); err != nil || total != 2 {
		t.Fatalf("transfer submissions=%d err=%v", total, err)
	}
	downloads := 0
	for len(f.events) > 0 {
		if ev := <-f.events; ev.Method == "getFile" {
			downloads++
		}
	}
	if downloads != 1 {
		t.Fatalf("bot transfer downloaded old attachment %d times; want 1", downloads)
	}
	entries, err := os.ReadDir(filepath.Join(data, "agents", "default", "workspace", "uploads"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("default uploads=%d err=%v", len(entries), err)
	}
	if entries, err := os.ReadDir(filepath.Join(data, "agents", beta.ID, "workspace", "uploads")); err == nil && len(entries) != 0 {
		t.Fatalf("Beta stored %d old attachments", len(entries))
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	t.Log("artifact: moved token skips old source; default receipt/upload unchanged; downloads=1; native user inputs=1 per agent; Beta answers only new input")
}
