package session

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

type sessionLogBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *sessionLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *sessionLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func TestUserSessionDiagnosticsSeparateQueuedStartAndStop(t *testing.T) {
	// Given a running native turn and a second ordinary input in its Inbox.
	var logs sessionLogBuffer
	prior := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prior) })
	r := startNative(t, "bash")
	marker := filepath.Join(r.dir, "started")
	id, err := r.service.Post(t.Context(), "bot", MainKey, "", "first-private-id", "# command-canary\ntouch '"+marker+"'; exec sleep 60")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, err := os.Stat(marker); return err == nil })
	const next = "next-private-id"
	if _, err := r.service.Post(t.Context(), "bot", MainKey, "", next, "printf 'output-canary'"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.service.Send(t.Context(), "bot", MainKey, Message{ID: "reject-private-id", Content: "reject-canary", WhenBusy: "reject"}); !errors.Is(err, engine.ErrBusy) {
		t.Fatalf("busy rejection: %v", err)
	}
	queued := logs.String()
	digest := sha256.Sum256([]byte(next))
	if !strings.Contains(queued, "request_hash="+hex.EncodeToString(digest[:16])) || !strings.Contains(queued, "phase=admitted") || strings.Count(queued, "phase=generation_started") != 1 {
		t.Fatalf("queued input must have a receipt but no start:\n%s", queued)
	}

	// When an exact cancellation wait expires, the log must not claim abort.
	c, err := r.service.Get(t.Context(), "bot", id)
	if err != nil {
		t.Fatal(err)
	}
	ref := executionRef(c)
	watch, err := r.bots.Watch(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	target := watch.Snapshot.RunTarget
	watch.Close()
	deadline, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	if err := r.bots.StopTask(deadline, ref, target); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired stop: %v", err)
	}
	if err := r.service.Stop(t.Context(), "bot", id, &target); err != nil {
		t.Fatal(err)
	}

	// Then the exact target aborts and the next generation starts, with no raw payload or ID.
	if sub := awaitReceipt(t, r, next); sub.Status != "done" {
		t.Fatalf("next input: %s", sub.Status)
	}
	waitFor(t, func() bool { return strings.Contains(logs.String(), "phase=settled reason=aborted") })
	got := logs.String()
	for _, phrase := range []string{"phase=enqueued", "reason=initialization", "reason=unknown", "reason=session_busy", "queued_ms=", "elapsed_ms=", "native_submission_ids=", "phase=stop_recorded", "reason=deadline abort_observed=false", "reason=aborted abort_observed=true", "phase=settled reason=aborted"} {
		if !strings.Contains(got, phrase) {
			t.Errorf("missing %q in logs:\n%s", phrase, got)
		}
	}
	if strings.Count(got, "phase=generation_started") != 2 {
		t.Errorf("expected one native start per turn, without duplicates from tool rounds:\n%s", got)
	}
	// The native shell reports a typed timeout; do not infer it from tool output.
	if err := r.q.UpdateBotSettings(t.Context(), sqlc.UpdateBotSettingsParams{ID: "bot", Name: "Bot", ModelProvider: "faux", ModelID: "tool", ThinkingLevel: "off"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.service.Post(t.Context(), "bot", MainKey, "", "timeout-private-id", `bash {"command":"# timeout-canary\nexec sleep 5","timeout":0.1}`); err != nil {
		t.Fatal(err)
	}
	_ = awaitReceipt(t, r, "timeout-private-id")
	waitFor(t, func() bool { return strings.Contains(logs.String(), "phase=shell_timeout") })
	got = logs.String()
	for _, private := range []string{"first-private-id", next, "command-canary", "output-canary", "reject-private-id", "reject-canary", "timeout-private-id", "timeout-canary"} {
		if strings.Contains(got, private) {
			t.Error("log contains private input or output")
		}
	}
}
