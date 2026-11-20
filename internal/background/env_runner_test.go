package background_test

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	durableenv "github.com/MichaelKinsy/PiG/durable/env"
	"github.com/recally-io/dex/internal/background"
	"github.com/recally-io/dex/internal/execenv"
	"github.com/recally-io/dex/internal/execenv/execenvtest"
)

func TestUserRunsBackgroundCommandsInRemoteEnv(t *testing.T) {
	t.Setenv("CONTROL_BG_SECRET", "host-secret")
	remoteHome := t.TempDir()
	remoteRoot := t.TempDir()
	provider := execenvtest.NewProvider(execenv.KindPiEnv, remoteRoot, remoteHome, map[string]string{
		"REMOTE_BG_MARKER": "remote-bg-ok",
	})
	handle, err := provider.Open(t.Context(), "bot", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close(context.Background()) })

	f := newFixture(t, background.Limits{Batch: 50 * time.Millisecond})
	f.m.ConfigureEnvOpener(func(ctx context.Context, _, _ string, cwd string, extraEnv map[string]string) (durableenv.ExecutionEnv, string, execenv.Kind, func(), error) {
		env, err := handle.Env(ctx, cwd, extraEnv)
		if err != nil {
			return nil, "", "", nil, err
		}
		return env, handle.Workspace(), handle.Kind(), func() {}, nil
	})

	startRemote := func(command, wake string, timeout time.Duration) string {
		f.nextKey++
		row, err := f.m.Start(t.Context(), background.StartParams{
			CallKey:         fmt.Sprintf("remote/%d", f.nextKey),
			AgentID:         "bot",
			ConversationID:  f.conv,
			SourceRequestID: f.source,
			Command:         command,
			Wake:            wake,
			Timeout:         timeout,
			DefaultCwd:      handle.Workspace(),
			Env:             map[string]string{"EXPLICIT_BG": "exp-ok"},
			InheritEnv:      true,
			EnvKind:         execenv.KindPiEnv,
		})
		if err != nil {
			t.Fatal(err)
		}
		return row.ID
	}

	// When a wake=exit command runs remotely.
	exitID := startRemote(`printf "%s|%s|%s\n" "$REMOTE_BG_MARKER" "$EXPLICIT_BG" "${CONTROL_BG_SECRET:-}" && printf "stderr line\n" >&2`, background.WakeExit, 10*time.Second)
	f.waitExited(exitID)

	wantLog := filepath.Join(handle.Workspace(), ".dex", "background", exitID+".log")
	logBytes, err := os.ReadFile(wantLog)
	if err != nil || !strings.Contains(string(logBytes), "remote-bg-ok|exp-ok|") || !strings.Contains(string(logBytes), "stderr line") {
		t.Fatalf("remote log (%s) bytes=%q err=%v", wantLog, logBytes, err)
	}
	var endWake []byte
	deadline := time.Now().Add(5 * time.Second)
	for {
		queryErr := f.conn.QueryRowContext(t.Context(), `SELECT content FROM submissions WHERE id = ?`, "background:"+exitID+":end").Scan(&endWake)
		if queryErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiting for end wake: %v", queryErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	var decoded struct {
		Result struct {
			Event      string `json:"event"`
			Output     string `json:"output"`
			OutputFile string `json:"outputFile"`
		} `json:"background-result"`
	}
	if err := json.Unmarshal(endWake, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Result.Event != background.StateExited || decoded.Result.OutputFile != wantLog || !strings.Contains(decoded.Result.Output, "remote-bg-ok|exp-ok|") {
		t.Fatalf("unexpected remote end wake: %+v", decoded.Result)
	}

	// When a wake=lines command prints lines and exits.
	linesID := startRemote(`printf "line-one\nline-two\n"`, background.WakeLines, 10*time.Second)
	f.waitExited(linesID)
	var lineCount int
	deadline = time.Now().Add(5 * time.Second)
	for {
		if err := f.conn.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM submissions WHERE id LIKE ?`, "background:"+linesID+":%").Scan(&lineCount); err == nil && lineCount >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected lines + end wake, got count=%d", lineCount)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Stop and timeout.
	stopID := startRemote(`sleep 30`, background.WakeExit, 30*time.Second)
	stopped, err := f.m.Stop(t.Context(), f.conv, stopID)
	if err != nil || stopped.State != background.StateStopped {
		t.Fatalf("stop remote command: row=%+v err=%v", stopped, err)
	}

	timeoutID := startRemote(`sleep 30`, background.WakeExit, 150*time.Millisecond)
	deadline = time.Now().Add(10 * time.Second)
	for {
		state, reason := f.state(timeoutID)
		if state == background.StateTimedOut {
			if reason != "timeout" {
				t.Fatalf("timeout reason=%q want=timeout", reason)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed_out wait ended in state=%s (%s)", state, reason)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
