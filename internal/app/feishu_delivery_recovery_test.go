package app_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestUserFeishuPostAcceptLostResponseReusesSameUUID(t *testing.T) {
	t.Parallel()
	// Given a Feishu channel where the first final send is accepted remotely but its response is lost.
	dir := t.TempDir()
	f := newFakeFeishu(t)
	d := startFeishu(t, f, dir)
	pairFeishu(t, d, f)
	conn := controlWriteDB(t, dir)

	f.mu.Lock()
	f.uuids = nil
	f.lostAccepts = 1
	f.mu.Unlock()

	// The final summary must use the same UUID on every attempt.
	long := strings.Repeat("lost-resp ", 500) + "TAIL_MARKER"
	f.text(t, "lost-resp-1", "owner", "p2p", long)
	eventually(t, "feishu outbox delivered after lost response retry", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM outbox_messages WHERE status='sent' AND peer='feishu:owner' AND kind='reply'")
		return n == 1, n
	})

	// Then the retried POST reused the exact same delivery UUID so Feishu can deduplicate within its window.
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.uuids) < 2 {
		t.Fatalf("expected at least 2 POST attempts with UUIDs, got %v", f.uuids)
	}
	seen := map[string]int{}
	reused := false
	for _, u := range f.uuids {
		seen[u]++
		if seen[u] >= 2 {
			reused = true
		}
	}
	if !reused {
		t.Fatalf("retry did not reuse delivery UUID: all=%v", f.uuids)
	}
}

func TestUserFeishuCardFallbackTransientFailureReusesTextUUID(t *testing.T) {
	t.Parallel()
	// Given a Feishu channel that rejects interactive cards (230001) and fails the first text fallback transiently.
	f := newFakeFeishu(t)
	d := startFeishu(t, f, t.TempDir())
	pairFeishu(t, d, f)

	f.mu.Lock()
	f.rejectCards = true
	f.textFailures = 1
	f.uuids = nil
	f.mu.Unlock()

	f.text(t, "fallback-retry-1", "owner", "p2p", "card fallback retry")
	f.waitReply(t, "echo: card fallback retry")

	// Then the retried text fallback used the exact same :text UUID.
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.textAttempts < 2 {
		t.Fatalf("expected >= 2 text fallback attempts, got %d", f.textAttempts)
	}
	if len(f.uuids) < 2 {
		t.Fatalf("expected UUIDs on retries, got %v", f.uuids)
	}
	// The last two UUIDs are both for the text fallback (client.execute retries the 503 text attempt directly).
	if f.uuids[len(f.uuids)-1] != f.uuids[len(f.uuids)-2] {
		t.Fatalf("text fallback retry changed UUID: %v", f.uuids)
	}
}

func TestUserFeishuCredentialRotationRevokesPendingOutbox(t *testing.T) {
	t.Parallel()
	// Given a pending Feishu outbox reply blocked by platform errors.
	dir := t.TempDir()
	f := newFakeFeishu(t)
	d := startFeishu(t, f, dir)
	pairFeishu(t, d, f)
	conn := controlWriteDB(t, dir)

	f.mu.Lock()
	f.fail = true
	f.mu.Unlock()

	f.text(t, "revoke-pending-1", "owner", "p2p", "secret before rotation")
	eventually(t, "pending feishu outbox row", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM outbox_messages WHERE status='pending' AND peer='feishu:owner'")
		return n == 1, n
	})

	// When Feishu credentials are deleted (or rotated to a new app), the pending outbox row is revoked atomically.
	d.must(t, http.MethodDelete, "/api/bots/default/channels/feishu", nil, nil)
	eventually(t, "revoked feishu outbox row", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM outbox_messages WHERE status='failed' AND peer='feishu:owner'")
		return n == 1, n
	})

	// And even after re-enabling sends, the revoked message is never delivered.
	f.mu.Lock()
	f.fail = false
	msgs := append([]string(nil), f.messages...)
	f.mu.Unlock()
	for _, msg := range msgs {
		if strings.Contains(msg, "secret before rotation") {
			t.Fatalf("revoked message was sent: %q", msg)
		}
	}
}
