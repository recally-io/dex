package main

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"
	"uuid"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

const sessionSurfaceTimeout = 20 * time.Second

func sessionsRequireIsolatedGateway(t *testing.T) {
	t.Helper()
	if os.Getenv("DEX_NSGO_SESSION_SURFACES_TEST") != "1" {
		t.Skip("set DEX_NSGO_SESSION_SURFACES_TEST=1 for the disposable #377 gateway")
	}
	origin, err := url.Parse(os.Getenv("DEX_NSGO_TEST_URL"))
	if os.Getenv("DEX_NSGO_POC_TEST") != "1" || err != nil || origin.Scheme != "http" ||
		origin.Hostname() != "127.0.0.1" || (origin.Port() != "19545" && origin.Port() != "19547") ||
		origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		t.Fatal("the test requires the disposable loopback gateway on port 19545 or 19547")
	}
}

func sessionsFixtureBot(t *testing.T, ctx context.Context) string {
	t.Helper()
	var bot struct {
		ID string `json:"id"`
	}
	seed := map[string]string{"name": "Session surfaces " + uuid.New().String()[:8], "provider": "faux", "modelId": "echo"}
	if err := requestJSON(ctx, http.MethodPost, "/api/bots", seed, &bot); err != nil {
		t.Fatal(err)
	}
	if bot.ID == "" || bot.ID == "default" {
		t.Fatalf("expected a separate fixture Bot, got %q", bot.ID)
	}
	return bot.ID
}

func TestUserFiltersArchivesAndRestoresSessions(t *testing.T) {
	sessionsRequireIsolatedGateway(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	botID := sessionsFixtureBot(t, ctx)

	// Given an empty Bot's Sessions page.
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		return sessionsPage(cx, botID, func(string, int, string) {})
	})
	app.WaitFor("text:No sessions yet", sessionSurfaceTimeout)

	// When the user starts a real session, it appears in the kind filter.
	active, err := newWorkspaceSession(ctx, botID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = sendChat(ctx, botID, active.ID, uuid.New().String(), "Check the session list"); err != nil {
		t.Fatal(err)
	}
	id := strconv.Itoa(active.ID)
	// Native nsgotest advances its clock only when the test asks it to run timers.
	app.Advance(3 * time.Second)
	app.WaitFor("id:sessions-open-"+id, sessionSurfaceTimeout)
	app.Press("id:sessions-kind")
	app.Press("id:sessions-kind-2")
	app.WaitFor("text:No sessions of this kind", sessionSurfaceTimeout)
	app.Press("id:sessions-kind")
	app.Press("id:sessions-kind-0")

	// Then archive moves the row to Archived, and Restore moves it back.
	app.Press("id:sessions-archive-" + id)
	app.WaitForGone("id:sessions-open-"+id, sessionSurfaceTimeout)
	app.Press("id:sessions-archived")
	app.WaitFor("id:sessions-restore-"+id, sessionSurfaceTimeout)
	app.Press("id:sessions-restore-" + id)
	app.WaitFor("id:sessions-archive-"+id, sessionSurfaceTimeout)
}
