package db_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserUpgradesSharedRunnerWithoutMovingExistingWorkspace(t *testing.T) {
	// Given a v0.19.0 database with an active bot and an archived bot on one runner.
	ctx := t.Context()
	file := filepath.Join(t.TempDir(), "control.sqlite")
	old, err := sql.Open("sqlite", "file:"+file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = old.Close() }()
	provider, err := goose.NewProvider(goose.DialectSQLite3, old, migrationFS(t, func(name string) bool {
		return name < "20261121180000_runner_workspaces.sql"
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO execution_environments (id, name, kind, created_at, updated_at) VALUES ('machine', 'Machine', 'runner', 1, 1)`,
		`INSERT INTO agents (id, name, model_provider, model_id, created_at, environment_id, archived_at) VALUES
		('active', 'Active', 'faux', 'echo', 1, 'machine', NULL), ('archived', 'Archived', 'faux', 'echo', 2, 'machine', 3)`,
	} {
		if _, err := old.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	// When Dex upgrades and the user restores the archived bot to the same runner.
	conn, err := db.Open(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `UPDATE agents SET archived_at = NULL WHERE id = 'archived'`); err != nil {
		t.Fatalf("restore second bot: %v", err)
	}

	// Then only the bot that previously owned the workspace keeps it.
	q := sqlc.New(conn)
	for agentID, want := range map[string]bool{"active": true, "archived": false} {
		legacy, err := q.UsesLegacyRunnerWorkspace(ctx, sqlc.UsesLegacyRunnerWorkspaceParams{EnvironmentID: "machine", AgentID: agentID})
		if err != nil || legacy != want {
			t.Fatalf("legacy workspace for %s = %t, want %t (err %v)", agentID, legacy, want, err)
		}
	}
}
