package db

import (
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestUserAutomationUpgradeDoesNotInventSuccessfulReports(t *testing.T) {
	// Given old completed, orphaned, pending, and skipped runs without reports.
	path := filepath.Join(t.TempDir(), "control.sqlite")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	dir, err := fs.Sub(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, old, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.UpTo(t.Context(), 20261026040647); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO agents (id,name,model_provider,model_id,created_at) VALUES ('default','Dex','faux','echo',1)`,
		`INSERT INTO conversations (id,agent_id,channel_key,created_at) VALUES (1,'default','automation:routine',1)`,
		`INSERT INTO automations (id,agent_id,name,prompt,schedule,time_zone,session,enabled,created_at,updated_at) VALUES ('routine','default','Check','check','{}','UTC','reuse',1,1,1)`,
		`INSERT INTO automation_runs (id,automation_id,status,created_at) VALUES ('done','routine','started',1),('orphan','routine','started',2),('pending','routine','started',3),('skipped','routine','skipped',4)`,
		`INSERT INTO submissions (id,conversation_id,content,status,created_at,settled_at) VALUES ('done',1,'check','done',1,2),('pending',1,'check','pending',3,NULL)`,
	} {
		if _, err = old.ExecContext(t.Context(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err = old.Close(); err != nil {
		t.Fatal(err)
	}
	// When normal startup upgrades the database.
	conn, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Then old terminal runs fail explicitly; pending and skipped runs remain distinct.
	for _, check := range []struct{ id, status, outcome string }{{"done", "failed", "failed"}, {"orphan", "failed", "failed"}, {"pending", "started", ""}, {"skipped", "skipped", ""}} {
		var status, outcome, summary string
		if err = conn.QueryRowContext(t.Context(), "SELECT status,outcome,summary FROM automation_runs WHERE id=?", check.id).Scan(&status, &outcome, &summary); err != nil {
			t.Fatal(err)
		}
		if status != check.status || outcome != check.outcome || (outcome == "failed" && summary == "") {
			t.Fatalf("%s: status=%q outcome=%q summary=%q", check.id, status, outcome, summary)
		}
	}
	t.Log("artifact: completed and orphaned runs failed; pending and skipped runs retained")
}
