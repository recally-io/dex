package db_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/recally-io/dex/internal/db"
)

func TestUserKeepsPrimarySessionWhenBoardSupportsMultipleSessions(t *testing.T) {
	// Given an existing Task that links a child of another Bot.
	path := filepath.Join(t.TempDir(), "control.sqlite")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = old.Close() })
	provider, err := goose.NewProvider(goose.DialectSQLite3, old, migrationFS(t, func(name string) bool {
		return name <= "20261125080000_simplified_single_owner_architecture.sql"
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	exec := func(conn *sql.DB, stmt string) {
		t.Helper()
		if _, err := conn.ExecContext(t.Context(), stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	exec(old, `INSERT INTO agents (id, name, model_provider, model_id, created_at) VALUES ('owner', 'Owner', 'faux', 'echo', 1), ('coder', 'Coder', 'faux', 'echo', 1)`)
	exec(old, `INSERT INTO conversations (id, agent_id, channel_key, created_at) VALUES (1, 'coder', 'chat:coder', 10), (2, 'owner', 'chat:owner', 20)`)
	exec(old, `INSERT INTO work_tasks (id, agent_id, title, created_at, updated_at) VALUES ('task', 'owner', 'Ship login', 2, 30)`)
	exec(old, `INSERT INTO work_task_sessions (task_id, conversation_id) VALUES ('task', 1)`)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	// When dex upgrades the database, the old session remains primary without changing the Owner Bot.
	conn, err := db.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	var owner, bot string
	var primary, linkedAt int64
	err = conn.QueryRowContext(t.Context(), `SELECT t.agent_id, s.agent_id, s.is_primary, s.linked_at FROM work_tasks t JOIN work_task_sessions s ON s.task_id = t.id WHERE s.conversation_id = 1`).Scan(&owner, &bot, &primary, &linkedAt)
	if err != nil || owner != "owner" || bot != "coder" || primary != 1 || linkedAt != 10 {
		t.Fatalf("upgraded link: owner=%q bot=%q primary=%d linkedAt=%d err=%v", owner, bot, primary, linkedAt, err)
	}

	// Then a second Bot can link a session, and the database forbids two primary links.
	exec(conn, `INSERT INTO work_task_sessions (task_id, conversation_id, agent_id, linked_at) VALUES ('task', 2, 'owner', 40)`)
	if _, err := conn.ExecContext(t.Context(), `UPDATE work_task_sessions SET is_primary = 1 WHERE task_id = 'task' AND conversation_id = 2`); err == nil {
		t.Fatal("a Task accepted two primary sessions")
	}
}
