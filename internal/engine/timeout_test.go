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

	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/tools"
)

func TestUserBashGetsDefaultTimeoutAndKeepsExplicitTimeout(t *testing.T) {
	// Given a command with an omitted, null, or explicit timeout.
	for _, tc := range []struct {
		name string
		args map[string]any
		want float64
	}{
		{"omitted", map[string]any{"command": "sleep 1"}, 120},
		{"null", map[string]any{"command": "sleep 1", "timeout": nil}, 120},
		{"explicit", map[string]any{"command": "sleep 1", "timeout": 300.0}, 300},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// When the command is prepared for the native bash tool.
			args, err := bashDefaultTimeout(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			input, err := durable.FromJsonValue[tools.BashToolInput](args)
			// Then the native tool receives the default or the explicit timeout.
			if err != nil || input.Timeout == nil || *input.Timeout != tc.want {
				t.Fatalf("timeout: %+v, %v", input, err)
			}
		})
	}
}

func TestUserBashTimeoutEndsItsProcess(t *testing.T) {
	// Given a real shell with a child process that would write after the timeout.
	for _, tc := range []struct {
		name, timeout string
		wait          time.Duration
	}{
		{"default", "", 135 * time.Second},
		{"null", `, "timeout": null`, 135 * time.Second},
		{"explicit", `, "timeout": 0.2`, 10 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			a := coreEngine(t, Config{})
			ref := coreCreate(t, a, "alpha", coreOptions("tool"))
			pidPath := filepath.Join(dir, "pid")
			marker := filepath.Join(dir, "later")
			command := fmt.Sprintf(`bash {"command": "sleep 130 & echo $! > %s; wait; touch %s"%s}`, pidPath, marker, tc.timeout)
			sub := coreSubmit(t, a, ref, "timeout", command, "tool")
			coreFile(t, pidPath)
			ctx, cancel := context.WithTimeout(t.Context(), tc.wait)
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
