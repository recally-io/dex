package db

import (
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestUserAutomationNotificationUpgradeDefaultsOffAndIsAgentScoped(t *testing.T) {
	t.Parallel()
	// Given a routine created before notification routing existed.
	path := filepath.Join(t.TempDir(), "control.sqlite")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := fs.Sub(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, old, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.UpTo(t.Context(), 20261103092655); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO agents (id,name,model_provider,model_id,created_at) VALUES ('a','A','faux','echo',1),('b','B','faux','echo',1)`,
		`INSERT INTO automations (id,agent_id,name,prompt,schedule,time_zone,session,enabled,created_at,updated_at) VALUES ('r','a','R','p','{}','UTC','reuse',1,1,1)`,
	} {
		if _, err = old.ExecContext(t.Context(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err = old.Close(); err != nil {
		t.Fatal(err)
	}
	// When the database is upgraded, existing routines remain off.
	conn, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	var route string
	if err = conn.QueryRowContext(t.Context(), `SELECT notification_route FROM automations WHERE id='r'`).Scan(&route); err != nil {
		t.Fatal(err)
	}
	if route != "none" {
		t.Fatalf("old automation route = %q, want none", route)
	}
	// A target explicitly authorized for one agent cannot be selected by another.
	if _, err = conn.ExecContext(t.Context(), `INSERT INTO automation_notification_targets (id,bot_id,platform,account_id,peer) VALUES ('t','b','feishu','app','feishu:owner')`); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(t.Context(), `UPDATE automations SET notification_route='target', notification_target_id='t' WHERE id='r'`); err == nil {
		t.Fatal("accepted another agent's target")
	}
}

func TestUserRevokingAChannelPermanentlyRejectsPendingNotifications(t *testing.T) {
	t.Parallel()
	// Given an authorized destination with a queued result.
	conn, err := Open(t.Context(), filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	for _, stmt := range []string{
		`INSERT INTO bots (id,name,model_provider,model_id,created_at) VALUES ('a','A','faux','echo',1)`,
		`INSERT INTO telegram_settings (bot_id,token,allowed_users) VALUES ('a','123:secret','42')`,
		`INSERT INTO automation_notification_targets (id,bot_id,platform,account_id,peer) VALUES ('t','a','telegram','123','telegram:42')`,
		`INSERT INTO automations (id,bot_id,name,prompt,schedule,time_zone,session,enabled,created_at,updated_at,notification_route,notification_target_id) VALUES ('r','a','R','p','{}','UTC','reuse',1,1,1,'target','t')`,
		`INSERT INTO sessions (id,bot_id,channel_key,created_at) VALUES (1,'a','automation:r',1)`,
		`INSERT INTO outbox_messages (id,event_id,bot_id,session_id,kind,peer,channel_context,body,created_at) VALUES ('result','run','a',1,'automation','telegram:42','{"accountId":"123"}','text',1)`,
	} {
		if _, err = conn.ExecContext(t.Context(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	// When the owner removes the recipient and later reauthorizes the same ID.
	if _, err = conn.ExecContext(t.Context(), `UPDATE telegram_settings SET allowed_users='' WHERE bot_id='a'`); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(t.Context(), `UPDATE telegram_settings SET allowed_users='42' WHERE bot_id='a'`); err != nil {
		t.Fatal(err)
	}
	// Then the original target is retired and the pending result stays failed.
	var status string
	var targets int
	if err = conn.QueryRowContext(t.Context(), `SELECT status FROM outbox_messages WHERE id='result'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err = conn.QueryRowContext(t.Context(), `SELECT count(*) FROM automation_notification_targets WHERE id='t'`).Scan(&targets); err != nil {
		t.Fatal(err)
	}
	var route string
	if err = conn.QueryRowContext(t.Context(), `SELECT notification_route FROM automations WHERE id='r'`).Scan(&route); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || targets != 0 || route != "none" {
		t.Fatalf("status=%s targets=%d route=%s", status, targets, route)
	}
}
