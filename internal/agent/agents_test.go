package agent_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/recally-io/dex/internal/agent"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserSavesSettingsForEachAgent(t *testing.T) {
	// Given two agents with no model, which need no worker configuration.
	conn, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)
	s := agent.New(q, nil, "", nil)
	for _, name := range []string{"First", "Second"} {
		if _, err := s.CreateAgent(t.Context(), agent.Agent{Name: name, ThinkingLevel: "off"}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := q.ListAgents(t.Context())
	if err != nil || len(rows) != 2 {
		t.Fatalf("agents=%+v err=%v", rows, err)
	}
	// When the owner changes the second agent.
	if err := s.SaveAgent(t.Context(), rows[1].ID, agent.Agent{Name: " Changed ", ThinkingLevel: "high", Instructions: "Second only"}); err != nil {
		t.Fatal(err)
	}
	// Then the first agent keeps its settings.
	first, err := s.Agent(t.Context(), rows[0].ID)
	if err != nil || first.Name != "First" || first.Instructions != "" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := s.Agent(t.Context(), rows[1].ID)
	if err != nil || second.Name != "Changed" || second.Instructions != "Second only" || second.ThinkingLevel != "high" {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if _, err := s.CreateAgent(t.Context(), agent.Agent{Name: " ", ThinkingLevel: "off"}); !errors.Is(err, agent.ErrInvalid) {
		t.Fatalf("invalid name: %v", err)
	}
	listed, err := s.ListAgents(t.Context())
	if err != nil || len(listed) != 2 {
		t.Fatalf("list=%+v err=%v", listed, err)
	}
	t.Logf("artifact: agent IDs=%s,%s; second=%+v", rows[0].ID, rows[1].ID, second)
}
