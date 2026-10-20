package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUserMemoryGitIgnoresAgentRepositoryCommands(t *testing.T) {
	for _, command := range []string{"hook", "fsmonitor"} {
		t.Run(command, func(t *testing.T) {
			// Given a memory repository with an agent-controlled Git command.
			repo := NewMemoryRepo(t.TempDir(), map[string]string{"PATH": os.Getenv("PATH")})
			if err := repo.Open(t.Context()); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(t.TempDir(), "ran")
			script := filepath.Join(repo.Dir, ".git", "hooks", "pre-commit")
			body := "#!/bin/sh\ntouch '" + strings.ReplaceAll(marker, "'", "'\\''") + "'\n"
			if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			if command == "fsmonitor" {
				if _, err := repo.git(t.Context(), "config", "core.fsmonitor", script); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(script, script+"-monitor"); err != nil {
					t.Fatal(err)
				}
				if _, err := repo.git(t.Context(), "config", "core.fsmonitor", script+"-monitor"); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(repo.Dir, "entry.md"), []byte("saved memory"), 0o600); err != nil {
				t.Fatal(err)
			}

			// When dex commits memory, repository commands do not run.
			if committed, err := repo.Commit(t.Context(), "Save memory"); err != nil || !committed {
				t.Fatalf("memory commit: changed=%t err=%v", committed, err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("agent Git %s ran: %v", command, err)
			}
		})
	}
}

func TestUserMemoryGitDoesNotInheritProcessSecrets(t *testing.T) {
	// Given an agent-controlled clean filter and a value outside the explicit tool environment.
	t.Setenv("DEX_PROCESS_SENTINEL", "process-only-test-value")
	repo := NewMemoryRepo(t.TempDir(), map[string]string{"PATH": os.Getenv("PATH")})
	if err := repo.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "filter-env")
	script := filepath.Join(repo.Dir, ".git", "capture-env")
	body := fmt.Sprintf("#!/bin/sh\nprintf '%%s' \"${DEX_PROCESS_SENTINEL-unset}\" > '%s'\ncat\n", strings.ReplaceAll(output, "'", "'\\''"))
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.git(t.Context(), "config", "filter.capture.clean", script); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{".gitattributes": "entry.txt filter=capture\n", "entry.txt": "saved memory"} {
		if err := os.WriteFile(filepath.Join(repo.Dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// When real Git runs its filter, it sees no process-only value.
	if _, err := repo.Commit(t.Context(), "Save memory"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil || string(got) != "unset" {
		t.Fatalf("Git inherited process environment: %q err=%v", got, err)
	}
}
