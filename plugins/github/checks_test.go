package github

import (
	"path/filepath"
	"testing"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/plugin"
	tasksvc "github.com/recally-io/dex/internal/task"
)

func TestUserCICannotResolveAWait(t *testing.T) {
	ctx := t.Context()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := plugin.Migrate(ctx, conn, "github", Migrations); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlc.New(conn).CreateAgent(ctx, sqlc.CreateAgentParams{ID: "default", Name: "Dex", ThinkingLevel: "off"}); err != nil {
		t.Fatal(err)
	}
	store := tasksvc.New(conn, nil)
	task, err := store.CreateTask(ctx, tasksvc.Owner, tasksvc.OriginUser, tasksvc.CreateTaskParams{AssigneeBotID: "default", Title: "Ship"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.LinkObject(ctx, "default", task.ID, Namespace(10), PullRequest(2)); err != nil {
		t.Fatal(err)
	}
	// Even a wait for this exact event type stays open, because CI is record-only.
	if err := store.AddWait(ctx, "default", task.ID, tasksvc.Wait{Namespace: Namespace(10), Type: CheckSuiteCompleted, Object: PullRequest(2), AfterAt: 1, Alert: true}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"action":"completed","repository":{"id":10},"check_suite":{"id":5,"head_sha":"abc","conclusion":"success","updated_at":"2026-11-21T03:00:00Z","pull_requests":[{"number":2,"base":{"repo":{"id":10}}}]}}`)
	receipt, err := acceptCheckSuite(ctx, store, 10, "webhook:1", body)
	if err != nil || receipt == nil || receipt.TaskID != task.ID || receipt.ResolvedWaits != 0 || receipt.Alerted {
		t.Fatalf("CI must only add a timeline entry: %+v, %v", receipt, err)
	}
}
