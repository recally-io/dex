package db_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/recally-io/dex/internal/db"
)

func TestUserDoesNotReceiveLegacyOutboxAtMatchingPeer(t *testing.T) {
	// Given a legacy delivery that matches the current Telegram bot and peer.
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "control.sqlite")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = old.Close() })
	provider, err := goose.NewProvider(goose.DialectSQLite3, old, migrationFS(t, func(name string) bool { return name < "20261125031400_channel_bindings.sql" }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := old.ExecContext(ctx, `
INSERT INTO agents (id, name, model_provider, model_id, created_at) VALUES ('fixture', 'Fixture', '', '', 1);
INSERT INTO conversations (id, agent_id, channel_key, created_at) VALUES (101, 'fixture', 'main', 1);
INSERT INTO telegram_settings (agent_id, token, allowed_users) VALUES ('fixture', '42:fixture', '7');
INSERT INTO outbox_messages (id, dedup_key, agent_id, conversation_id, kind, peer, channel_context, created_at)
VALUES ('old', 'old', 'fixture', 101, 'proactive', 'telegram:7', '{"accountId":"42","senderId":"7"}', 1);
INSERT INTO channel_webhook_inbox (agent_id, platform, account, event_id, payload, received_at)
VALUES ('fixture', 'telegram', '42', 'evt-1', CAST('{"update_id":1}' AS BLOB), 1);
`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	// When the owner starts the upgraded server, matching addresses do not prove continuity.
	upgraded, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	var status string
	if err := upgraded.QueryRowContext(ctx, `SELECT status FROM outbox_messages WHERE id = 'old'`).Scan(&status); err != nil || status != "failed" {
		t.Fatalf("legacy delivery status = %q, error = %v; want failed", status, err)
	}
	var payload []byte
	if err := upgraded.QueryRowContext(ctx, `SELECT payload FROM channel_webhook_inbox WHERE event_id = 'evt-1'`).Scan(&payload); err != nil || string(payload) != `{"update_id":1}` {
		t.Fatalf("pending webhook payload = %q, error = %v; want preserved payload", payload, err)
	}
}
