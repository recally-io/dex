package engine

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/mcptest"
)

func TestUserBackgroundActivityTracksWithoutUserWatch(t *testing.T) {
	// Given a background execution with a real faux bash tool and no transcript subscriber.
	a := coreEngine(t, Config{})
	ref := coreCreate(t, a, "alpha", coreOptions("bash"))
	sub, err := a.Submit(t.Context(), ref, Input{
		RequestID: "activity", Content: "printf activity; while :; do sleep 1; done", Kind: "message",
		Origin: "task", Source: "dex:alpha/task/activity", Child: true, Options: coreOptions("bash"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	var activity Activity
	for {
		activity = a.Activity([]Ref{ref})[ref]
		if activity.LastActivityAt != nil && activity.ToolStartedAt != nil && len(activity.RunningTools) == 1 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("activity did not update: %+v", activity)
		case <-tick.C:
		}
	}
	if activity.RunningTools[0].Name != "bash" {
		t.Fatalf("running tool: %+v", activity.RunningTools)
	}
	// When admission retries the same request, it keeps the single running child watch.
	replay, err := a.Submit(t.Context(), ref, Input{
		RequestID: "activity", Content: "printf activity; while :; do sleep 1; done", Kind: "message",
		Origin: "task", Source: "dex:alpha/task/activity", Child: true, Options: coreOptions("bash"),
	})
	if err != nil || replay.ID != sub.ID {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	if running, ok := a.ChildRunning(ref); !ok || !running {
		t.Fatalf("ChildRunning during execution = (%t, %t), want (true, true)", running, ok)
	}
	_ = a.Abort(t.Context(), ref)
	for _, s := range []*Submission{sub, replay} {
		if _, err := s.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := a.Activity([]Ref{ref}); len(got) != 0 {
		t.Fatalf("activity remained after settlement: %+v", got)
	}
	if running, ok := a.ChildRunning(ref); running || ok {
		t.Fatalf("ChildRunning after settlement = (%t, %t), want (false, false)", running, ok)
	}

	// Extension tool names can hold private names, so Activity masks them as "other".
	config := Config{DataDir: t.TempDir(), Home: t.TempDir()}
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	mcp := &mcptest.Server{Dir: t.TempDir()}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var request struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(body, &request) == nil && request.Method == "tools/call" {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		mcp.ServeHTTP(w, r)
	}))
	defer func() { unblock(); server.Close() }()
	raw, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"secret-token": map[string]any{"type": "http", "url": server.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	extensionFile(t, filepath.Join(config.DataDir, "agents", "alpha", ".agents", "mcp.json"), string(raw))
	extAgents := coreEngine(t, config)
	extRef := coreCreate(t, extAgents, "alpha", coreOptions("tool"))
	extSub, err := extAgents.Submit(t.Context(), extRef, Input{
		RequestID: "masked", Content: `mcp__secret-token__append {"text":"hi"}`, Kind: "message",
		Origin: "task", Source: "dex:alpha/task/masked", Child: true, Options: coreOptions("tool"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for {
		activity = extAgents.Activity([]Ref{extRef})[extRef]
		if len(activity.RunningTools) == 1 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("masked activity did not update: %+v", activity)
		case <-tick.C:
		}
	}
	if activity.RunningTools[0].Name != "other" {
		t.Fatalf("extension tool name exposed: %+v", activity.RunningTools)
	}
	unblock()
	coreWait(t, extSub)
}
