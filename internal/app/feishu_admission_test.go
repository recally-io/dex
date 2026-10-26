package app_test

import (
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"uuid"
)

func TestUserFeishuDisconnectDoesNotStrandDurableAdmission(t *testing.T) {
	t.Parallel()
	// Given native admission paused for maintenance.
	f := newFakeFeishu(t)
	f.pingInterval = 1
	dataDir := t.TempDir()
	d := startFeishu(t, f, dataDir)
	pairFeishu(t, d, f)
	d.must(t, http.MethodPost, "/api/maintenance", nil, nil)
	conn, err := sql.Open("sqlite", filepath.Join(dataDir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	webChat := d.newSession(t)
	webID := uuid.NewV7().String()
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/messages", webChat.ID), map[string]string{"id": webID, "text": "web input during maintenance"}, nil, http.StatusConflict)
	var webReceipts int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE id=?", webID).Scan(&webReceipts); err != nil {
		t.Fatal(err)
	}
	if webReceipts != 0 {
		t.Fatal("maintenance rejection saved a web receipt")
	}
	state := func(source string) string {
		var status string
		err := conn.QueryRowContext(t.Context(), "SELECT status FROM submissions WHERE source_key = ?", source).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return "absent"
		}
		if err != nil {
			t.Fatal(err)
		}
		return status
	}
	body, err := json.Marshal(map[string]any{
		"header": map[string]string{"event_type": "im.message.receive_v1"},
		"event": map[string]any{
			"sender":  map[string]any{"sender_type": "user", "sender_id": map[string]string{"open_id": "owner"}},
			"message": map[string]string{"message_id": "cancel-window", "chat_type": "p2p", "message_type": "text", "content": `{"text":"stored before disconnect"}`},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.events <- body
	eventually(t, "Feishu receipt stored while native admission is paused", func() (bool, any) {
		s := state("feishu:app:cancel-window")
		return s == "pending", s
	})
	// When the input socket closes after the database commit but before native admission.
	f.mu.Lock()
	socket, before := f.conn, f.connections
	f.mu.Unlock()
	if err := socket.CloseNow(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "Feishu reconnect after a durable receipt", func() (bool, any) { f.mu.Lock(); defer f.mu.Unlock(); return f.connections > before, f.connections })
	var admitted sql.NullInt64
	if err := conn.QueryRowContext(t.Context(), "SELECT admitted_at FROM submissions WHERE source_key=?", "feishu:app:cancel-window").Scan(&admitted); err != nil {
		t.Fatal(err)
	}
	if admitted.Valid {
		t.Fatal("maintenance receipt claims native admission")
	}
	// Repeat the platform event while the original receipt still lacks admission.
	f.text(t, "cancel-window", "owner", "p2p", "stored before disconnect")
	d.must(t, http.MethodDelete, "/api/maintenance", nil, nil)
	f.text(t, "after-window", "owner", "p2p", "after maintenance")
	f.waitReply(t, "echo: after maintenance")
	// The platform repeats the original event after reconnect.
	f.text(t, "cancel-window", "owner", "p2p", "stored before disconnect")
	f.text(t, "after-redelivery", "owner", "p2p", "after platform retry")
	f.waitReply(t, "echo: after platform retry")
	// Then an already stored input must reach native execution without restarting dex.
	got := state("feishu:app:cancel-window")
	t.Logf("original=%s later=%s after-retry=%s", got, state("feishu:app:after-window"), state("feishu:app:after-redelivery"))
	var count int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE source_key=?", "feishu:app:cancel-window").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("platform retry created %d receipts", count)
	}
	t.Logf("artifact: rejected web receipts=%d; repeated channel receipts=%d; original status=%s", webReceipts, count, got)
	if got != "done" {
		t.Fatalf("stored input was stranded after disconnect and platform retry: status=%s", got)
	}
}
