package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestUserBashTimeoutEndsItsProcess(t *testing.T) {
	// Given a real shell with a child process that would write after the timeout.
	for _, tc := range []struct {
		name, timeout string
	}{
		{"default", ""},
		{"null", `, "timeout": null`},
		{"explicit", `, "timeout": 0.2`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			a := coreEngine(t, Config{BashTimeout: 200 * time.Millisecond})
			ref := coreCreate(t, a, "alpha", coreOptions("tool"))
			pidPath := filepath.Join(dir, "pid")
			marker := filepath.Join(dir, "later")
			command := fmt.Sprintf(`bash {"command": "sleep 5 & echo $! > %s; wait; touch %s"%s}`, pidPath, marker, tc.timeout)
			sub := coreSubmit(t, a, ref, "timeout", command, "tool")
			coreFile(t, pidPath)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			// When the timeout expires, the native tool returns an error.
			outcome, err := sub.Wait(ctx)
			if err != nil || !strings.Contains(outcome.Answer, "timed out") || !strings.Contains(outcome.Answer, "tool error:") {
				t.Fatalf("timeout result: %+v, %v", outcome, err)
			}
			// Then the child process has ended, not just the shell that started it.
			data, err := os.ReadFile(pidPath)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				t.Fatal(err)
			}
			if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
				t.Fatalf("child process %d still exists or cannot be checked: %v", pid, err)
			}
		})
	}
}
