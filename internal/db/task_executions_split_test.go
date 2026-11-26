package db_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/recally-io/dex/internal/db/sqlc"
	sqlite3 "modernc.org/sqlite/lib"
)

const splitTaskExecutionsSeed = `
INSERT INTO agents (id, name, model_provider, model_id, created_at) VALUES ('bot', 'Bot', '', '', 1);
INSERT INTO tasks (id, number, assignee_bot_id, title, created_at, updated_at) VALUES ('task', 1, 'bot', 'Task', 1, 1);
INSERT INTO conversations (id, agent_id, channel_key, parent_id, created_at) VALUES
  (1, 'bot', 'main', NULL, 1), (2, 'bot', 'child:first', 1, 2), (3, 'bot', 'child:empty', 1, 3);
INSERT INTO submissions (id, conversation_id, content, kind, status, parent_request_id, created_at, settled_at) VALUES
  ('parent', 1, '', 'message', 'done', NULL, 10, 20),
  ('standalone', 2, '', 'message', 'done', NULL, 20, 30),
  ('wake', 2, '', 'wake', 'done', 'parent', 25, 35),
  ('first', 2, '', 'message', 'pending', 'parent', 50, NULL),
  ('later', 2, '', 'message', 'done', 'parent', 40, 60);
INSERT INTO task_executions (id, task_id, bot_id, kind, conversation_id, turn_request_id, is_primary, state, started_at, ended_at) VALUES
  ('session:first', 'task', 'bot', 'dex_session', 2, '', 1, 'running', 2, NULL),
  ('session:empty', 'task', 'bot', 'dex_session', 3, '', 0, 'running', 3, NULL),
  ('session:known', 'task', 'bot', 'dex_session', 1, 'parent', 0, 'completed', 4, 20);
INSERT INTO task_executions (id, task_id, bot_id, kind, harness, runner_id, host_meta_json, workspace_path, git_branch,
  external_session_id, state, lease_epoch, lease_expires_at, lease_ttl_ms, last_heartbeat_at, dispatch_id, runner_enrollment, started_at, ended_at)
VALUES ('external', 'task', 'bot', 'external_agent', 'codex', 'runner', '{"hostname":"host"}', '/workspace', 'topic',
  'harness-session', 'completed', 2, 4000, 1000, 3000, 'dispatch-2', 'enrollment', 1000, 3500);
INSERT INTO task_execution_tokens (token_hash, task_id, execution_id, lease_epoch, revoked_at, created_at, dispatch_id) VALUES
  ('old-hash', 'task', 'external', 1, 2000, 1000, 'dispatch-1'), ('new-hash', 'task', 'external', 2, NULL, 2000, 'dispatch-2');
`

func TestUserKeepsTaskSessionsAndExternalTokensAcrossUpgrade(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	const previousVersion = 20261126071600
	const splitVersion = 20261126090719
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "control.sqlite")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	provider, err := goose.NewProvider(goose.DialectSQLite3, conn, migrationFS(t, func(string) bool { return true }))
	if err != nil {
		t.Fatal(err)
	}

	// Given saved Session links, an external run, and both its revoked and current tokens.
	if _, err := provider.UpTo(ctx, previousVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, splitTaskExecutionsSeed); err != nil {
		t.Fatal(err)
	}

	// When Goose upgrades, rolls back, and upgrades again, the same rows stay usable.
	for _, phase := range []struct {
		name string
		down bool
	}{{"up", false}, {"down", true}, {"up again", false}} {
		if !t.Run(phase.name, func(t *testing.T) {
			if phase.down {
				if _, err := provider.DownTo(ctx, previousVersion); err != nil {
					t.Fatal(err)
				}
			} else if _, err := provider.UpTo(ctx, splitVersion); err != nil {
				t.Fatal(err)
			}
			assertTaskBoardRows(t, conn, `PRAGMA integrity_check`, "ok")
			assertTaskBoardRows(t, conn, `SELECT CAST(foreign_keys AS TEXT) FROM pragma_foreign_keys`, "1")
			assertTaskBoardRows(t, conn, `SELECT json_array("table", rowid, parent, fkid) FROM pragma_foreign_key_check`)
			assertTaskBoardRows(t, conn, `SELECT json_array(token_hash, task_id, execution_id, lease_epoch, revoked_at, created_at, dispatch_id)
				FROM task_execution_tokens ORDER BY lease_epoch`,
				`["old-hash","task","external",1,2000,1000,"dispatch-1"]`, `["new-hash","task","external",2,null,2000,"dispatch-2"]`)

			// Then the first parent message in receipt order is linked even if it is still pending.
			sessions := ` FROM task_sessions ORDER BY id`
			runs := ` FROM task_external_runs`
			fkTable := "task_external_runs"
			if phase.down {
				sessions = ` FROM task_executions WHERE kind = 'dex_session' ORDER BY id`
				runs = ` FROM task_executions WHERE kind = 'external_agent'`
				fkTable = "task_executions"
				assertTaskBoardRows(t, conn, `SELECT name FROM sqlite_schema WHERE name IN ('task_sessions', 'task_external_runs')`)
			} else {
				assertTaskBoardRows(t, conn, `SELECT name FROM sqlite_schema WHERE name = 'task_executions'`)
			}
			assertTaskBoardRows(t, conn, `SELECT json_array(id, task_id, bot_id, conversation_id, turn_request_id, is_primary, state, started_at, ended_at)`+sessions,
				`["session:empty","task","bot",3,"",0,"running",3,null]`,
				`["session:first","task","bot",2,"first",1,"running",2,null]`,
				`["session:known","task","bot",1,"parent",0,"completed",4,20]`)
			assertTaskBoardRows(t, conn, `SELECT json_array(id, task_id, bot_id, harness, runner_id, host_meta_json ->> 'hostname', workspace_path,
				git_branch, external_session_id, state, lease_epoch, lease_expires_at, lease_ttl_ms, last_heartbeat_at, dispatch_id,
				runner_enrollment, started_at, ended_at)`+runs,
				`["external","task","bot","codex","runner","host","/workspace","topic","harness-session","completed",2,4000,1000,3000,"dispatch-2","enrollment",1000,3500]`)
			assertTaskBoardRows(t, conn, `SELECT "table" || ':' || on_delete FROM pragma_foreign_key_list('task_execution_tokens') WHERE "from" = 'execution_id'`, fkTable+":CASCADE")
		}) {
			return
		}
	}

	// Run the current queries against the current schema.
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	// A later result cannot settle the link while its first message is pending.
	q := sqlc.New(conn)
	const sessionOutcome = `SELECT json_array(turn_request_id, state, ended_at) FROM task_sessions WHERE id = 'session:first'`
	if err := q.ProjectTaskChildSession(ctx, 2); err != nil {
		t.Fatal(err)
	}
	assertTaskBoardRows(t, conn, sessionOutcome, `["first","running",null]`)
	if _, err := conn.ExecContext(ctx, `UPDATE submissions SET status = 'done', settled_at = 70 WHERE id = 'first'`); err != nil {
		t.Fatal(err)
	}
	if err := q.ProjectTaskChildSession(ctx, 2); err != nil {
		t.Fatal(err)
	}
	assertTaskBoardRows(t, conn, sessionOutcome, `["first","completed",70]`)

	// Tokens can no longer refer to a Session. Deleting an external run removes its tokens.
	_, err = conn.ExecContext(ctx, `INSERT INTO task_execution_tokens (token_hash, task_id, execution_id, lease_epoch, created_at)
		VALUES ('wrong-hash', 'task', 'session:first', 1, 4000)`)
	assertTaskBoardSQLiteError(t, err, sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY)
	if _, err := conn.ExecContext(ctx, `DELETE FROM task_external_runs WHERE id = 'external'`); err != nil {
		t.Fatal(err)
	}
	assertTaskBoardRows(t, conn, `SELECT token_hash FROM task_execution_tokens`)
}
