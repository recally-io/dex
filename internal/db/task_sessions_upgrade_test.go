package db_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/recally-io/dex/internal/db"
	sqlite3 "modernc.org/sqlite/lib"
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
	assertTaskBoardRows(t, conn, `SELECT json_array(t.assignee_bot_id, e.bot_id, e.is_primary, e.started_at, e.kind)
		FROM tasks t JOIN task_executions e ON e.task_id = t.id WHERE e.conversation_id = 1`,
		`["owner","coder",1,10,"dex_session"]`)

	// Then a second Bot can link a session, and the database forbids two primary links.
	exec(conn, `INSERT INTO task_executions (id, task_id, conversation_id, bot_id, kind, started_at)
		VALUES ('session:task:2', 'task', 2, 'owner', 'dex_session', 40)`)
	_, err = conn.ExecContext(t.Context(), `UPDATE task_executions SET is_primary = 1 WHERE task_id = 'task' AND conversation_id = 2`)
	assertTaskBoardSQLiteError(t, err, sqlite3.SQLITE_CONSTRAINT_UNIQUE)
}
