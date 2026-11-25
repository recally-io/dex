package db_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserRetiredTelegramGroupsStaySealedAfterUpgrade(t *testing.T) {
	// Given an old database with the same native ID in Main and a group store.
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "control.sqlite")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = old.Close() })
	migration := "20261125080000_simplified_single_owner_architecture.sql"
	provider, err := goose.NewProvider(goose.DialectSQLite3, old, migrationFS(t, func(name string) bool { return name < migration }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = old.ExecContext(ctx, `
INSERT INTO agents (id, name, model_provider, model_id, created_at) VALUES ('default', 'Default', '', '', 1) ON CONFLICT DO NOTHING;
INSERT INTO conversations (id, agent_id, channel_key, execution_id, execution_scope, parent_id, created_at) VALUES
 (101, 'default', 'main', 1, '', NULL, 1),
 (102, 'default', 'telegram-topic:tg_1_-10_0', 1, 'tg_1_-10_0', NULL, 1),
 (103, 'default', 'chat:group-child', 2, 'tg_1_-10_0', 102, 1),
 (104, 'default', 'telegram:-11', 3, '', NULL, 1);
INSERT INTO submissions (id, conversation_id, content, origin, kind, status, created_at) VALUES
 ('private', 101, 'private input', 'telegram:42', 'message', 'pending', 1),
 ('public', 102, 'public input', 'telegram:-10', 'message', 'pending', 1),
 ('child', 103, 'child input', 'task', 'message', 'pending', 1),
 ('legacy', 104, 'legacy input', 'unknown', 'message', 'pending', 1);
INSERT INTO outbox_messages (id, dedup_key, agent_id, conversation_id, source_submission_id, kind, peer, created_at) VALUES
 ('private', 'private', 'default', 101, 'private', 'reply', 'telegram:42', 1),
 ('public', 'public', 'default', 102, 'public', 'reply', 'telegram:-10', 1),
 ('child', 'child', 'default', 103, 'child', 'proactive', 'telegram:42', 1),
 ('negative-peer', 'negative-peer', 'default', 101, NULL, 'proactive', 'telegram:-10:4', 1);
INSERT INTO channel_commands (source_key, agent_id, peer, channel_key, command) VALUES
 ('group-clear', 'default', 'telegram:-10:4', 'telegram-topic:tg_1_-10_0', '/clear');
INSERT INTO telegram_album_members (source_key, agent_id, account_id, sender_id, peer, group_key, input_json, received_at, batch_source, state) VALUES
 ('group-file', 'default', '1', '42', 'telegram:-10:4', 'group-album', '{}', 1, 'group-batch', 'sealed');
`)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	// When startup upgrades the database.
	upgraded, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	q := sqlc.New(upgraded)

	// Then workers see private work only, and private lookup cannot reach group data.
	pending, err := q.ListPendingSubmissions(ctx, 0)
	if err != nil || len(pending) != 1 || pending[0].ID != "private" {
		t.Fatalf("pending inputs = %v, err = %v", pending, err)
	}
	for _, id := range []string{"public", "child", "legacy"} {
		var status, content string
		err := upgraded.QueryRowContext(ctx, `SELECT status, content FROM submissions WHERE id = ?`, id).Scan(&status, &content)
		if err != nil || status != "unanswered" || content != id+" input" {
			t.Fatalf("sealed input %s = %q, %q, err = %v", id, status, content, err)
		}
	}
	outgoing, err := q.ListPendingOutboxMessages(ctx, sqlc.ListPendingOutboxMessagesParams{AgentID: "default", Channel: sql.NullString{String: "telegram", Valid: true}})
	if err != nil || len(outgoing) != 0 {
		t.Fatalf("pending Telegram deliveries = %v, err = %v", outgoing, err)
	}
	for id, want := range map[string]string{"private": "failed", "public": "failed", "child": "failed", "negative-peer": "failed"} {
		var status string
		err := upgraded.QueryRowContext(ctx, `SELECT status FROM outbox_messages WHERE id = ?`, id).Scan(&status)
		if err != nil || status != want {
			t.Fatalf("outbox %s = %q, want %q, err = %v", id, status, want, err)
		}
	}
	commands, err := q.ListChannelCommands(ctx, "default")
	if err != nil || len(commands) != 0 {
		t.Fatalf("pending commands = %v, err = %v", commands, err)
	}
	sealed, err := q.ListSealedTelegramAlbums(ctx, "default")
	if err != nil || len(sealed) != 0 {
		t.Fatalf("pending album batches = %v, err = %v", sealed, err)
	}
	for _, id := range []int64{102, 103, 104} {
		if _, err := q.GetConversation(ctx, id); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("group chat %d is visible: %v", id, err)
		}
	}
	aborts, err := q.ListPendingExecutionAborts(ctx)
	if err != nil || len(aborts) != 1 || aborts[0].ID != 104 || aborts[0].ExecutionID.Int64 != 3 {
		t.Fatalf("startup must isolate only the legacy group in the shared store: %v, err = %v", aborts, err)
	}
	if _, err := q.GetConversationByExecution(ctx, sqlc.GetConversationByExecutionParams{AgentID: "default", ExecutionID: sql.NullInt64{Int64: 2, Valid: true}}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("group native ID is visible in the private store: %v", err)
	}
	main, err := q.GetConversationByExecution(ctx, sqlc.GetConversationByExecutionParams{AgentID: "default", ExecutionID: sql.NullInt64{Int64: 1, Valid: true}})
	if err != nil || main.ID != 101 || main.ExecutionID.Int64 != 1 {
		t.Fatalf("Main identity changed: %+v, err = %v", main, err)
	}
}
