package db_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestUserKeepsCurrentReceiptsAndHistoryDuringLegacySchemaCleanup(t *testing.T) {
	t.Parallel()
	// Given saved Session links, delivery data, callback receipts, and a Task execution token.
	ctx := t.Context()
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "control.sqlite")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	provider, err := goose.NewProvider(goose.DialectSQLite3, conn, migrationFS(t, func(string) bool { return true }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 20261126022412); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `
INSERT INTO agents (id, name, model_provider, model_id, created_at) VALUES ('bot', 'Bot', '', '', 1);
INSERT INTO conversations (id, agent_id, channel_key, execution_id, parent_id, created_at) VALUES
    (1, 'bot', 'web:old', 11, NULL, 1), (2, 'bot', 'task:old', 12, 1, 2);
INSERT INTO submissions (id, conversation_id, content, status, kind, producer, source_key, exit_action, channel_revoked, created_at) VALUES
    ('root', 1, 'Message', 'done', 'message', 'user', NULL, 'ask_user', 0, 1),
    ('child', 2, 'Child turn', 'done', 'message', 'sub_session', NULL, 'report_parent', 1, 2),
    ('timer:old:fire', 1, 'Wake', 'pending', 'task_result', 'scheduler', 'timer:old:fire', 'stay_silent', 0, 3);
INSERT INTO monitors (id, operation_key, agent_id, conversation_id, root_request_id, source_submission_id,
    chain_depth, due_at, prompt, state, fire_submission_id, created_at, updated_at)
VALUES ('old', 'old', 'bot', 1, 'root', 'root', 0, 3, 'Wake', 'fired', 'timer:old:fire', 1, 3);
INSERT INTO outbox_messages (rowid, id, event_id, agent_id, conversation_id, source_submission_id, kind, peer, status, attempts, created_at)
VALUES (17, 'delivery', 'event', 'bot', 2, 'child', 'system_error', 'telegram:42', 'sent', 2, 4);
INSERT INTO outbox_files (id, agent_id, operation_key, outbox_id, name, mime_type, body)
VALUES ('file', 'bot', 'file-op', 'delivery', 'file.txt', 'text/plain', X'AB');
INSERT INTO telegram_callbacks (id, agent_id, source_id, part, ordinal, peer, channel_context, conversation_id,
    execution_scope, execution_id, context_revision, label, object_id, action, message_id, expires_at, consumed_at) VALUES
    ('private', 'bot', 'source', 0, 0, 'telegram:42', '{}', 1, '', 11, '', 'Status', '1', '/status', '9', 10, 5),
    ('scoped', 'bot', 'source', 1, 0, 'telegram:-100', '{}', 1, 'tg_123_-100_7', 11, '', 'Status', '1', '/status', '9', 10, 5);
INSERT INTO automations (id, agent_id, name, prompt, schedule, time_zone, enabled, created_at, updated_at)
VALUES ('auto', 'bot', 'Check', 'Wake', '{}', 'UTC', 1, 1, 1);
INSERT INTO automation_runs (id, automation_id, status, reason, created_at)
VALUES ('failed-probe', 'auto', 'skipped', 'the environment Box is unavailable', 2);
INSERT INTO tasks (id, number, assignee_bot_id, title, created_at, updated_at) VALUES ('task', 1, 'bot', 'Task', 1, 1);
INSERT INTO task_executions (id, task_id, bot_id, kind, state, started_at) VALUES
    ('dispatch', 'task', 'bot', 'external_agent', 'dispatched', 1), ('idle', 'task', 'bot', 'external_agent', 'idle', 2);
INSERT INTO task_execution_tokens (token_hash, task_id, execution_id, lease_epoch, created_at) VALUES ('token', 'task', 'idle', 1, 2);
`); err != nil {
		t.Fatal(err)
	}

	// When the database goes up, down, and up again, the normalized data stays usable.
	for _, phase := range []string{"up", "down", "up again"} {
		t.Run(phase, func(t *testing.T) {
			if phase == "down" {
				if _, err := provider.Down(ctx); err != nil {
					t.Fatal(err)
				}
			} else if _, err := provider.UpByOne(ctx); err != nil {
				t.Fatal(err)
			}
			check := func(query, want string) {
				t.Helper()
				var got string
				if err := conn.QueryRowContext(ctx, query).Scan(&got); err != nil || got != want {
					t.Fatalf("%s: got %q, want %q: %v", query, got, want, err)
				}
			}
			check(`PRAGMA integrity_check`, "ok")
			check(`SELECT count(*) FROM pragma_foreign_key_check`, "0")
			check(`SELECT child.channel_key || ':' || parent.channel_key || ':' || child.execution_id
				FROM conversations child JOIN conversations parent ON parent.id = child.parent_id WHERE child.id = 2`, "chat:legacy:2:chat:legacy:1:12")
			check(`SELECT exit_action FROM submissions WHERE id = 'root'`, "ask_user")
			check(`SELECT exit_action || ':' || channel_revoked FROM submissions WHERE id = 'child'`, ":1")
			check(`SELECT status || ':' || exit_action FROM submissions WHERE id = 'timer:old:fire'`, "unanswered:")
			check(`SELECT o.rowid || ':' || o.kind || ':' || o.status || ':' || o.attempts || ':' || hex(f.body)
				FROM outbox_messages o JOIN outbox_files f ON f.outbox_id = o.id`, "17:control:sent:2:AB")
			check(`SELECT id || ':' || message_id || ':' || consumed_at FROM telegram_callbacks`, "private:9:5")
			check(`SELECT count(*) FROM task_executions WHERE state = 'stale' AND ended_at IS NOT NULL`, "2")
			check(`SELECT execution_id FROM task_execution_tokens WHERE token_hash = 'token'`, "idle")
			want, runner := "0", "Runner"
			if phase == "down" {
				want, runner = "1", "environment"
			}
			check(`SELECT reason FROM automation_runs WHERE id = 'failed-probe'`, "the "+runner+" Box is unavailable")
			check(`SELECT count(*) FROM sqlite_schema WHERE name = 'task_entries_task_seq'`, want)
			check(`SELECT count(*) FROM pragma_table_info('submissions') WHERE name = 'channel_delivery_error'`, want)
			check(`SELECT count(*) FROM pragma_table_info('telegram_callbacks') WHERE name = 'part'`, want)
			check(`SELECT count(*) FROM pragma_table_info('conversations') WHERE name = 'execution_scope'`, want)
		})
	}
}
