package db

import (
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestUserKeepsAutomationAndRunnerDataAcrossMigration(t *testing.T) {
	ctx := t.Context()
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "control.sqlite")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	dir, err := fs.Sub(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, conn, dir)
	if err != nil {
		t.Fatal(err)
	}

	// Given an existing Runner, its bindings, and pending channel deliveries.
	if _, err = p.UpTo(ctx, 20261126005655); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO execution_environments (id,name,kind,secret,created_at,updated_at) VALUES ('machine','Machine','runner','saved',1,2)`,
		`INSERT INTO runner_pairing (environment_id,credential_digest) VALUES ('machine','paired')`,
		`INSERT INTO agents (id,name,model_provider,model_id,created_at,environment_id) VALUES ('a','A','faux','echo',1,'machine')`,
		`INSERT INTO telegram_settings (agent_id,token,allowed_users) VALUES ('a','123:secret','42')`,
		`INSERT INTO routine_notification_targets (id,agent_id,platform,account_id,peer) VALUES ('t','a','telegram','123','telegram:42')`,
		`UPDATE agents SET primary_notification_target_id='t' WHERE id='a'`,
		`INSERT INTO conversations (id,agent_id,channel_key,created_at,environment_id) VALUES (1,'a','web:1',1,'machine')`,
		`INSERT INTO submissions (id,conversation_id,content,status,created_at) VALUES ('s',1,'input','pending',1)`,
		`INSERT INTO automations (id,agent_id,name,prompt,schedule,time_zone,enabled,created_at,updated_at,environment_id,notification_route,notification_target_id) VALUES ('auto','a','A','check','{}','UTC',1,1,1,'machine','target','t')`,
		`INSERT INTO automation_runs (id,automation_id,status,created_at,environment_id) VALUES ('run','auto','started',1,'machine')`,
		`INSERT INTO routine_tool_operations (key,agent_id,conversation_id,source_request_id,action,result) VALUES ('op','a',1,'s','create','{}')`,
		`INSERT INTO routine_notification_deliveries (run_id,agent_id,target_id,platform,account_id,peer,body) VALUES ('run','a','t','telegram','123','telegram:42','old result')`,
		`INSERT INTO monitors (id,operation_key,agent_id,conversation_id,root_request_id,source_submission_id,chain_depth,due_at,prompt,created_at,updated_at,target_environment,target_environment_revision) VALUES ('m','check','a',1,'s','s',0,2,'monitor',1,1,'machine',2)`,
		`INSERT INTO outbox_messages (id,event_id,agent_id,conversation_id,source_submission_id,kind,peer,channel_context,body,retry_at,attempts,created_at) VALUES ('o','event','a',1,'s','routine','telegram:42','{"accountId":"123"}','result',42,3,1), ('f','file','a',1,'s','file','telegram:42','{"accountId":"123"}','',0,0,1)`,
		`INSERT INTO outbox_files (id,agent_id,operation_key,submission_id,outbox_id,name,mime_type,body) VALUES ('f','a','file-op','s','f','result.txt','text/plain',X'616263')`,
	} {
		if _, err = conn.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	for _, step := range []struct {
		name string
		up   bool
	}{{"up", true}, {"down", false}, {"up again", true}} {
		t.Run(step.name, func(t *testing.T) {
			// When the database changes version, then the data and constraints stay intact.
			runners, runnerID, monitorID, monitorRev, prefix, kind := "runners", "runner_id", "target_runner_id", "target_runner_revision", "automation_", "automation"
			if step.up {
				_, err = p.UpByOne(ctx)
			} else {
				_, err = p.Down(ctx)
				runners, runnerID, monitorID, monitorRev, prefix, kind = "execution_environments", "environment_id", "target_environment", "target_environment_revision", "routine_", "routine"
			}
			if err != nil {
				t.Fatal(err)
			}
			if step.up {
				var old int
				if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name GLOB 'execution_environments*' OR name GLOB 'routine_*' OR name GLOB 'sqlite_autoindex_execution_environments_*' OR name GLOB 'sqlite_autoindex_routine_*'`).Scan(&old); err != nil || old != 0 {
					t.Fatalf("retired objects = %d, err = %v", old, err)
				}
			}
			for _, query := range []string{
				`SELECT count(*) FROM ` + runners + ` WHERE id='machine' AND kind='runner' AND secret='saved'`,
				`SELECT count(*) FROM runner_pairing WHERE ` + runnerID + `='machine' AND credential_digest='paired'`,
				`SELECT count(*) FROM agents WHERE id='a' AND ` + runnerID + `='machine' AND primary_notification_target_id='t'`,
				`SELECT count(*) FROM conversations WHERE id=1 AND ` + runnerID + `='machine'`,
				`SELECT count(*) FROM automations WHERE id='auto' AND ` + runnerID + `='machine' AND notification_target_id='t'`,
				`SELECT count(*) FROM automation_runs WHERE id='run' AND ` + runnerID + `='machine'`,
				`SELECT count(*) FROM monitors WHERE id='m' AND ` + monitorID + `='machine' AND ` + monitorRev + `=2`,
				`SELECT count(*) FROM ` + prefix + `notification_targets WHERE id='t' AND peer='telegram:42'`,
				`SELECT count(*) FROM ` + prefix + `notification_deliveries WHERE run_id='run' AND body='old result'`,
				`SELECT count(*) FROM ` + prefix + `tool_operations WHERE key='op' AND source_request_id='s'`,
				`SELECT count(*) FROM outbox_messages WHERE id='o' AND kind='` + kind + `' AND body='result' AND status='pending' AND retry_at=42 AND attempts=3 AND channel='telegram' AND target='42'`,
				`SELECT count(*) FROM outbox_files JOIN outbox_messages ON outbox_id=outbox_messages.id WHERE outbox_id='f' AND kind='file' AND outbox_files.body=X'616263'`,
			} {
				var count int
				if err := conn.QueryRowContext(ctx, query).Scan(&count); err != nil || count != 1 {
					t.Errorf("%s: count = %d, err = %v", query, count, err)
				}
			}
			var violations int
			if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
				t.Fatalf("foreign key violations = %d, err = %v", violations, err)
			}
		})
	}
}
