package db_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestUserUpgradesAndRollsBackRetiredStorage(t *testing.T) {
	// Given retired storage, a delivery receipt, and an entry with no recorded author.
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
	if _, err := provider.UpTo(ctx, 20261126014758); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `
INSERT INTO agents(id, name, model_provider, model_id, created_at) VALUES ('bot', 'Bot', '', '', 1);
INSERT INTO conversations(id, agent_id, channel_key, created_at) VALUES (1, 'bot', 'main', 1);
INSERT INTO artifacts(id, agent_id, conversation_id, display_conversation_id, operation_key, name, size, created_at)
VALUES ('artifact', 'bot', 1, 1, 'artifact-op', 'file.txt', 1, 1);
INSERT INTO channel_peers(peer, channel_key) VALUES ('telegram:7', 'peer:7');
INSERT INTO submissions(id, conversation_id, content, status, answer, created_at, delivered_at, delivered_parts)
VALUES ('receipt', 1, 'input', 'done', 'reply', 1, 2, 1);
INSERT INTO tasks(id, number, assignee_bot_id, title, created_at, updated_at) VALUES ('task', 1, 'bot', 'Task', 1, 1);
INSERT INTO task_entries(task_id, seq, content, created_at, meta_json)
VALUES ('task', 1, 'Entry', 1, '{"legacyAuthor":"unknown"}');`); err != nil {
		t.Fatal(err)
	}

	// When the database goes up, down, and up again, the retired storage is dropped and restored.
	for _, phase := range []struct {
		name string
		up   bool
	}{{"first up", true}, {"down", false}, {"second up", true}} {
		t.Run(phase.name, func(t *testing.T) {
			if phase.up {
				if _, err := provider.UpByOne(ctx); err != nil {
					t.Fatal(err)
				}
			} else if _, err := provider.Down(ctx); err != nil {
				t.Fatal(err)
			}
			wantColumns, wantObjects, wantAuthor := 0, 0, "unknown:{}"
			if !phase.up {
				wantColumns, wantObjects, wantAuthor = 6, 3, `owner:{"legacyAuthor":"unknown"}`
			}
			var columns, objects, violations int
			if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE
(type = 'table' AND name IN ('artifacts', 'channel_peers')) OR (type = 'index' AND name = 'artifacts_by_conversation' AND tbl_name = 'artifacts')`).Scan(&objects); err != nil || objects != wantObjects {
				t.Fatalf("retired schema objects = %d, want %d: %v", objects, wantObjects, err)
			}
			if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('submissions') WHERE name IN
('delivered_parts', 'rendered_reply', 'channel_retry_at', 'channel_remote_ids', 'channel_group_fallback', 'channel_group_cursor')`).Scan(&columns); err != nil || columns != wantColumns {
				t.Fatalf("direct-delivery columns = %d, want %d: %v", columns, wantColumns, err)
			}
			if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
				t.Fatalf("foreign key violations = %d: %v", violations, err)
			}
			var author, receipt string
			if err := conn.QueryRowContext(ctx, `SELECT actor_kind || ':' || meta_json FROM task_entries WHERE task_id = 'task' AND seq = 1`).Scan(&author); err != nil || author != wantAuthor {
				t.Fatalf("author = %q, want %q: %v", author, wantAuthor, err)
			}
			if err := conn.QueryRowContext(ctx, `SELECT answer || ':' || delivered_at FROM submissions WHERE id = 'receipt'`).Scan(&receipt); err != nil || receipt != "reply:2" {
				t.Fatalf("delivery receipt = %q: %v", receipt, err)
			}
		})
	}
}
