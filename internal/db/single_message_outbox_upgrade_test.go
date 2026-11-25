package db_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/recally-io/dex/internal/db"
)

func TestUserUpgradeDoesNotReplayPartiallySentOutbox(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "control.sqlite")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = old.Close() })
	provider, err := goose.NewProvider(goose.DialectSQLite3, old, migrationFS(t, func(name string) bool { return name < "20261125060045_single_message_outbox.sql" }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	// Given pending legacy messages, including a multipart reply with one receipt.
	if _, err := old.ExecContext(ctx, `
INSERT INTO agents (id, name, model_provider, model_id, created_at) VALUES ('fixture', 'Fixture', '', '', 1);
INSERT INTO conversations (id, agent_id, channel_key, created_at) VALUES (101, 'fixture', 'main', 1);
INSERT INTO telegram_settings (agent_id, token, allowed_users, options) VALUES ('fixture', '42:fixture', '7', '{"preview":"off"}');
INSERT INTO outbox_messages (id, dedup_key, agent_id, conversation_id, kind, peer, channel_context,
    created_at, binding_id, rendered_parts, delivered_parts, remote_ids)
SELECT key, key, 'fixture', 101, 'proactive', 'telegram:7', '{"accountId":"42","senderId":"7"}', 1,
    channel_bindings.id, parts, delivered, remote
FROM channel_bindings CROSS JOIN (
    SELECT 'multi' AS key, '[{"text":"first"},{"text":"second"}]' AS parts, 1 AS delivered, '[{"id":"1"}]' AS remote
    UNION ALL SELECT 'single', '[{"text":"one"}]', 0, '[]'
    UNION ALL SELECT 'confirmed', '[{"text":"one"}]', 1, '[{"id":"2"}]'
)
WHERE agent_id = 'fixture';`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	// When Dex upgrades, then it keeps the receipt and only retries the safe singleton.
	upgraded, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	for id, want := range map[string]string{"multi": `failed:[{"id":"1"}]`, "single": "pending:[]", "confirmed": `sent:[{"id":"2"}]`} {
		var got string
		if err := upgraded.QueryRowContext(ctx, `SELECT status || ':' || remote_ids FROM outbox_messages WHERE id = ?`, id).Scan(&got); err != nil || got != want {
			t.Fatalf("%s = %s, want %s: %v", id, got, want, err)
		}
	}
	var bindingTables, typing int
	if err := upgraded.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name = 'channel_bindings'`).Scan(&bindingTables); err != nil || bindingTables != 0 {
		t.Fatalf("binding tables = %d: %v", bindingTables, err)
	}
	if err := upgraded.QueryRowContext(ctx, `SELECT options ->> 'typing' FROM telegram_settings WHERE agent_id = 'fixture'`).Scan(&typing); err != nil || typing != 0 {
		t.Fatalf("typing = %d: %v", typing, err)
	}
}
