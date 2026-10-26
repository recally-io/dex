//go:build agenteval

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserEvalCopiesOnlyProvidersWithoutChangingSource(t *testing.T) {
	// Given a control database with a provider and a private agent.
	ctx := t.Context()
	source := filepath.Join(t.TempDir(), "control.sqlite")
	conn, err := db.Open(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	q := sqlc.New(conn)
	if err = q.UpsertProvider(ctx, sqlc.UpsertProviderParams{ID: "eval", Name: "Eval", Api: "openai-completions", ApiKey: "test-only-key", Models: "[]", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err = q.CreateAgentIfMissing(ctx, sqlc.CreateAgentIfMissingParams{ID: "private", Name: "Private", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err = conn.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}

	// When the eval copies providers into a fresh control database.
	rows, err := providers(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "control.sqlite")
	if err = seed(ctx, target, rows); err != nil {
		t.Fatal(err)
	}

	// Then the source bytes stay unchanged and no agent is copied.
	after, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("source database changed")
	}
	dest, err := db.Open(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	defer dest.Close()
	copied, err := sqlc.New(dest).ListProviders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(copied) != 1 || copied[0].ID != "eval" || copied[0].ApiKey != "test-only-key" {
		t.Fatal("provider row was not copied")
	}
	agents, err := sqlc.New(dest).ListAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 0 {
		t.Fatal("private agents were copied")
	}
}

func TestUserEvalArtifactsRemoveProviderKeys(t *testing.T) {
	// Given a key with characters that JSON must escape.
	secret := "test-key-\"\\\n"
	path := filepath.Join(t.TempDir(), "results.json")
	// When a transcript contains that key.
	if err := save(path, map[string]string{"toolOutput": "prefix " + secret + " suffix"}, []string{secret}); err != nil {
		t.Fatal(err)
	}
	// Then the artifact contains the redaction marker and no key.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("[REDACTED]")) || bytes.Contains(data, []byte("test-key-")) {
		t.Fatal("provider key remained in artifact")
	}
}
