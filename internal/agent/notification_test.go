package agent_test

import (
	"path/filepath"
	"testing"

	"github.com/recally-io/dex/internal/agent"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserCanSelectOnlyTheirAuthorizedChannelTarget(t *testing.T) {
	// Given two agents, only one with a paired Feishu owner.
	conn, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)
	for _, id := range []string{"a", "b"} {
		if err = q.CreateAgentIfMissing(t.Context(), sqlc.CreateAgentIfMissingParams{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = conn.ExecContext(t.Context(), `INSERT INTO feishu_settings (agent_id, app_id, app_secret, brand, owner_id) VALUES ('a','app','test-secret','feishu','owner')`); err != nil {
		t.Fatal(err)
	}
	s := agent.New(q, nil, "", nil)
	// When the user lists eligible targets and selects one as primary.
	list, err := s.NotificationTargets(t.Context(), "a")
	if err != nil || len(list.Targets) != 1 {
		t.Fatalf("targets=%+v err=%v", list, err)
	}
	id := list.Targets[0].ID
	if err = s.SetPrimaryNotificationTarget(t.Context(), "a", id); err != nil {
		t.Fatal(err)
	}
	// Then another agent cannot use that target and the owner sees it as primary.
	if err = s.SetPrimaryNotificationTarget(t.Context(), "b", id); err == nil {
		t.Fatal("accepted another agent's target")
	}
	list, err = s.NotificationTargets(t.Context(), "a")
	if err != nil || list.PrimaryID != id {
		t.Fatalf("primary=%+v err=%v", list, err)
	}
}
