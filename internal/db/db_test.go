package db

import (
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestMigrationKeepsChatsOnTheAgentSettings(t *testing.T) {
	// Given a database of the schema before chat settings, with a chat and a message
	path := filepath.Join(t.TempDir(), "control.sqlite")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := fs.Sub(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, old, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(t.Context(), 7); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO agents (id, name, model_provider, model_id, created_at) VALUES ('default', 'Dex', 'faux', 'echo', 1)`,
		`INSERT INTO conversations (id, agent_id, channel_key, created_at) VALUES (1, 'default', 'web:x', 1)`,
		`INSERT INTO submissions (id, conversation_id, content, status, created_at) VALUES ('s1', 1, 'hi', 'pending', 1)`,
	} {
		if _, err := old.ExecContext(t.Context(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	// When dex opens it
	db, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Then the agent thinks "off", as before, the chat overrides nothing, and the message has no files
	var level, provider2, model, chatLevel, attachments string
	err = db.QueryRowContext(t.Context(), `SELECT a.thinking_level, c.model_provider, c.model_id, c.thinking_level, s.attachments
		FROM agents a, conversations c, submissions s`).Scan(&level, &provider2, &model, &chatLevel, &attachments)
	if err != nil {
		t.Fatal(err)
	}
	if level != "off" || provider2 != "" || model != "" || chatLevel != "" || attachments != "[]" {
		t.Fatalf("got %q %q %q %q %q", level, provider2, model, chatLevel, attachments)
	}

	// And a level that pi-ai does not have is refused
	if _, err := db.ExecContext(t.Context(), `UPDATE conversations SET thinking_level = 'huge'`); err == nil {
		t.Fatal("the database stored an unknown thinking level")
	}
}

func TestUserUpgradeDoesNotResendOldChannelAnswers(t *testing.T) {
	// Given version 9 with a settled Telegram answer and a pending message.
	path := filepath.Join(t.TempDir(), "control.sqlite")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := fs.Sub(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, old, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.UpTo(t.Context(), 9); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO agents (id,name,model_provider,model_id,created_at) VALUES ('default','Dex','faux','echo',1)`,
		`INSERT INTO conversations (id,agent_id,channel_key,created_at) VALUES (1,'default','telegram:7',1)`,
		`INSERT INTO submissions (id,conversation_id,content,status,answer,created_at,settled_at) VALUES ('old',1,'hi','done','echo: hi',1,2)`,
		`INSERT INTO submissions (id,conversation_id,content,status,created_at) VALUES ('pending',1,'later','pending',3)`,
	} {
		if _, err = old.ExecContext(t.Context(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err = old.Close(); err != nil {
		t.Fatal(err)
	}
	// When the current version upgrades the database.
	conn, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	// Then the old answer is excluded from redelivery, but pending work remains pending.
	var delivered sql.NullInt64
	if err = conn.QueryRowContext(t.Context(), "SELECT delivered_at FROM submissions WHERE id='old'").Scan(&delivered); err != nil {
		t.Fatal(err)
	}
	if !delivered.Valid || delivered.Int64 != 2 {
		t.Fatalf("old delivery: %+v", delivered)
	}
	var status string
	if err = conn.QueryRowContext(t.Context(), "SELECT status FROM submissions WHERE id='pending'").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatal(status)
	}
}
