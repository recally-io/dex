package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
	"uuid"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
	"github.com/nsgo-dev/nsgo/ui"
)

func TestUserCanChooseOrCancelNewSessionProject(t *testing.T) {
	t.Parallel()
	if os.Getenv("DEX_NSGO_TEST_URL") != "http://127.0.0.1:19545" {
		t.Skip("requires the disposable project picker gateway on port 19545")
	}
	chatRequireTestGateway(t)
	const timeout = 20 * time.Second
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	// Given a reused writable session with a project on the explicit Local Runner.
	parent := t.TempDir()
	project := filepath.Join(parent, "fixture-project")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	if canonical, err := filepath.EvalSymlinks(parent); err == nil {
		parent = canonical
		project = filepath.Join(parent, "fixture-project")
	}
	var bot struct {
		ID string `json:"id"`
	}
	if err := requestJSON(ctx, http.MethodPost, "/api/bots", map[string]string{
		"name": "Project picker " + uuid.New().String()[:8], "provider": "faux", "modelId": "echo",
	}, &bot); err != nil {
		t.Fatal(err)
	}
	var session chatSession
	if err := requestJSON(ctx, http.MethodPost, chatSessionsPath(bot.ID), nil, &session); err != nil {
		t.Fatal(err)
	}
	endpoint := chatSessionPath(bot.ID, session.ID)
	if err := requestJSON(ctx, http.MethodPut, endpoint+"/runner", map[string]string{"runnerId": "local"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := requestJSON(ctx, http.MethodPut, endpoint+"/project", map[string]string{"cwd": project}, &session); err != nil {
		t.Fatal(err)
	}
	type pickerEvent struct {
		closed bool
		saved  chatSession
	}
	events := make(chan pickerEvent, 8)
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		open := core.NewSignal(cx, true)
		return core.Group{
			ui.Button{Text: "Open project picker", TestID: "open-picker", OnPress: func() { open.Set(true) }},
			core.Show{When: open.Get, Then: func(cx *core.Scope) core.Node {
				return newSessionProject(cx, bot.ID, session, func() {
					events <- pickerEvent{closed: true}
					open.Set(false)
				}, func(saved chatSession) { events <- pickerEvent{saved: saved} })
			}},
		}
	})
	dialog := "id:new-session-project-dialog"
	path := "id:new-session-project-path"
	use := "id:new-session-project-save"
	checkProject := func(want string) {
		t.Helper()
		got, err := chatGetSession(ctx, bot.ID, session.ID)
		if err != nil || got.Cwd != want || got.RunnerID != "local" {
			t.Fatalf("current session = %+v, error = %v; want cwd %q on Local", got, err, want)
		}
	}

	nextEvent := func() pickerEvent {
		t.Helper()
		select {
		case event := <-events:
			return event
		default:
			t.Fatal("missing project picker callback")
			return pickerEvent{}
		}
	}
	checkClosed := func() {
		t.Helper()
		if got := nextEvent(); !got.closed {
			t.Fatalf("expected close callback, got save callback: %+v", got.saved)
		}
		select {
		case extra := <-events:
			t.Fatalf("unexpected callback after close: %+v", extra)
		default:
		}
	}
	checkSaved := func(want string) {
		t.Helper()
		got := nextEvent()
		if got.closed || got.saved.ID != session.ID || got.saved.Cwd != want || got.saved.RunnerID != "local" {
			t.Fatalf("expected save callback for session %d on Local with cwd %q before close, got %+v", session.ID, want, got)
		}
		checkClosed()
	}

	// When the user opens an invalid path, the picker keeps the attempted path and reports the error.
	app.WaitFor(dialog, timeout)
	recentProject := `role:button "` + project + `"`
	app.WaitFor(recentProject, timeout)
	bad := filepath.Join(parent, "missing")
	app.Clear(path)
	app.Type(path, bad)
	app.Submit(path)
	app.WaitFor("id:new-session-project-directory-error", timeout)
	if got := app.Find(path).Text; got != bad {
		t.Fatalf("path after error = %q, want %q", got, bad)
	}
	app.Press("id:new-session-project-cancel")
	app.WaitForGone(dialog, timeout)
	checkClosed()
	checkProject(project)

	// When a recent project disappears, selecting it reports a save error and keeps the dialog open.
	app.Press("id:open-picker")
	app.WaitFor(recentProject, timeout)
	app.WaitUntil(timeout, func() bool { return app.Enabled("id:new-session-project-up") })
	if err := os.Remove(project); err != nil {
		t.Fatal(err)
	}
	app.Press(recentProject)
	app.WaitFor("id:new-session-project-save-error", timeout)
	checkProject(project)

	// Then confirming the parent changes this session, and reopening reads the saved path.
	app.Press("id:new-session-project-up")
	app.WaitUntil(timeout, func() bool { return app.Enabled(use) && app.Find(path).Text == parent })
	app.Press(use)
	app.WaitForGone(dialog, timeout)
	checkSaved(parent)
	checkProject(parent)
	app.Press("id:open-picker")
	app.WaitUntil(timeout, func() bool { return app.Enabled(use) && app.Find(path).Text == parent })
	app.Press("id:new-session-project-reset")
	app.WaitForGone(dialog, timeout)
	checkSaved("")
	checkProject("")
}
