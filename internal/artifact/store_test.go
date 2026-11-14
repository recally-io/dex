package artifact_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/recally-io/dex/internal/artifact"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserKeepsEachBotsDirectorySeparate(t *testing.T) {
	data := t.TempDir()
	service := artifact.New(data)
	for _, bot := range []string{"a", "b"} {
		directory := filepath.Join(data, "agents", bot, "artifacts")
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "report.md"), []byte(bot), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.Rename("a", "report.md", "renamed.md"); err != nil {
		t.Fatal(err)
	}
	if rows, err := service.List("b"); err != nil || len(rows) != 1 || rows[0].Name != "report.md" {
		t.Fatalf("other bot files=%+v err=%v", rows, err)
	}
	if err := service.Delete("a", "renamed.md"); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(data, "agents", "b", "artifacts", "report.md")); err != nil || string(body) != "b" {
		t.Fatalf("other bot file=%q err=%v", body, err)
	}
}

func TestUserCannotEscapeTheBotArtifactDirectory(t *testing.T) {
	data := t.TempDir()
	directory := filepath.Join(data, "agents", "default", "artifacts")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "link.md")); err != nil {
		t.Fatal(err)
	}
	service := artifact.New(data)
	if rows, err := service.List("default"); err != nil || len(rows) != 0 {
		t.Fatalf("link listed=%+v err=%v", rows, err)
	}
	for _, name := range []string{"link.md", "../outside.md", outside} {
		if _, f, err := service.Open("default", name); err == nil {
			_ = f.Close()
			t.Fatalf("opened %s", name)
		}
		if err := service.Delete("default", name); err == nil {
			t.Fatalf("deleted %s", name)
		}
	}
	if err := os.Remove(filepath.Join(directory, "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), directory); err != nil {
		t.Fatal(err)
	}
	if _, err := service.List("default"); err == nil {
		t.Fatal("followed artifacts directory link")
	}
	if body, err := os.ReadFile(outside); err != nil || string(body) != "outside" {
		t.Fatalf("source=%q err=%v", body, err)
	}
}

func TestUserCannotDeliverOutsideWorkspaceOrOverTheLimit(t *testing.T) {
	data := t.TempDir()
	workspace := filepath.Join(data, "agents", "default", "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "link")); err != nil {
		t.Fatal(err)
	}
	large := filepath.Join(workspace, "large.bin")
	if err := os.WriteFile(large, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(large, artifact.MaxSize+1); err != nil {
		t.Fatal(err)
	}
	service := artifact.New(data)
	for _, path := range []string{"../outside.md", outside, "link", "large.bin"} {
		if _, err := service.Deliver(t.Context(), engine.TaskContext{AgentID: "default"}, path); err == nil {
			t.Fatalf("delivered %s", path)
		}
	}
}
