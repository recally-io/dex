package db_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserKeepsQuotedQuestionsAfterAgentScopeUpgrade(t *testing.T) {
	// Given a database with a question mapping from before agent-scoped remote IDs.
	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	oldSchema := fstest.MapFS{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() >= "20261110142827_" {
			continue
		}
		content, err := os.ReadFile(filepath.Join("migrations", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		oldSchema[entry.Name()] = &fstest.MapFile{Data: content}
	}
	path := filepath.Join(t.TempDir(), "control.sqlite")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = old.Close() })
	migrations, err := goose.NewProvider(goose.DialectSQLite3, old, oldSchema)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO agents (id, name, model_provider, model_id, created_at) VALUES ('first', 'First', 'faux', 'echo', 1), ('second', 'Second', 'faux', 'echo', 1)`,
		`INSERT INTO conversations (id, agent_id, channel_key, created_at) VALUES (1, 'first', 'main', 1), (2, 'second', 'main', 1)`,
		`INSERT INTO questions (id, agent_id, conversation_id, source_submission_id, tool_operation_key, text, options_json, status, created_at) VALUES ('q1', 'first', 1, 's1', 'first/1', 'First question?', '["Yes","No"]', 'open', 1), ('q2', 'second', 2, 's2', 'second/1', 'Second question?', '["Yes","No"]', 'open', 1)`,
		`INSERT INTO question_messages (peer, remote_id, question_id) VALUES ('telegram:7', '7001', 'q1')`,
	} {
		if _, err := old.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	// When startup applies the new migration and another bot records the same remote ID.
	conn, err := db.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)
	if err := q.AddQuestionMessage(t.Context(), sqlc.AddQuestionMessageParams{AgentID: "second", Peer: "telegram:7", RemoteID: "7001", QuestionID: "q2"}); err != nil {
		t.Fatal(err)
	}

	// Then both the old and the new quote resolve to the question of the correct bot.
	for agentID, want := range map[string]string{"first": "q1", "second": "q2"} {
		got, err := q.GetQuestionByMessage(t.Context(), sqlc.GetQuestionByMessageParams{AgentID: agentID, Peer: "telegram:7", RemoteID: "7001"})
		if err != nil || got != want {
			t.Fatalf("agent %s: got %q, want %q: %v", agentID, got, want, err)
		}
	}
}
