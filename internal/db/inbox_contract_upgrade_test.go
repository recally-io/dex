package db_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/recally-io/dex/internal/db"
)

func TestUserLegacySubmissionsBackfillProducerAndReplyAddressOnUpgrade(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "control.sqlite")

	// Given a database migrated up to the schema right before unify_inbox_contract.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	migration := "20261125080000_simplified_single_owner_architecture.sql"
	provider, err := goose.NewProvider(goose.DialectSQLite3, raw, migrationFS(t, func(name string) bool { return name < migration }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `
		INSERT INTO agents (id, name, model_provider, model_id, created_at) VALUES ('default', 'Default', '', '', 1) ON CONFLICT DO NOTHING;
		INSERT INTO conversations (id, agent_id, channel_key, title, created_at) VALUES (1, 'default', 'main', 'Main', 1);
		INSERT INTO conversations (id, agent_id, channel_key, title, parent_id, created_at) VALUES (2, 'default', 'chat:c1', 'Child', 1, 2);

		INSERT INTO submissions (id, conversation_id, content, attachments, origin, kind, status, created_at) VALUES
			('web:1', 1, 'hi web', '[]', 'web', 'message', 'done', 10),
			('tg:1:42:1', 1, 'hi tg', '[]', 'telegram:42', 'message', 'done', 11),
			('child-turn-1', 2, 'do work', 'null', 'task', 'message', 'done', 12),
			('result:child-turn-1', 1, 'child answer', 'null', 'telegram:42', 'task_result', 'done', 13),
			('monitor:m1:1', 1, 'check changed', 'null', 'telegram:42', 'task_result', 'done', 14),
			('background:b1:exit', 1, 'cmd exited', 'null', 'telegram:42', 'task_result', 'done', 15),
			('routine-run-1', 1, 'nightly', '[]', 'automation', 'message', 'done', 16),
			('dream-1', 1, 'dream', '[]', 'dream', 'message', 'done', 17),
			('task-alert:ev1', 1, 'pr review', 'null', 'web', 'task_result', 'done', 18);
	`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	// When startup opens and upgrades the database.
	upgraded, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })

	// Then each legacy row has its producer separated from direct-channel reply routing.
	want := map[string][3]string{
		"web:1":               {"user", "", ""},
		"tg:1:42:1":           {"user", "telegram", "42"},
		"child-turn-1":        {"sub_session", "", ""},
		"result:child-turn-1": {"sub_session", "", ""},
		"monitor:m1:1":        {"scheduler", "", ""},
		"background:b1:exit":  {"scheduler", "", ""},
		"routine-run-1":       {"scheduler", "", ""},
		"dream-1":             {"scheduler", "", ""},
		"task-alert:ev1":      {"plugin", "", ""},
	}
	for id, exp := range want {
		var producer, replyChannel, replyPeer string
		if err := upgraded.QueryRowContext(ctx, `SELECT producer, reply_channel, reply_peer FROM submissions WHERE id = ?`, id).Scan(&producer, &replyChannel, &replyPeer); err != nil {
			t.Fatalf("query %s: %v", id, err)
		}
		got := [3]string{producer, replyChannel, replyPeer}
		if got != exp {
			t.Fatalf("submission %s = %v, want %v", id, got, exp)
		}
	}
}
