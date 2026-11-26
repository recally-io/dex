package db_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserKeepsDeliveryAndRunnerDataAcrossCleanup(t *testing.T) {
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
	if _, err := provider.UpTo(ctx, 20261126090719); err != nil {
		t.Fatal(err)
	}

	// Given a Bot on a saved SSH Runner, a delivered reply, and queued channel work.
	if _, err := conn.ExecContext(ctx, `
INSERT INTO runners (id, name, kind, config, created_at, updated_at) VALUES ('remote', 'Remote', 'pi-env', '{"host":"remote"}', 1, 1);
INSERT INTO agents (id, name, model_provider, model_id, created_at, runner_id) VALUES ('bot', 'Bot', '', '', 1, 'remote');
INSERT INTO conversations (id, agent_id, channel_key, created_at) VALUES (1, 'bot', 'main', 1);
INSERT INTO telegram_settings (agent_id, token, allowed_users) VALUES ('bot', '123:fixture', '42');
INSERT INTO submissions (id, conversation_id, content, status, origin, created_at, delivered_at)
VALUES ('input', 1, 'input', 'done', 'telegram:42', 1, 2);
INSERT INTO outbox_messages (id, event_id, agent_id, conversation_id, source_submission_id, kind, peer, channel_context, topic_key, status, created_at, delivered_at)
VALUES ('input', 'reply:bot:input', 'bot', 1, 'input', 'reply', 'telegram:42', '{"accountId":"123"}', '', 'sent', 1, 2),
       ('routine:result', 'remind:result', 'bot', 1, NULL, 'automation', 'telegram:42', '{"accountId":"123"}', 'routine:watch', 'pending', 3, NULL);
INSERT INTO channel_webhook_inbox (platform, account, event_id, agent_id, received_at, payload)
VALUES ('telegram', '123', 'event', 'bot', 4, X'0102');`); err != nil {
		t.Fatal(err)
	}

	// When Dex upgrades, rolls back, and upgrades again, the saved work remains usable.
	for _, phase := range []struct {
		name, runnerKind, queue, topic string
		up                             bool
	}{
		{"up", "ssh", "channel_webhook_queue", "automation:watch", true},
		{"down", "pi-env", "channel_webhook_inbox", "routine:watch", false},
		{"up again", "ssh", "channel_webhook_queue", "automation:watch", true},
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
			check(`SELECT count(*) FROM pragma_foreign_key_check`, "0")
			check(`SELECT kind || ':' || json_extract(config, '$.host') FROM runners JOIN agents ON runners.id = agents.runner_id`, phase.runnerKind+":remote")
			check(`SELECT hex(payload) FROM `+phase.queue+` WHERE event_id = 'event'`, "0102")
			check(`SELECT status || ':' || topic_key FROM outbox_messages WHERE id = 'routine:result' AND event_id = 'remind:result'`, "pending:"+phase.topic)
			check(`SELECT status || ':' || delivered_at FROM outbox_messages WHERE id = 'input'`, "sent:2")
			if phase.up {
				check(`SELECT count(*) FROM pragma_table_info('submissions') WHERE name = 'delivered_at'`, "0")
			} else {
				check(`SELECT delivered_at FROM submissions WHERE id = 'input'`, "2")
			}
		})
	}

	// Then the delivery wake and superseding still find the pending message from before the upgrade.
	q := sqlc.New(conn)
	if peer, err := q.GetPendingAutomationOutboxPeer(ctx, "remind:result"); err != nil || peer != "telegram:42" {
		t.Fatalf("pending Automation peer = %q: %v", peer, err)
	}
	if n, err := q.SupersedeOutboxTopic(ctx, sqlc.SupersedeOutboxTopicParams{ConversationID: 1, Peer: "telegram:42", TopicKey: "automation:watch"}); err != nil || n != 1 {
		t.Fatalf("superseded pending messages = %d, want 1: %v", n, err)
	}
}
