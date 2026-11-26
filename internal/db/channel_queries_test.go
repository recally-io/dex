package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserChannelQueuesDoNotReadWebHistory(t *testing.T) {
	// Given main history with no channel reply or pending input.
	conn, err := Open(t.Context(), filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	for _, stmt := range []string{
		`INSERT INTO agents (id,name,model_provider,model_id,created_at) VALUES ('default','Dex','faux','echo',1),('other','Other','faux','echo',1)`,
		`INSERT INTO conversations (id,agent_id,channel_key,created_at) VALUES (1,'default','main',1),(2,'other','main',1)`,
		`WITH RECURSIVE history(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM history WHERE n<10000)
		INSERT INTO submissions (id,conversation_id,content,status,answer,created_at,origin)
		SELECT 'history-'||n,1,'old input','done','old answer',1,
		CASE WHEN n%3=0 THEN 'telegram:7' ELSE 'web' END FROM history`,
	} {
		if _, err := conn.ExecContext(t.Context(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	var version string
	if err := conn.QueryRowContext(t.Context(), "SELECT sqlite_version()").Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Logf("artifact: modernc.org/sqlite engine=%s; main history=10000", version)
	q := sqlc.New(conn)
	if count, err := q.CountChannelPending(t.Context(), sqlc.CountChannelPendingParams{AgentID: "default", Peer: "wechat:owner"}); err != nil || count != 0 {
		t.Fatalf("empty pending queue: %d, %v", count, err)
	}

	// When SQLite plans the query, it uses the pending index for the exact origin.
	rows, err := conn.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+channelQuery(t, "CountChannelPending"), sql.Named("agent_id", "default"), sql.Named("peer", "wechat:owner"))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(plan, func(detail string) bool {
		return strings.HasPrefix(detail, "SEARCH s ") && strings.Contains(detail, "submissions_channel_pending (origin=")
	}) {
		t.Fatalf("pending query reads history without an origin index: %q", plan)
	}

	// Then the pending count includes only the exact peer and agent.
	if _, err := conn.ExecContext(t.Context(), `INSERT INTO submissions
		(id,conversation_id,content,status,created_at,origin,kind) VALUES
		('pending',1,'wait','pending',1,'telegram:7','message'),
		('near-peer',1,'wait','pending',1,'telegram:70','message'),
		('other-pending',2,'wait','pending',1,'telegram:7','message'),
		('wechat-pending',1,'wait','pending',1,'wechat:owner','message')`); err != nil {
		t.Fatal(err)
	}
	count, err := q.CountChannelPending(t.Context(), sqlc.CountChannelPendingParams{AgentID: "default", Peer: "telegram:7"})
	if err != nil || count != 1 {
		t.Fatalf("exact peer pending: %d, %v", count, err)
	}
}

func TestUserTelegramPendingResetDoesNotHoldFollowingReply(t *testing.T) {
	// Given an earlier pending input and a later reply to the same peer.
	ctx := t.Context()
	conn, err := Open(ctx, filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.ExecContext(ctx, `
INSERT INTO agents (id, name, model_provider, model_id, created_at) VALUES ('bot', 'Bot', '', '', 1);
INSERT INTO telegram_settings (agent_id, token, allowed_users) VALUES ('bot', '123:fixture', '42');
INSERT INTO conversations (id, agent_id, channel_key, created_at) VALUES (1, 'bot', 'main', 1);
INSERT INTO submissions (id, conversation_id, content, status, origin, created_at) VALUES
  ('earlier', 1, 'input', 'pending', 'telegram:42', 1),
  ('later', 1, 'input', 'done', 'telegram:42', 2);
INSERT INTO outbox_messages (id, event_id, agent_id, conversation_id, source_submission_id, kind, peer, channel_context, body, created_at)
VALUES ('reply', 'reply:later', 'bot', 1, 'later', 'reply', 'telegram:42', '{"accountId":"123","senderId":"42"}', 'answer', 2);`); err != nil {
		t.Fatal(err)
	}
	q := sqlc.New(conn)
	params := sqlc.ListPendingOutboxMessagesParams{AgentID: "bot", Channel: sql.NullString{String: "telegram", Valid: true}}
	if replies, err := q.ListPendingOutboxMessages(ctx, params); err != nil || len(replies) != 0 {
		t.Fatalf("replies = %+v, want the pending input to hold the reply: %v", replies, err)
	}

	// When the earlier input is a reset, the reply is ready even if the reset is pending.
	if _, err := conn.ExecContext(ctx, "UPDATE submissions SET kind = 'context_reset' WHERE id = 'earlier'"); err != nil {
		t.Fatal(err)
	}
	if replies, err := q.ListPendingOutboxMessages(ctx, params); err != nil || len(replies) != 1 || replies[0].ID != "reply" {
		t.Fatalf("replies = %+v, want the later reply: %v", replies, err)
	}
}

func channelQuery(t *testing.T, name string) string {
	t.Helper()
	source, err := os.ReadFile("queries/channels.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, found := strings.Cut(string(source), "-- name: "+name+" :")
	if !found {
		t.Fatalf("query %s not found", name)
	}
	_, rest, _ = strings.Cut(rest, "\n")
	query, _, _ := strings.Cut(rest, "-- name:")
	return strings.TrimSpace(query)
}
