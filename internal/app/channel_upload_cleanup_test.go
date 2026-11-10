package app_test

import (
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cleanupDB(t *testing.T, dir, source string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "control.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(t.Context(), fmt.Sprintf(`CREATE TRIGGER reject_upload BEFORE INSERT ON submissions WHEN NEW.source_key = '%s' BEGIN SELECT RAISE(ABORT, 'upload receipt failure'); END`, source))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func uploadCount(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "agents", "default", "workspace", "uploads"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func assertReceiptUpload(t *testing.T, db *sql.DB, dir, source string) {
	t.Helper()
	var raw string
	if err := db.QueryRowContext(t.Context(), "SELECT attachments FROM submissions WHERE source_key=?", source).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var attachments []struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(raw), &attachments); err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 {
		t.Fatalf("receipt attachments = %s", raw)
	}
	if _, err := os.Stat(filepath.Join(dir, "agents", "default", "workspace", attachments[0].Path)); err != nil {
		t.Fatalf("receipt upload missing: %v", err)
	}
}

func TestUserTelegramUploadRetryCleansUncommittedFile(t *testing.T) {
	// Given a receipt write that fails after the document download.
	f := newFakeTelegram(t)
	f.files["notes.txt"] = "notes"
	dir := t.TempDir()
	d := startTelegram(t, f, dir, 42)
	db := cleanupDB(t, dir, "telegram:123:51")
	f.mu.Lock()
	f.updates = append(f.updates, map[string]any{"update_id": int64(51), "message": map[string]any{
		"from": map[string]any{"id": 42}, "chat": map[string]any{"id": 7, "type": "private"}, "caption": "read",
		"document": map[string]any{"file_id": "notes.txt", "file_name": "notes.txt"},
	}})
	f.mu.Unlock()
	// When the failed attempt ends and the platform retries it.
	waitTelegramState(t, d, "error")
	var status struct {
		Telegram struct {
			Status struct {
				Reason string `json:"reason"`
			} `json:"status"`
		} `json:"telegram"`
	}
	d.must(t, http.MethodGet, "/api/agents/default/channels", nil, &status)
	if !strings.Contains(status.Telegram.Status.Reason, "upload receipt failure") {
		t.Fatalf("retry lost receipt failure: %q", status.Telegram.Status.Reason)
	}
	if n := uploadCount(t, dir); n != 0 {
		t.Errorf("failed Telegram attempt left %d uploads", n)
	}
	if _, err := db.ExecContext(t.Context(), "DROP TRIGGER reject_upload"); err != nil {
		t.Fatal(err)
	}
	f.next(t)
	// Then one receipt owns one surviving upload.
	var n int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE source_key='telegram:123:51'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 || uploadCount(t, dir) != 1 {
		t.Fatalf("receipts=%d uploads=%d", n, uploadCount(t, dir))
	}
	assertReceiptUpload(t, db, dir, "telegram:123:51")
}

func TestUserFeishuUploadRetryCleansUncommittedFile(t *testing.T) {
	// Given a receipt write that fails after the file download.
	f := newFakeFeishu(t)
	dir := t.TempDir()
	d := startFeishu(t, f, dir)
	pairFeishu(t, d, f)
	db := cleanupDB(t, dir, "feishu:app:retry-upload")
	body, err := json.Marshal(map[string]any{"header": map[string]string{"event_type": "im.message.receive_v1"}, "event": map[string]any{
		"sender":  map[string]any{"sender_type": "user", "sender_id": map[string]string{"open_id": "owner"}},
		"message": map[string]string{"message_id": "retry-upload", "chat_type": "p2p", "message_type": "file", "content": `{"file_key":"file1","file_name":"notes.txt"}`},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// When a later event confirms the failed attempt has finished.
	f.events <- body
	f.text(t, "after-failure", "owner", "p2p", "after failure")
	if n := uploadCount(t, dir); n != 0 {
		t.Errorf("failed Feishu attempt left %d uploads", n)
	}
	if _, err := db.ExecContext(t.Context(), "DROP TRIGGER reject_upload"); err != nil {
		t.Fatal(err)
	}
	f.events <- body
	f.text(t, "after-retry", "owner", "p2p", "after retry")
	// Then one receipt owns one surviving upload.
	var n int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE source_key='feishu:app:retry-upload'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 || uploadCount(t, dir) != 1 {
		t.Fatalf("receipts=%d uploads=%d", n, uploadCount(t, dir))
	}
	assertReceiptUpload(t, db, dir, "feishu:app:retry-upload")
}

func TestUserFeishuRejectedLaterResourceCleansEarlierUpload(t *testing.T) {
	// Given a post whose second image is permanently unavailable.
	f := newFakeFeishu(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/resources/missing") {
			http.Error(w, "missing", http.StatusNotFound)
			return
		}
		f.serve(w, r)
	}))
	t.Cleanup(server.Close)
	f.server = server
	dir := t.TempDir()
	d := startFeishu(t, f, dir)
	pairFeishu(t, d, f)
	// When Feishu delivers the post.
	f.message(t, "partial", "owner", "p2p", "post", `{"content":[[{"tag":"img","image_key":"first"},{"tag":"img","image_key":"missing"}]]}`)
	// Then the rejected post leaves no upload.
	if n := uploadCount(t, dir); n != 0 {
		t.Fatalf("rejected Feishu post left %d uploads", n)
	}
}
