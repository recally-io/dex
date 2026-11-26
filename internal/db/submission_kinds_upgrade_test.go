package db_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestUserKeepsSubmissionHistoryWhenSystemInputsSplit(t *testing.T) {
	t.Parallel()
	const previous = 20261126063445
	const split = 20261126071600
	ctx := t.Context()
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "control.sqlite")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	provider, err := goose.NewProvider(goose.DialectSQLite3, conn, migrationFS(t, func(name string) bool {
		return name <= "20261126071600_split_task_result_submission_kinds.sql"
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, previous); err != nil {
		t.Fatal(err)
	}

	// Given a parent session with old system inputs, a pending Board wake, and linked delivery data.
	seed := []string{
		`INSERT INTO agents (id, name, model_provider, model_id, created_at) VALUES ('bot', 'Bot', 'faux', 'echo', 1)`,
		`INSERT INTO conversations (id, agent_id, channel_key, created_at) VALUES (1, 'bot', 'main', 1)`,
		`INSERT INTO submissions (rowid, id, conversation_id, kind, origin, status, content, created_at) VALUES
			(10, 'owner', 1, 'message', 'web', 'done', 'message', 10),
			(20, 'result:child', 1, 'task_result', 'task', 'done', 'answer', 20),
			(30, 'report:child', 1, 'task_result', 'telegram:7', 'pending', 'report', 30),
			(40, 'monitor:m:1', 1, 'task_result', 'web', 'done', 'monitor', 40),
			(50, 'background:p:exit', 1, 'task_result', 'task', 'done', 'process', 50),
			(60, 'old-monitor-id', 1, 'task_result', 'monitor', 'done', 'old monitor', 60),
			(70, 'task-alert:event', 1, 'task_result', 'web', 'done', 'alert', 70),
			(80, 'task-wake:board', 1, 'task_wake', 'web', 'pending', 'pointers', 80),
			(90, 'reset', 1, 'context_reset', 'web', 'done', '', 90)`,
		`INSERT INTO task_wake_admission_snapshots (submission_id, content) VALUES ('task-wake:board', 'snapshot')`,
		`INSERT INTO outbox_messages (id, event_id, agent_id, conversation_id, source_submission_id, kind, peer, created_at)
			VALUES ('out', 'event', 'bot', 1, 'result:child', 'proactive', 'telegram:7', 1)`,
		`INSERT INTO outbox_files (id, agent_id, operation_key, submission_id, name, mime_type, body)
			VALUES ('file', 'bot', 'tool', 'report:child', 'note', 'text/plain', X'61')`,
	}
	for _, stmt := range seed {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %s: %v", stmt, err)
		}
	}

	const schemaObjects = `SELECT name FROM sqlite_schema WHERE tbl_name = 'submissions' AND type IN ('index', 'trigger') ORDER BY name`
	var objects []string
	readRows(t, conn, schemaObjects, func(scan func(...any) error) error {
		var name string
		if err := scan(&name); err != nil {
			return err
		}
		objects = append(objects, name)
		return nil
	})

	assertData := func(result, wake string) {
		t.Helper()
		assertTaskBoardRows(t, conn, `PRAGMA integrity_check`, "ok")
		assertTaskBoardRows(t, conn, `SELECT CAST(foreign_keys AS TEXT) FROM pragma_foreign_keys`, "1")
		assertTaskBoardRows(t, conn, `SELECT "table" FROM pragma_foreign_key_check`)
		assertTaskBoardRows(t, conn, `SELECT CAST(strict AS TEXT) FROM pragma_table_list WHERE name = 'submissions'`, "1")
		assertTaskBoardRows(t, conn, schemaObjects, objects...)
		assertTaskBoardRows(t, conn, `SELECT rowid || ':' || id || ':' || kind || ':' || content FROM submissions ORDER BY rowid`,
			"10:owner:message:message", "20:result:child:"+result+":answer", "30:report:child:"+result+":report",
			"40:monitor:m:1:"+wake+":monitor", "50:background:p:exit:"+wake+":process", "60:old-monitor-id:"+wake+":old monitor",
			"70:task-alert:event:"+result+":alert", "80:task-wake:board:task_wake:pointers", "90:reset:context_reset:")
		assertTaskBoardRows(t, conn, `SELECT source_submission_id FROM outbox_messages WHERE id = 'out'`, "result:child")
		assertTaskBoardRows(t, conn, `SELECT submission_id FROM outbox_files WHERE id = 'file'`, "report:child")
		assertTaskBoardRows(t, conn, `SELECT content FROM task_wake_admission_snapshots WHERE submission_id = 'task-wake:board'`, "snapshot")
	}
	assertRejected := func(kind string, code int) {
		t.Helper()
		_, err := conn.ExecContext(ctx, `INSERT INTO submissions (id, conversation_id, kind, status, content, created_at)
			VALUES ('new', 1, ?, 'pending', '', 91)`, kind)
		assertTaskBoardSQLiteError(t, err, code)
	}

	// When Goose upgrades, rolls back, and upgrades again, links and receipt order stay intact.
	if _, err := provider.UpTo(ctx, split); err != nil {
		t.Fatalf("split submission kinds: %v", err)
	}
	assertData("session_result", "wake")
	assertRejected("task_result", sqlite3.SQLITE_CONSTRAINT_CHECK)
	if _, err := provider.DownTo(ctx, previous); err != nil {
		t.Fatalf("restore old submission kinds: %v", err)
	}
	assertData("task_result", "task_result")
	assertRejected("session_result", sqlite3.SQLITE_CONSTRAINT_CHECK)
	assertRejected("wake", sqlite3.SQLITE_CONSTRAINT_CHECK)
	if _, err := provider.UpTo(ctx, split); err != nil {
		t.Fatalf("split submission kinds again: %v", err)
	}
	assertData("session_result", "wake")

	// Then the upgraded database still enforces uniqueness and releases data on settlement.
	assertRejected("task_wake", sqlite3.SQLITE_CONSTRAINT_UNIQUE)
	if _, err := conn.ExecContext(ctx, `UPDATE submissions SET status = 'unanswered' WHERE id IN ('task-wake:board', 'report:child')`); err != nil {
		t.Fatal(err)
	}
	assertTaskBoardRows(t, conn, `SELECT submission_id FROM task_wake_admission_snapshots`)
	assertTaskBoardRows(t, conn, `SELECT submission_id FROM outbox_files`)
}
