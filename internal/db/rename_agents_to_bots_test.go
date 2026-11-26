package db_test

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/plugin"
)

const beforeBotRename = 20261126095120

func TestUserKeepsBotDataAcrossRename(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "control.sqlite")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	provider, err := goose.NewProvider(goose.DialectSQLite3, conn, os.DirFS("migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, beforeBotRename); err != nil {
		t.Fatal(err)
	}

	// Given saved work, paired accounts, and enabled plugins from before the rename.
	fixture, err := os.ReadFile("testdata/rename_agents_to_bots.sql")
	if err != nil {
		t.Fatal(err)
	}
	execBotSQL(t, conn, string(fixture))
	for name, source := range botRenamePlugins {
		const firstMigration = "migrations/00001_tables.sql"
		content, err := os.ReadFile(filepath.Join("..", "..", "plugins", name, firstMigration))
		if err != nil {
			t.Fatal(err)
		}
		if err := plugin.Migrate(ctx, conn, name, fstest.MapFS{firstMigration: {Data: content}}); err != nil {
			t.Fatal(err)
		}
		execBotSQL(t, conn, source.insert(source.oldColumn))
	}
	execBotSQL(t, conn, `INSERT INTO plugin_email_imap_pulls (source_id, address, username, password, folder, configuration_id, uid_validity, last_uid)
VALUES ('source', 'example.invalid:993', 'fixture', 'secret', 'INBOX', 'config', 123, 456)`)

	// When Dex upgrades, rolls back, and upgrades again, the same data stays linked.
	for _, phase := range []struct {
		name, table, column, wechat, telegram string
		up                                    bool
	}{
		{"up", "bots", "bot_id", "wechat_bot_id", "telegram_bot_id", true},
		{"down", "agents", "agent_id", "bot_id", "bot_id", false},
		{"up again", "bots", "bot_id", "wechat_bot_id", "telegram_bot_id", true},
	} {
		t.Run(phase.name, func(t *testing.T) {
			if phase.up {
				if _, err := provider.UpByOne(t.Context()); err != nil {
					t.Fatal(err)
				}
			} else if _, err := provider.Down(t.Context()); err != nil {
				t.Fatal(err)
			}
			checkBotRenameIntegrity(t, conn)
			checkBotSQL(t, conn, `SELECT name FROM `+phase.table+` WHERE id = 'fixture'`, "Fixture")
			for _, table := range []string{
				"sessions", "dreams", "automations", "telegram_settings", "wechat_settings", "feishu_settings",
				"channel_commands", "automation_tool_operations", "automation_notification_targets",
				"questions", "question_messages", "telegram_album_members", "telegram_effects", "monitors",
				"task_tool_operations", "mcp_servers", "telegram_managed_setups", "channel_webhook_queue",
				"web_notifications", "telegram_callbacks", "outbox_messages", "outbox_files",
			} {
				checkBotSQL(t, conn, `SELECT `+phase.column+` FROM `+table, "fixture")
			}
			checkBotSQL(t, conn, `SELECT action_`+phase.column+` FROM questions`, "target")
			checkBotSQL(t, conn, `SELECT `+phase.wechat+` FROM wechat_settings`, "wx-bot")
			checkBotSQL(t, conn, `SELECT `+phase.telegram+` FROM telegram_managed_setups`, "777")
			checkBotSQL(t, conn, `SELECT assignee_bot_id FROM tasks`, "fixture")
			checkBotSQL(t, conn, `SELECT bot_id FROM task_sessions`, "fixture")
			checkBotSQL(t, conn, `SELECT bot_id FROM task_external_runs`, "fixture")
			checkBotSQL(t, conn, `SELECT group_concat(account_id, ',') FROM (
    SELECT account_id FROM configured_channel_addresses WHERE `+phase.column+` = 'fixture' ORDER BY platform
)`, "fs-app,123,wx-bot")
			checkBotSQL(t, conn, `SELECT status FROM outbox_messages WHERE id = 'reply'`, "pending")
			for _, source := range botRenamePlugins {
				checkBotSQL(t, conn, `SELECT `+source.oldColumn+` FROM `+source.table, "fixture")
			}

			// A pairing change still cancels pending delivery.
			tx, err := conn.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			if _, err := tx.ExecContext(t.Context(), `UPDATE telegram_settings SET allowed_users = '' WHERE `+phase.column+` = 'fixture'`); err != nil {
				t.Fatal(err)
			}
			var status string
			if err := tx.QueryRowContext(t.Context(), `SELECT status FROM outbox_messages WHERE id = 'reply'`).Scan(&status); err != nil || status != "failed" {
				t.Fatalf("unpaired reply status = %q, want failed: %v", status, err)
			}
		})
	}

	for name, source := range botRenamePlugins {
		if err := plugin.Migrate(ctx, conn, name, os.DirFS(filepath.Join("..", "..", "plugins", name))); err != nil {
			t.Fatal(err)
		}
		checkBotSQL(t, conn, `SELECT `+source.newColumn+` FROM `+source.table, "fixture")
	}
	checkBotSQL(t, conn, `SELECT uid_validity || ':' || last_uid FROM plugin_email_imap_pulls`, "123:456")
	checkBotRenameIntegrity(t, conn)
}

func TestUserCanEnableBotPluginsOnNewInstall(t *testing.T) {
	t.Parallel()
	conn, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	execBotSQL(t, conn, `INSERT INTO bots (id, name, model_provider, model_id, created_at) VALUES ('fixture', 'Fixture', '', '', 1)`)
	for name, source := range botRenamePlugins {
		t.Run(name, func(t *testing.T) {
			if err := plugin.Migrate(t.Context(), conn, name, os.DirFS(filepath.Join("..", "..", "plugins", name))); err != nil {
				t.Fatal(err)
			}
			execBotSQL(t, conn, source.insert(source.newColumn))
			checkBotSQL(t, conn, `SELECT `+source.newColumn+` FROM `+source.table, "fixture")
			checkBotRenameIntegrity(t, conn)
		})
	}
}

var botRenamePlugins = map[string]struct {
	table, oldColumn, newColumn string
	insert                      func(string) string
}{
	"github": {"plugin_github_webhook_settings", "issue_agent_id", "issue_bot_id", func(column string) string {
		return `INSERT INTO plugin_github_webhook_settings (repository_id, secret, ` + column + `) VALUES (1, hex(zeroblob(16)), 'fixture')`
	}},
	"webhook": {"plugin_webhook_sources", "task_agent_id", "task_bot_id", func(column string) string {
		return `INSERT INTO plugin_webhook_sources (id, secret, ` + column + `) VALUES ('source', hex(zeroblob(16)), 'fixture')`
	}},
	"email": {"plugin_email_sources", "task_agent_id", "task_bot_id", func(column string) string {
		return `INSERT INTO plugin_email_sources (id, secret, ` + column + `) VALUES ('source', hex(zeroblob(16)), 'fixture')`
	}},
}

func execBotSQL(t *testing.T, conn *sql.DB, query string) {
	t.Helper()
	if _, err := conn.ExecContext(t.Context(), query); err != nil {
		t.Fatal(err)
	}
}

func checkBotSQL(t *testing.T, conn *sql.DB, query, want string) {
	t.Helper()
	var got string
	if err := conn.QueryRowContext(t.Context(), query).Scan(&got); err != nil || got != want {
		t.Fatalf("%s = %q, want %q: %v", query, got, want, err)
	}
}

func checkBotRenameIntegrity(t *testing.T, conn *sql.DB) {
	t.Helper()
	checkBotSQL(t, conn, `PRAGMA foreign_keys`, "1")
	checkBotSQL(t, conn, `PRAGMA integrity_check`, "ok")
	var table string
	var rowid sql.NullInt64
	var parent string
	var fkid int
	err := conn.QueryRowContext(t.Context(), `PRAGMA foreign_key_check`).Scan(&table, &rowid, &parent, &fkid)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign key check: %s %v %s %d: %v", table, rowid, parent, fkid, err)
	}
}
