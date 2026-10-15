package app_test

import (
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func TestUserFeishuDisconnectDoesNotStrandDurableAdmission(t *testing.T) {
	// Given a real worker in maintenance and a full control delivery queue.
	f := newFakeFeishu(t)
	f.pingInterval = 1
	dataDir := t.TempDir()
	d := startFeishu(t, f, dataDir)
	pairFeishu(t, d, f)
	d.must(t, http.MethodPost, "/api/maintenance", nil, nil)
	fillChat := d.newChat(t, "queued work")
	for i := range 1024 {
		d.send(t, fillChat, fmt.Sprintf("queued work %d", i))
	}
	conn, err := sql.Open("sqlite", filepath.Join(dataDir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
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
	eventually(t, "Feishu input stored before queue space is available", func() (bool, any) {
		s := state("feishu:app:cancel-window")
		return s == "pending", s
	})
	// When the input socket closes after the database commit but before queue admission.
	f.mu.Lock()
	socket, before := f.conn, f.connections
	f.mu.Unlock()
	if err := socket.CloseNow(); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(3 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer deadline.Stop()
	defer tick.Stop()
waitReconnect:
	for {
		f.mu.Lock()
		reconnected := f.connections > before
		f.mu.Unlock()
		if reconnected {
			break
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			break waitReconnect
		}
	}
	f.mu.Lock()
	t.Logf("connections before=%d after=%d", before, f.connections)
	f.mu.Unlock()
	// Stop the filler chat so the real worker can drain this queue without 1025 model runs.
	d.must(t, http.MethodPost, fmt.Sprintf("/api/chats/%d/abort", fillChat), nil, nil)
	d.must(t, http.MethodDelete, "/api/maintenance", nil, nil)
	f.text(t, "after-window", "owner", "p2p", "after queue drains")
	f.waitReply(t, "echo: after queue drains")
	// The platform repeats the original event after reconnect.
	f.text(t, "cancel-window", "owner", "p2p", "stored before disconnect")
	f.text(t, "after-redelivery", "owner", "p2p", "after platform retry")
	f.waitReply(t, "echo: after platform retry")
	// Then an already stored input must reach the worker without restarting either process.
	got := state("feishu:app:cancel-window")
	t.Logf("original=%s later=%s after-retry=%s", got, state("feishu:app:after-window"), state("feishu:app:after-redelivery"))
	if got != "done" {
		t.Fatalf("stored input was stranded after disconnect and platform retry: status=%s", got)
	}
}
