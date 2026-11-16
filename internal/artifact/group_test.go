package artifact_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/recally-io/dex/internal/artifact"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserGroupSharingCannotBorrowBotArtifactDirectory(t *testing.T) {
	// Given a private file under the same actual bot identity as a group runtime.
	data := t.TempDir()
	workspace := filepath.Join(data, "agents", "default", "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "private.txt"), []byte("PRIVATE_ARTIFACT_CANARY"), 0600); err != nil {
		t.Fatal(err)
	}
	service := artifact.New(data)
	// When a scoped caller reaches the private sharing handler directly.
	_, err := service.Deliver(t.Context(), engine.TaskContext{AgentID: "default", Scope: "tg_123_-100_7"}, "private.txt")
	// Then no private bytes enter a public delivery or even the bot-wide artifact directory.
	if err == nil {
		t.Fatal("group delivery fell back to the private bot workspace and artifact directory")
	}
	rows, err := service.List("default")
	if err != nil || len(rows) != 0 {
		t.Fatalf("private copy created before denial: rows=%v err=%v", rows, err)
	}
}
