package runner_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/durable/env"
	"github.com/recally-io/dex/internal/execenv/runner"
)

func TestUserCanCorrelateRunnerWaitSendStartAndCancellation(t *testing.T) {
	logs := captureRunnerLogs(t)
	client, _ := connect(t)
	ctx, deadline := context.WithTimeout(t.Context(), 10*time.Second)
	defer deadline()
	callCtx, connected := runner.TraceConnectionWait(ctx, "test-runner", runner.MethodExec)
	connected(time.Now(), nil)
	callCtx, cancel := context.WithCancel(callCtx)
	defer cancel()
	_, err := client.Exec(callCtx, `printf 'sensitive-output'; exec sleep 60 # sensitive-command`, runner.ExecParams{}, func(_ string, _ env.ShellStream) {
		cancel()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("command returned %v", err)
	}
	_, unavailable := runner.TraceConnectionWait(ctx, "absent-runner", runner.MethodRead)
	unavailable(time.Now(), errors.Join(runner.ErrOffline, errors.New("sensitive-error")))

	raw := logs.String()
	if strings.Contains(raw, "sensitive-") {
		t.Fatal("runner log contained private command, error or output text")
	}
	var callID string
	var terminal map[string]any
	seen := map[string]bool{}
	for _, record := range runnerLogRecords(t, raw) {
		if record["environment"] == "absent-runner" {
			if record["phase"] != "connection_wait" || record["result"] != "offline" || record["level"] != "INFO" {
				t.Fatalf("offline wait was not classified: %v", record)
			}
			seen["offline"] = true
			continue
		}
		id, _ := record["call_id"].(string)
		if id == "" || (callID != "" && id != callID) || record["environment"] != "test-runner" || record["method"] != "exec" {
			t.Fatalf("event not correlated to runner exec: %v", record)
		}
		callID = id
		phase, _ := record["phase"].(string)
		seen[phase] = true
		if phase == "finished" {
			terminal = record
		}
	}
	for _, phase := range []string{"connection_wait", "slot_acquired", "sent", "remote_started", "finished", "offline"} {
		if !seen[phase] {
			t.Fatalf("missing phase %q", phase)
		}
	}
	if terminal["result"] != "canceled" || terminal["cancel_settlement"] != "acknowledged" || terminal["sent"] != true || terminal["slot_wait"] == nil || terminal["sent_after"] == nil || terminal["remote_start_observed_after"] == nil || terminal["elapsed"] == nil {
		t.Fatalf("cancel record lacks measured stages: %v", terminal)
	}
}

type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func captureRunnerLogs(t *testing.T) *logBuffer {
	t.Helper()
	old := slog.Default()
	t.Cleanup(func() { slog.SetDefault(old) })
	logs := new(logBuffer)
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return logs
}

func runnerLogRecords(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(raw), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["msg"] == "runner RPC" {
			out = append(out, record)
		}
	}
	return out
}
