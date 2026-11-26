package db_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestUserKeepsTaskDataAcrossTableRenameAndRollback(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	const previousVersion = 20261125171516
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "control.sqlite")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	provider, err := goose.NewProvider(goose.DialectSQLite3, conn, migrationFS(t, func(string) bool { return true }))
	if err != nil {
		t.Fatal(err)
	}

	// Given a database with entries, source links, and a replay receipt.
	if _, err := provider.UpTo(ctx, previousVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO agents (id, name, model_provider, model_id, created_at) VALUES ('bot', 'Bot', '', '', 1);
		INSERT INTO conversations (id, agent_id, channel_key, created_at) VALUES (1, 'bot', 'main', 1);
		INSERT INTO submissions (id, conversation_id, content, status, created_at) VALUES ('input', 1, 'Proceed', 'done', 2);
		INSERT INTO work_task_numbers (number) VALUES (42);
		INSERT INTO work_tasks (id, assignee_bot_id, title, number, created_at, updated_at) VALUES ('task', 'bot', 'Ship', 42, 2, 3);
		INSERT INTO work_source_events (id, namespace, change_key, event_type, objects, revision, content, source_url, occurred_at, received_at, disposition)
			VALUES ('event', 'github', 'opened:7', 'opened', '[]', '', 'Opened PR', '', 2, 3, 'unmatched');
		INSERT INTO work_task_entries (task_id, seq, content, created_at) VALUES ('task', 1, 'Keep this note', 3);
		INSERT INTO work_task_inputs (submission_id, task_id) VALUES ('input', 'task');
		INSERT INTO work_task_objects (namespace, object_type, object_key, task_id) VALUES ('github', 'pull', '7', 'task');
		INSERT INTO work_task_waits (task_id, namespace, object_type, object_key, event_type, revision, after_at)
			VALUES ('task', 'github', 'pull', '7', 'merged', '', 3);
		INSERT INTO work_task_tool_operations (key, agent_id, conversation_id, source_request_id, action, result)
			VALUES ('op', 'bot', 1, 'input', 'create', 'result');
		INSERT INTO work_source_receipts (namespace, receipt_key, event_id) VALUES ('github', 'delivery', 'event');
		INSERT INTO work_source_holds (event_id, held_at) VALUES ('event', 3);
		INSERT INTO task_executions (id, task_id, bot_id, kind, conversation_id, started_at)
			VALUES ('run', 'task', 'bot', 'dex_session', 1, 3);
	`); err != nil {
		t.Fatal(err)
	}

	// When dex upgrades, rolls back, and upgrades again, the active names find the same records.
	for _, step := range []struct {
		name string
		down bool
	}{{"upgrade", false}, {"rollback", true}, {"upgrade again", false}} {
		t.Run(step.name, func(t *testing.T) {
			var err error
			if step.down {
				_, err = provider.Down(ctx)
			} else {
				_, err = provider.UpByOne(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range []struct{ old, current, column, want string }{
				{"work_tasks", "tasks", "title", "Ship"},
				{"work_task_numbers", "task_numbers", "CAST(number AS TEXT)", "42"},
				{"work_task_entries", "task_entries", "content", "Keep this note"},
				{"work_task_inputs", "task_inputs", "submission_id", "input"},
				{"work_task_objects", "task_objects", "object_key", "7"},
				{"work_task_waits", "task_waits", "event_type", "merged"},
				{"work_task_tool_operations", "task_tool_operations", "result", "result"},
				{"work_source_events", "task_source_events", "content", "Opened PR"},
				{"work_source_receipts", "task_source_receipts", "receipt_key", "delivery"},
				{"work_source_holds", "task_source_holds", "event_id", "event"},
			} {
				table, absent := row.current, row.old
				if step.down {
					table, absent = row.old, row.current
				}
				assertTaskBoardRows(t, conn, "SELECT "+row.column+" FROM "+table, row.want)
				assertTaskBoardRows(t, conn, "SELECT name FROM sqlite_schema WHERE type = 'table' AND name = '"+absent+"'")
			}

			// Then foreign keys and the append-only ledger still protect the data.
			assertTaskBoardRows(t, conn, `SELECT CAST(foreign_keys AS TEXT) FROM pragma_foreign_keys`, "1")
			assertTaskBoardRows(t, conn, `SELECT json_array("table", rowid, parent, fkid) FROM pragma_foreign_key_check`)
			entries := "task_entries"
			if step.down {
				entries = "work_task_entries"
			}
			assertTaskBoardRows(t, conn, "SELECT name FROM sqlite_schema WHERE type = 'trigger' AND tbl_name = '"+entries+"' ORDER BY name", entries+"_no_delete", entries+"_no_update")
			_, err = conn.ExecContext(ctx, "DELETE FROM "+entries)
			assertTaskBoardSQLiteError(t, err, sqlite3.SQLITE_CONSTRAINT_TRIGGER)
			_, err = conn.ExecContext(ctx, "UPDATE "+entries+" SET content = 'changed'")
			assertTaskBoardSQLiteError(t, err, sqlite3.SQLITE_CONSTRAINT_TRIGGER)
		})
	}
}
