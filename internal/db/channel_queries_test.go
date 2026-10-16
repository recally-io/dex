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
		INSERT INTO submissions (id,conversation_id,content,status,answer,created_at,origin,delivered_at)
		SELECT 'history-'||n,1,'old input','done','old answer',1,
		CASE WHEN n%3=0 THEN 'telegram:7' ELSE 'web' END,
		CASE WHEN n%3=0 THEN 2 ELSE NULL END FROM history`,
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
	replyParams := sqlc.ListChannelRepliesParams{AgentID: "default", OriginStart: "telegram:", OriginEnd: "telegram;"}
	if replies, err := q.ListChannelReplies(t.Context(), replyParams); err != nil || len(replies) != 0 {
		t.Fatalf("empty reply queue: %+v, %v", replies, err)
	}
	if count, err := q.CountChannelPending(t.Context(), sqlc.CountChannelPendingParams{AgentID: "default", Peer: "wechat:owner"}); err != nil || count != 0 {
		t.Fatalf("empty pending queue: %d, %v", count, err)
	}

	// When the project driver plans the actual queries, each uses its origin index.
	for _, tc := range []struct {
		name, index string
		args        []any
	}{
		{"ListChannelReplies", "submissions_channel_replies", []any{sql.Named("agent_id", "default"), sql.Named("origin_start", "telegram:"), sql.Named("origin_end", "telegram;")}},
		{"CountChannelPending", "submissions_channel_pending", []any{sql.Named("agent_id", "default"), sql.Named("peer", "wechat:owner")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := conn.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+channelQuery(t, tc.name), tc.args...)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var plan []string
			usesOrigin := false
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				plan = append(plan, detail)
				if strings.Contains(detail, "SEARCH s ") && strings.Contains(detail, tc.index) && strings.Contains(detail, "origin") {
					usesOrigin = true
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			t.Logf("artifact: %s empty queue plan=%q", tc.name, plan)
			if !usesOrigin {
				t.Fatalf("empty channel queue reads history without an origin index: %q", plan)
			}
		})
	}

	// Then filtering keeps exact peers, agent ownership, and rowid send order.
	if _, err := conn.ExecContext(t.Context(), `INSERT INTO submissions
		(id,conversation_id,content,status,created_at,origin,kind,delivered_at) VALUES
		('z-first',1,'first','done',30,'telegram:9','message',NULL),
		('a-second',1,'second','done',10,'telegram:7','task_result',NULL),
		('unanswered',1,'third','unanswered',20,'telegram:7','message',NULL),
		('failed-result',1,'skip','unanswered',1,'telegram:7','task_result',NULL),
		('reset',1,'skip','done',1,'telegram:7','context_reset',NULL),
		('delivered',1,'skip','done',1,'telegram:7','message',2),
		('near-prefix',1,'skip','done',1,'telegramx:7','message',NULL),
		('other-agent',2,'skip','done',1,'telegram:7','message',NULL),
		('pending',1,'wait','pending',1,'telegram:7','message',NULL),
		('near-peer',1,'wait','pending',1,'telegram:70','message',NULL),
		('other-pending',2,'wait','pending',1,'telegram:7','message',NULL),
		('wechat-pending',1,'wait','pending',1,'wechat:owner','message',NULL)`); err != nil {
		t.Fatal(err)
	}
	replies, err := q.ListChannelReplies(t.Context(), replyParams)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, reply := range replies {
		ids = append(ids, reply.ID)
	}
	if !slices.Equal(ids, []string{"z-first", "a-second", "unanswered"}) {
		t.Fatalf("reply order or filter changed: %q", ids)
	}
	count, err := q.CountChannelPending(t.Context(), sqlc.CountChannelPendingParams{AgentID: "default", Peer: "telegram:7"})
	if err != nil || count != 1 {
		t.Fatalf("exact peer pending: %d, %v", count, err)
	}
	t.Logf("artifact: replies=%q; telegram:7 pending=%d", ids, count)
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
