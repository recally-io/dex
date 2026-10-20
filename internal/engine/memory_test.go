package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/durable"
)

func TestUserMemoryRecoversChangesAndRefreshesPrompt(t *testing.T) {
	// Given a new local memory repository.
	repo := NewMemoryRepo(filepath.Join(t.TempDir(), "memory"), nil)
	if err := repo.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	from := "dex:a/chat/one"
	ext := repo.Extension("a", func(context.Context, durable.ConversationId) (string, error) { return from, nil })
	if err := os.WriteFile(filepath.Join(repo.Dir, "MEMORY.md"), []byte("remember today"), 0600); err != nil {
		t.Fatal(err)
	}
	// When another owner reopens after an uncommitted edit.
	reopened := NewMemoryRepo(repo.Dir, nil)
	if err := reopened.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	log, err := reopened.git(t.Context(), "log", "-1", "--format=%s")
	if err != nil || strings.TrimSpace(log) != "Recover uncommitted memory changes" {
		t.Fatalf("recovery commit: %q %v", log, err)
	}
	text, err := ext.Sections[1].Render(t.Context(), durable.PromptInput{})
	if err != nil || text == nil || *text != "remember today" {
		t.Fatalf("memory prompt: %v %v", text, err)
	}
	from = "dex:a/chat/two"
	rules, err := ext.Sections[0].Render(t.Context(), durable.PromptInput{})
	if err != nil || !strings.Contains(*rules, "source: dex:a/chat/two") {
		t.Fatalf("fresh source: %v %v", rules, err)
	}
	from = "dex:a/dream/one"
	rules, err = ext.Sections[0].Render(t.Context(), durable.PromptInput{})
	if err != nil || strings.Contains(*rules, "For entries that you learn") {
		t.Fatalf("dream source: %v %v", rules, err)
	}
	if changed, err := repo.Commit(t.Context(), "no changes"); err != nil || changed {
		t.Fatalf("empty commit: %v %v", changed, err)
	}
}
func TestUserMemoryCreatesNestedRepository(t *testing.T) {
	root := NewMemoryRepo(t.TempDir(), nil)
	if err := root.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	nested := NewMemoryRepo(filepath.Join(root.Dir, "nested"), nil)
	if err := nested.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(nested.Dir, ".git")); err != nil {
		t.Fatal(err)
	}
}
