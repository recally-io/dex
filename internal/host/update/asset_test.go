package update

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestUserUpdateVerifiesStagedFileWhileAnotherForkHoldsIt(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("Linux reports ETXTBSY for an executable that is open for writing")
	}
	// Given a staged executable that another descriptor still holds open for writing,
	// as a child process forked by another goroutine does until it calls exec.
	stage := filepath.Join(t.TempDir(), "dex")
	if err := os.WriteFile(stage, []byte("#!/bin/sh\necho dex v9.9.9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	held, err := os.OpenFile(stage, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	release := time.AfterFunc(50*time.Millisecond, func() { _ = held.Close() })
	t.Cleanup(func() { release.Stop(); _ = held.Close() })

	// When the updater verifies it, then it waits for the descriptor and runs the file.
	out, err := runStaged(t.Context(), stage)
	if err != nil {
		t.Fatalf("runStaged: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "dex v9.9.9" {
		t.Fatalf("version output = %q", got)
	}
	t.Logf("artifact: staged version=%q after a held write descriptor closed", strings.TrimSpace(string(out)))
}
