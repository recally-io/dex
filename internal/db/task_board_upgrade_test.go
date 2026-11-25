package db_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/recally-io/dex/internal/db"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestUserKeepsTaskHistoryWhenUpgradingToTaskBoard(t *testing.T) {
	const legacyMigration = "20261125125254_work_task_multi_sessions.sql"
	const legacyVersion = 20261125125254
	path := filepath.Join(t.TempDir(), "control.sqlite")
	ctx := t.Context()

	// Given a Task database from before Phase 1.
	old, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = old.Close() })
	legacy, err := goose.NewProvider(goose.DialectSQLite3, old, migrationFS(t, func(name string) bool {
		return name <= legacyMigration
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Up(ctx); err != nil {
		t.Fatalf("build legacy database: %v", err)
	}
	for _, stmt := range []string{
		`INSERT INTO agents (id, name, model_provider, model_id, created_at) VALUES
			('alpha', 'Alpha', 'faux', 'echo', 1), ('beta', 'Beta', 'faux', 'echo', 1)`,
		`INSERT INTO conversations (id, agent_id, channel_key, created_at) VALUES
			(1, 'alpha', 'main', 2), (2, 'beta', 'main', 2),
			(3, 'alpha', 'chat:alpha', 3), (4, 'beta', 'chat:beta', 4)`,
		// The insertion order and update times differ from the Task number order.
		`INSERT INTO work_tasks (id, agent_id, title, lifecycle, created_at, updated_at) VALUES
			('c', 'beta', 'Cancelled', 'cancelled', 20, 30),
			('b', 'beta', 'Completed', 'completed', 10, 40),
			('a', 'alpha', 'Open', 'open', 10, 50)`,
		`INSERT INTO work_task_sessions (task_id, conversation_id, agent_id, is_primary, linked_at) VALUES
			('a', 3, 'alpha', 0, 12), ('a', 4, 'beta', 1, 11)`,
		`INSERT INTO work_source_events (id, namespace, change_key, event_type, objects, revision, content, source_url,
			occurred_at, received_at, task_id, disposition) VALUES
			('event', 'github', 'change', 'opened', '[]', 'r1', 'Source update', '', 14, 15, 'a', 'attached')`,
		`INSERT INTO work_task_entries (id, task_id, author, event_id, content, created_at) VALUES
			(2, 'a', 'owner', NULL, 'Owner note', 13), (6, 'b', 'owner', NULL, 'Completion note', 16),
			(9, 'a', 'agent', NULL, 'Bot note', 12), (12, 'a', '', NULL, 'Old note', 10),
			(22, 'a', '', 'event', 'Source update', 15)`,
		`INSERT INTO work_task_objects (namespace, object_type, object_key, task_id) VALUES ('github', 'pull', '42', 'a')`,
		`INSERT INTO work_task_waits (id, task_id, namespace, object_type, object_key, event_type, revision, after_at) VALUES
			(1, 'a', 'github', 'pull', '42', 'merged', 'r1', 15)`,
		`INSERT INTO submissions (id, conversation_id, kind, content, status, created_at) VALUES
			('input', 1, 'task_result', 'Task input', 'done', 16)`,
		`INSERT INTO work_task_inputs (submission_id, task_id) VALUES ('input', 'a')`,
		`INSERT INTO questions (id, agent_id, conversation_id, source_submission_id, tool_operation_key, text,
			options_json, status, created_at) VALUES ('question', 'alpha', 1, 'input', 'ask', 'Proceed?', '[]', 'open', 17)`,
	} {
		if _, err := old.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed legacy database: %s: %v", stmt, err)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	// When dex opens the database, the Board keeps the Task history and links.
	open := func() *sql.DB {
		t.Helper()
		conn, err := db.Open(ctx, path)
		if err != nil {
			t.Fatalf("upgrade Task Board: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	conn := open()
	assertLinks := func(conn *sql.DB) {
		t.Helper()
		assertTaskBoardRows(t, conn, `SELECT json_array(namespace, object_type, object_key, task_id) FROM work_task_objects`,
			`["github","pull","42","a"]`)
		assertTaskBoardRows(t, conn, `SELECT json_array(id, task_id, namespace, object_type, object_key, event_type, revision, after_at, resolved_by) FROM work_task_waits`,
			`[1,"a","github","pull","42","merged","r1",15,null]`)
		assertTaskBoardRows(t, conn, `SELECT json_array(i.submission_id, i.task_id, s.kind, s.content, s.conversation_id)
			FROM work_task_inputs i JOIN submissions s ON s.id = i.submission_id`, `["input","a","task_result","Task input",1]`)
		assertTaskBoardRows(t, conn, `SELECT json_array(id, agent_id, conversation_id, source_submission_id, text, status) FROM questions`,
			`["question","alpha",1,"input","Proceed?","open"]`)
		assertTaskBoardRows(t, conn, `SELECT json_array(id, namespace, change_key, task_id, disposition, content) FROM work_source_events`,
			`["event","github","change","a","attached","Source update"]`)
		assertTaskBoardRows(t, conn, `SELECT CAST(foreign_keys AS TEXT) FROM pragma_foreign_keys`, "1")
		assertTaskBoardRows(t, conn, `SELECT json_array("table", rowid, parent, fkid) FROM pragma_foreign_key_check`)
	}
	assertBoard := func(conn *sql.DB) {
		t.Helper()
		assertTaskBoardRows(t, conn, `SELECT json_array(id, number, assignee_bot_id, title, status, next_entry_seq, revision)
			FROM work_tasks ORDER BY number`,
			`["a",1,"alpha","Open","in_progress",5,1]`, `["b",2,"beta","Completed","done",2,1]`, `["c",3,"beta","Cancelled","cancelled",1,1]`)
		assertTaskBoardRows(t, conn, `SELECT json_array(id, task_id, seq, kind, actor_kind, actor_bot_id, trust, event_id, content, created_at)
			FROM work_task_entries ORDER BY id`,
			`[2,"a",1,"note","owner","","owner",null,"Owner note",13]`,
			`[6,"b",1,"note","owner","","owner",null,"Completion note",16]`,
			`[9,"a",2,"note","bot","alpha","agent",null,"Bot note",12]`,
			`[12,"a",3,"note","owner","","owner",null,"Old note",10]`,
			`[22,"a",4,"external","external","","external","event","Source update",15]`)
		assertTaskBoardRows(t, conn, `SELECT json_extract(meta_json, '$.legacyAuthor') FROM work_task_entries WHERE id = 12`, "unknown")
		assertTaskBoardRows(t, conn, `SELECT json_array(id, task_id, bot_id, kind, conversation_id, is_primary, state, started_at)
			FROM task_executions ORDER BY conversation_id`,
			`["session:a:3","a","alpha","dex_session",3,0,"running",12]`, `["session:a:4","a","beta","dex_session",4,1,"running",11]`)
		assertLinks(conn)
	}
	assertBoard(conn)
	assertTaskBoardNewWrites(t, conn)

	// Keep wake receipts through downgrade without treating them as channel replies.
	if _, err := conn.ExecContext(ctx, `INSERT INTO submissions (id, conversation_id, kind, content, status, created_at, delivered_at)
		VALUES ('undelivered-wake', 1, 'task_wake', '', 'done', 60, NULL),
		       ('delivered-wake', 2, 'task_wake', '', 'done', 60, 61),
		       ('next-wake', 1, 'task_wake', '', 'pending', 62, NULL)`); err != nil {
		t.Fatal(err)
	}
	// When Goose rolls back Phase 2, it removes the dependency schema and keeps existing Tasks.
	provider, err := goose.NewProvider(goose.DialectSQLite3, conn, migrationFS(t, func(string) bool { return true }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 20261125152756); err != nil {
		t.Fatalf("roll back Phase 2: %v", err)
	}
	assertTaskBoardRows(t, conn, `SELECT name FROM sqlite_schema WHERE name IN ('task_dependencies', 'task_dependencies_upstream', 'work_tasks_parent')`)
	assertBoard(conn)

	// When Goose rolls back Phase 1, the old schema can read the same history.
	down, err := provider.DownTo(ctx, legacyVersion)
	if err != nil || len(down) != 2 {
		t.Fatalf("roll back Phase 1: applied %d down migrations: %v", len(down), err)
	}
	assertTaskBoardRows(t, conn, `SELECT json_array(id, agent_id, title, lifecycle) FROM work_tasks ORDER BY id`,
		`["a","alpha","Open","open"]`, `["b","beta","Completed","completed"]`, `["c","beta","Cancelled","cancelled"]`)
	assertTaskBoardRows(t, conn, `SELECT json_array(id, task_id, author, event_id, content, created_at) FROM work_task_entries ORDER BY id`,
		`[2,"a","owner",null,"Owner note",13]`, `[6,"b","owner",null,"Completion note",16]`,
		`[9,"a","agent",null,"Bot note",12]`, `[12,"a","",null,"Old note",10]`, `[22,"a","","event","Source update",15]`)
	assertTaskBoardRows(t, conn, `SELECT json_array(task_id, conversation_id, agent_id, is_primary, linked_at)
		FROM work_task_sessions ORDER BY conversation_id`, `["a",3,"alpha",0,12]`, `["a",4,"beta",1,11]`)
	assertLinks(conn)
	assertTaskBoardRows(t, conn, `SELECT json_array(id, kind, delivered_at IS NOT NULL) FROM submissions
		WHERE id IN ('undelivered-wake', 'delivered-wake') ORDER BY id`,
		`["delivered-wake","task_result",1]`, `["undelivered-wake","task_result",1]`)
	assertTaskBoardRows(t, conn, `SELECT CAST(delivered_at AS TEXT) FROM submissions WHERE id = 'delivered-wake'`, "61")
	_, err = conn.ExecContext(ctx, `INSERT INTO submissions (id, conversation_id, kind, content, status, created_at)
		VALUES ('wake', 1, 'task_wake', '', 'pending', 60)`)
	assertTaskBoardSQLiteError(t, err, sqlite3.SQLITE_CONSTRAINT_CHECK)
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	// Then the next startup upgrades the database again without losing history.
	conn = open()
	assertBoard(conn)
	assertTaskBoardNewWrites(t, conn)
}

func assertTaskBoardRows(t *testing.T, conn *sql.DB, query string, want ...string) {
	t.Helper()
	rows, err := conn.QueryContext(t.Context(), query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		got = append(got, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%s\n got: %q\nwant: %q", query, got, want)
	}
}

func assertTaskBoardNewWrites(t *testing.T, conn *sql.DB) {
	t.Helper()
	ctx := t.Context()
	// Do not include new writes when checking whether the legacy data survives a round trip.
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var next int
	if err := tx.QueryRowContext(ctx, `INSERT INTO work_task_numbers DEFAULT VALUES RETURNING number`).Scan(&next); err != nil || next != 4 {
		t.Fatalf("next Task number: got %d, want 4: %v", next, err)
	}
	const wake = `INSERT INTO submissions (id, conversation_id, kind, content, status, created_at)
		VALUES (?, ?, 'task_wake', '', 'pending', 60)`
	if _, err := tx.ExecContext(ctx, wake, "wake-1", 1); err != nil {
		t.Fatalf("insert first task_wake: %v", err)
	}
	_, err = tx.ExecContext(ctx, wake, "wake-duplicate", 1)
	assertTaskBoardSQLiteError(t, err, sqlite3.SQLITE_CONSTRAINT_UNIQUE)
	if _, err := tx.ExecContext(ctx, wake, "wake-2", 2); err != nil {
		t.Fatalf("insert task_wake for another Main: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE submissions SET admitted_at = 61 WHERE id = 'wake-1'`); err != nil {
		t.Fatalf("admit first task_wake: %v", err)
	}
	if _, err := tx.ExecContext(ctx, wake, "wake-3", 1); err != nil {
		t.Fatalf("insert task_wake after admission: %v", err)
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM work_task_entries WHERE id = 2`)
	assertTaskBoardSQLiteError(t, err, sqlite3.SQLITE_CONSTRAINT_TRIGGER)
	_, err = tx.ExecContext(ctx, `UPDATE work_task_entries SET content = 'changed' WHERE id = 2`)
	assertTaskBoardSQLiteError(t, err, sqlite3.SQLITE_CONSTRAINT_TRIGGER)
	if _, err := tx.ExecContext(ctx, `DELETE FROM work_source_events WHERE id = 'event'`); err != nil {
		t.Fatalf("clear removed source event link: %v", err)
	}
	var eventID sql.NullString
	var trust string
	if err := tx.QueryRowContext(ctx, `SELECT event_id, trust FROM work_task_entries WHERE id = 22`).Scan(&eventID, &trust); err != nil || eventID.Valid || trust != "external" {
		t.Fatalf("entry after source event deletion: event = %v, trust = %q, err = %v", eventID, trust, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func assertTaskBoardSQLiteError(t *testing.T, err error, want int) {
	t.Helper()
	if sqliteErr, ok := errors.AsType[*sqlite.Error](err); !ok || sqliteErr.Code() != want {
		t.Fatalf("want SQLite error code %d, got %v", want, err)
	}
}
