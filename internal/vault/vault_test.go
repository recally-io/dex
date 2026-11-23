package vault_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/recally-io/dex/internal/vault"
)

func TestUserCannotOpenCopiedVaultWithoutIdentity(t *testing.T) {
	data := t.TempDir()
	store := vault.New(data)
	const secret = "test-vault-credential"
	if err := store.Edit("bot", "dex", "TOKEN", new(secret)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(data, "vault", "dex.age")
	dataBytes, err := os.ReadFile(path)
	if err != nil || bytes.Contains(dataBytes, []byte(secret)) || !bytes.HasPrefix(dataBytes, []byte("age-encryption.org/v1\n")) {
		t.Fatalf("expected an age file: %v", err)
	}
	for _, path := range []string{path, filepath.Join(data, "vault", "identity.txt")} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("permissions of %s: %v %v", path, info, err)
		}
	}
	copyDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(copyDir, "vault"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(copyDir, "vault", "dex.age"), dataBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	copied := vault.New(copyDir)
	if _, err := copied.Load("bot"); err == nil || !strings.Contains(err.Error(), "identity is missing") {
		t.Fatalf("vault without identity: %v", err)
	}
	if err := copied.Edit("bot", "agent", "NEW", new("new-test-credential")); err == nil {
		t.Fatal("an edit replaced the missing identity")
	}
	original, err := vault.New(data).Load("bot")
	if err != nil {
		t.Fatal(err)
	}
	got, err := original.Resolve(map[string]string{"TOKEN": "vault:TOKEN"}, false)
	if err != nil || got["TOKEN"] != secret {
		t.Fatalf("original vault did not decrypt: %v", err)
	}
}

func TestUserConcurrentVaultEditsDoNotLoseEntries(t *testing.T) {
	store := vault.New(t.TempDir())
	var wg sync.WaitGroup
	for _, name := range []string{"FIRST", "SECOND"} {
		wg.Go(func() {
			if err := store.Edit("bot", "dex", name, new("test-vault-credential-"+name)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	snapshot, err := store.Load("bot")
	if err != nil || len(snapshot.List()) != 2 {
		t.Fatalf("vault metadata: %+v, %v", snapshot.List(), err)
	}
}
