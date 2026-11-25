package engine

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
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
	waitForRunningTool(t, a, ref, "bash")
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
	if err := a.StopSession(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*Submission{sub, replay} {
		coreWait(t, s)
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
	config.MCPServers = testMCPStore(t, config.DataDir)
	testSetMCP(t, config.MCPServers, "alpha", "secret-token", serverConfig{Type: "http", URL: server.URL})
	extAgents := coreEngine(t, config)
	extRef := coreCreate(t, extAgents, "alpha", coreOptions("tool"))
	extSub, err := extAgents.Submit(t.Context(), extRef, Input{
		RequestID: "masked", Content: `mcp__secret-token__append {"text":"hi"}`, Kind: "message",
		Origin: "task", Source: "dex:alpha/task/masked", Child: true, Options: coreOptions("tool"),
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForRunningTool(t, extAgents, extRef, "other")
	unblock()
	coreWait(t, extSub)
}

func waitForRunningTool(t *testing.T, a *Agents, ref Ref, name string) {
	t.Helper()
	// Start the deadline after admission. Setup and settlement of other runs do not count.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	tick := time.Tick(5 * time.Millisecond)
	for {
		activity := a.Activity([]Ref{ref})[ref]
		if activity.LastActivityAt != nil && activity.ToolStartedAt != nil && len(activity.RunningTools) == 1 {
			if got := activity.RunningTools[0].Name; got != name {
				t.Fatalf("running tool = %q, want %q", got, name)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("activity for %q did not update: %+v", name, activity)
		case <-tick:
		}
	}
}
