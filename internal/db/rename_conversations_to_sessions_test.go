package db_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestUserKeepsSessionsAndLinksAcrossRename(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "control.sqlite")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	provider, err := goose.NewProvider(goose.DialectSQLite3, conn, migrationFS(t, func(string) bool { return true }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 20261126093534); err != nil {
		t.Fatal(err)
	}

	// Given a parent, a child, and saved work that refers to both.
	if _, err := conn.ExecContext(ctx, `
INSERT INTO agents (id, name, model_provider, model_id, created_at) VALUES ('bot', 'Bot', '', '', 1);
INSERT INTO conversations (id, agent_id, channel_key, execution_id, parent_id, created_at) VALUES
    (1, 'bot', 'main', 10, NULL, 1), (2, 'bot', 'chat:child', 20, 1, 2);
INSERT INTO submissions (id, conversation_id, result_child_id, content, status, created_at)
    VALUES ('input', 1, 2, 'saved input', 'done', 1);
INSERT INTO automations (id, agent_id, name, prompt, schedule, time_zone, enabled, created_at, updated_at, session, target_conversation_id)
    VALUES ('auto', 'bot', 'Watch', 'Check', '{}', 'UTC', 1, 1, 1, 'session', 2);
INSERT INTO automation_tool_operations (key, agent_id, conversation_id, source_request_id, action, result)
    VALUES ('aop', 'bot', 1, 'input', 'create', '{}');
INSERT INTO questions (id, agent_id, conversation_id, source_submission_id, tool_operation_key, text, options_json, status, created_at)
    VALUES ('question', 'bot', 1, 'input', 'qop', 'Continue?', '[]', 'open', 1);
INSERT INTO fork_receipts (id, source_id, entry_id, before_entry, fork_id) VALUES ('fork', 1, 10, 0, 2);
INSERT INTO compact_receipts (id, conversation_id, instructions) VALUES ('compact', 2, 'Summarize');
INSERT INTO background_commands (id, call_key, conversation_id, source_request_id, wake, label, command, cwd, output_path, pid, pgid, process_started_at, state)
    VALUES ('proc', 'pop', 1, 'input', 'exit', 'Process', 'echo test', '/work', '/output', 10, 10, 1, 'running');
INSERT INTO monitors (id, operation_key, agent_id, conversation_id, root_request_id, source_submission_id, chain_depth, due_at, prompt, created_at, updated_at)
    VALUES ('monitor', 'mop', 'bot', 1, 'input', 'input', 0, 2, 'Check', 1, 1);
INSERT INTO task_tool_operations (key, agent_id, conversation_id, source_request_id, action, result)
    VALUES ('top', 'bot', 1, 'input', 'create', '{}');
INSERT INTO main_turn_stops (conversation_id, target) VALUES (1, 'input');
INSERT INTO web_notifications (id, event_id, agent_id, conversation_id, source_submission_id, body, created_at)
    VALUES ('notice', 'notice:event', 'bot', 1, 'input', 'Saved notice', 1);
INSERT INTO telegram_settings (agent_id, token, allowed_users) VALUES ('bot', '123:fixture', '42');
INSERT INTO telegram_callbacks (id, agent_id, source_id, ordinal, peer, channel_context, conversation_id, execution_id, context_revision, label, object_id, action, expires_at)
    VALUES ('cb', 'bot', 'input', 0, 'telegram:42', '{"accountId":"123"}', 1, 10, '1', 'Open', 'question', 'open', 2);
INSERT INTO outbox_messages (id, event_id, agent_id, conversation_id, source_submission_id, kind, peer, channel_context, body, created_at)
    VALUES ('reply', 'reply:event', 'bot', 1, 'input', 'reply', 'telegram:42', '{"accountId":"123"}', 'Saved reply', 1);
INSERT INTO tasks (id, assignee_bot_id, title, created_at, updated_at) VALUES ('task', 'bot', 'Task', 1, 1);
INSERT INTO task_sessions (id, task_id, bot_id, conversation_id, started_at) VALUES ('link', 'task', 'bot', 2, 1);`); err != nil {
		t.Fatal(err)
	}

	// When Dex upgrades, rolls back, and upgrades again, all links still resolve.
	for _, phase := range []struct {
		name, table, column string
		up                  bool
	}{
		{"up", "sessions", "session_id", true},
		{"down", "conversations", "conversation_id", false},
		{"up again", "sessions", "session_id", true},
	} {
		t.Run(phase.name, func(t *testing.T) {
			if phase.up {
				if _, err := provider.UpByOne(ctx); err != nil {
					t.Fatal(err)
				}
			} else if _, err := provider.Down(ctx); err != nil {
				t.Fatal(err)
			}
			check := func(query, want string) {
				t.Helper()
				var got string
				if err := conn.QueryRowContext(ctx, query).Scan(&got); err != nil || got != want {
					t.Fatalf("%s = %q, want %q: %v", query, got, want, err)
				}
			}
			check(`PRAGMA integrity_check`, "ok")
			check(`PRAGMA foreign_keys`, "1")
			check(`SELECT count(*) FROM pragma_foreign_key_check`, "0")
			check(`SELECT parent.execution_id || ':' || child.execution_id FROM `+phase.table+` child JOIN `+phase.table+` parent ON child.parent_id = parent.id WHERE child.channel_key = 'chat:child'`, "10:20")
			check(`SELECT source_id || ':' || fork_id FROM fork_receipts WHERE id = 'fork'`, "1:2")
			check(`SELECT `+phase.column+` || ':' || result_child_id FROM submissions WHERE id = 'input' AND content = 'saved input'`, "1:2")
			check(`SELECT target_`+phase.column+` FROM automations WHERE id = 'auto'`, "2")
			for _, table := range []string{"automation_tool_operations", "questions", "background_commands", "monitors", "task_tool_operations", "main_turn_stops", "web_notifications", "telegram_callbacks", "outbox_messages"} {
				check(`SELECT `+phase.column+` FROM `+table, "1")
			}
			check(`SELECT `+phase.column+` FROM compact_receipts WHERE id = 'compact'`, "2")
			check(`SELECT `+phase.column+` FROM task_sessions WHERE task_id = 'task'`, "2")
		})
	}
}
