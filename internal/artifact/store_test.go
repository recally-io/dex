package artifact_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/recally-io/dex/internal/artifact"
)

func TestUserReceivesWorkspaceSnapshot(t *testing.T) {
	// Given a report in an agent's workspace.
	agent := t.TempDir()
	workspace := filepath.Join(agent, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "report.md"), []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	// When the agent explicitly delivers the report and then changes the source.
	a, err := artifact.Snapshot(agent, "report.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "report.md"), []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	// Then the delivered file retains the original content.
	f, err := artifact.Open(agent, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b := make([]byte, 5)
	if _, err := f.Read(b); err != nil {
		t.Fatal(err)
	}
	if string(b) != "first" || a.Name != "report.md" {
		t.Fatalf("snapshot = %q, name %q", b, a.Name)
	}
}

func TestUserCannotDeliverOutsideWorkspace(t *testing.T) {
	agent := t.TempDir()
	workspace := filepath.Join(agent, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(workspace, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../secret", filepath.Join(outside, "secret"), "link", "escape/secret"} {
		t.Run(path, func(t *testing.T) {
			if _, err := artifact.Snapshot(agent, path); err == nil {
				t.Fatalf("delivered %q", path)
			}
		})
	}
}

func TestUserCannotDeliverAnOversizedFile(t *testing.T) {
	// Given a sparse source file larger than the delivery limit.
	agent := t.TempDir()
	workspace := filepath.Join(agent, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	large := filepath.Join(workspace, "large.bin")
	if err := os.WriteFile(large, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(large, artifact.MaxSize+1); err != nil {
		t.Fatal(err)
	}
	// When the agent attempts to deliver it.
	_, err := artifact.Snapshot(agent, "large.bin")
	// Then no snapshot is created.
	if err == nil {
		t.Fatal("oversized file delivered")
	}
	if _, statErr := os.Stat(filepath.Join(agent, "artifacts")); !os.IsNotExist(statErr) {
		t.Fatalf("artifacts directory = %v, want absent", statErr)
	}
}
