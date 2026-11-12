package db_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"
	"github.com/recally-io/dex/internal/db"
)

const (
	childSessionsMigration = "20261111180000_child_sessions.sql"
	dropTasksMigration     = "20261112120000_drop_tasks.sql"
)

func migrationFS(t *testing.T, keep func(name string) bool) fstest.MapFS {
	t.Helper()
	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sql") || !keep(entry.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join("migrations", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[entry.Name()] = &fstest.MapFile{Data: data}
	}
	return files
}

func readRows(t *testing.T, conn *sql.DB, query string, each func(scan func(...any) error) error) {
	t.Helper()
	rows, err := conn.QueryContext(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := each(rows.Scan); err != nil {
			t.Fatal(err)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestUserUpgradesTaskConversationsToChildSessions(t *testing.T) {
	// Given a database with the task schema: a parent chat, a finished task with its result, and a
	// message that the user wrote to the task conversation after it ended.
	path := filepath.Join(t.TempDir(), "control.sqlite")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = old.Close() })
	provider, err := goose.NewProvider(goose.DialectSQLite3, old, migrationFS(t, func(name string) bool { return name < childSessionsMigration }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO agents (id, name, model_provider, model_id, created_at) VALUES ('default', 'Dex', 'faux', 'echo', 1)`,
		`INSERT INTO conversations (id, agent_id, channel_key, created_at) VALUES (1, 'default', 'chat:parent', 1), (2, 'default', 'task:t1', 2)`,
		`INSERT INTO submissions (id, conversation_id, content, status, created_at) VALUES ('source', 1, 'please delegate', 'done', 1), ('t1', 2, 'goal', 'done', 2), ('user', 2, 'thanks', 'done', 3)`,
		`INSERT INTO tasks (id, parent_conversation_id, conversation_id, spawn_key, source_request_id, title, status, created_at) VALUES ('t1', 1, 2, 'default/7', 'source', 'Old task', 'done', 2)`,
		`INSERT INTO submissions (id, conversation_id, content, status, kind, source_key, created_at) VALUES ('task:t1:result', 1, '{}', 'done', 'task_result', 'task:t1:result', 4)`,
		`INSERT INTO task_steer_receipts (agent_id, parent_conversation_id, task_id, request_id, native_request_id, content, state, created_at) VALUES ('default', 1, 't1', 'steer', 'native-steer', 'more', 'pending', 5)`,
	} {
		if _, err := old.ExecContext(t.Context(), stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	// When dex opens the database.
	conn, err := db.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// Then the task conversation is a child of its parent, the task input is a parent turn, and
	// every other input stays as it was.
	parents := map[int64]sql.NullInt64{}
	readRows(t, conn, `SELECT id, parent_id FROM conversations`, func(scan func(...any) error) error {
		var id int64
		var parent sql.NullInt64
		if err := scan(&id, &parent); err != nil {
			return err
		}
		parents[id] = parent
		return nil
	})
	if parents[1].Valid || parents[2] != (sql.NullInt64{Int64: 1, Valid: true}) {
		t.Fatalf("parent_id: %+v", parents)
	}
	requests := map[string]sql.NullString{}
	readRows(t, conn, `SELECT id, parent_request_id FROM submissions`, func(scan func(...any) error) error {
		var id string
		var request sql.NullString
		if err := scan(&id, &request); err != nil {
			return err
		}
		requests[id] = request
		return nil
	})
	if requests["t1"] != (sql.NullString{String: "source", Valid: true}) || requests["user"].Valid || requests["source"].Valid || requests["task:t1:result"].Valid {
		t.Fatalf("parent_request_id: %+v", requests)
	}
	// And the task tables are gone, with the rows and the steer receipt that they held.
	var tables int
	if err := conn.QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_master WHERE name IN ('tasks', 'task_steer_receipts')`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("task tables left: %d, %v", tables, err)
	}
}

func TestChildSessionsMigrationRollsBackAndAppliesAgain(t *testing.T) {
	// Given a database at the newest schema.
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "control.sqlite")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	provider, err := goose.NewProvider(goose.DialectSQLite3, conn, migrationFS(t, func(name string) bool { return name <= childSessionsMigration }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	// When the migration rolls back and applies again.
	if _, err := provider.Down(t.Context()); err != nil {
		t.Fatalf("down: %v", err)
	}
	if _, err := conn.ExecContext(t.Context(), `SELECT parent_id FROM conversations`); err == nil {
		t.Fatal("down kept parent_id")
	}
	if _, err := provider.UpByOne(t.Context()); err != nil {
		t.Fatalf("up again: %v", err)
	}
	// Then the new columns exist again.
	if _, err := conn.ExecContext(t.Context(), `SELECT parent_id FROM conversations`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `SELECT parent_request_id FROM submissions`); err != nil {
		t.Fatal(err)
	}
}

func TestDropTasksMigrationRollsBackAndAppliesAgain(t *testing.T) {
	// Given a database at the newest schema.
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "control.sqlite")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	provider, err := goose.NewProvider(goose.DialectSQLite3, conn, migrationFS(t, func(name string) bool { return name <= dropTasksMigration }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	// When the migration rolls back and applies again.
	if _, err := provider.Down(t.Context()); err != nil {
		t.Fatalf("down: %v", err)
	}
	// Then down restores both tables, empty, and up drops them again.
	for _, table := range []string{"tasks", "task_steer_receipts"} {
		var rows int
		if err := conn.QueryRowContext(t.Context(), `SELECT count(*) FROM `+table).Scan(&rows); err != nil || rows != 0 {
			t.Fatalf("%s after down: %d, %v", table, rows, err)
		}
	}
	if _, err := provider.UpByOne(t.Context()); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if _, err := conn.ExecContext(t.Context(), `SELECT 1 FROM tasks`); err == nil {
		t.Fatal("up kept tasks")
	}
}
