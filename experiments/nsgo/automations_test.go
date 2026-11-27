package main

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestUserCreatesAnEnabledDailyAutomationByDefault(t *testing.T) {
	t.Parallel()

	// Given a new automation form with a name and instructions.
	draft := automationsDraftFrom(nil, "UTC")
	draft.Name, draft.Prompt = "  Weekly notes  ", "  Summarize changes.  "

	// When the user keeps the defaults and saves, the request uses the real API fields.
	input, err := automationsFormInput(draft, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}

	// Then the automation starts enabled, uses a fresh session and notifies the primary channel.
	const want = `{"name":"Weekly notes","prompt":"Summarize changes.","cwd":"","schedule":{"kind":"daily","time":"09:00"},"timeZone":"UTC","session":"new","enabled":true,"runnerId":"","notification":{"mode":"default"}}`
	if string(body) != want {
		t.Fatalf("request = %s, want %s", body, want)
	}
}

func TestUserCanEditBoundAutomationOnlyWithAnAvailableTarget(t *testing.T) {
	t.Parallel()

	// Given a weekly automation with a saved Runner and project.
	draft := automationsDraftFrom(&automationsEntry{automationsInput: automationsInput{
		Name: "Weekly", Prompt: "Review the notes", Cwd: "/project", RunnerID: "machine", Session: "reuse",
		Schedule: automationsSchedule{Kind: "weekly", Time: "09:30", Days: []int{1, 5}}, TimeZone: "Asia/Shanghai",
		Notification: &automationsNotification{Mode: "none"},
	}}, "UTC")
	draft.Session, draft.TargetSessionID = "session", 42

	// When the user selects a deleted target, the form cannot save.
	if _, err := automationsFormInput(draft, nil); err == nil {
		t.Fatal("saved an unavailable target")
	}

	// Then an eligible target keeps its ID, without carrying the old Runner or project.
	input, err := automationsFormInput(draft, []automationsTarget{{ID: 42, Title: "Example"}})
	if err != nil {
		t.Fatal(err)
	}
	if input.Session != "session" || input.TargetSessionID != 42 || input.Cwd != "" || input.RunnerID != "" || input.Enabled ||
		!reflect.DeepEqual(input.Schedule, automationsSchedule{Kind: "weekly", Time: "09:30", Days: []int{1, 5}}) || input.Notification.Mode != "none" {
		t.Fatalf("bound input changed unrelated settings: %+v", input)
	}
}

func TestUserCreatesEditsRunsAndDeletesAnAutomation(t *testing.T) {
	if os.Getenv("DEX_NSGO_AUTOMATIONS_TEST") != "1" {
		t.Skip("set DEX_NSGO_AUTOMATIONS_TEST=1 to use the dedicated synthetic gateway")
	}
	if os.Getenv("DEX_NSGO_POC_TEST") != "1" || os.Getenv("DEX_NSGO_TEST_URL") != "http://127.0.0.1:19545" {
		t.Fatal("automation writes require the dedicated synthetic gateway on port 19545")
	}
	t.Parallel()
	for _, view := range []struct {
		name string
		w, h core.Points
	}{{"desktop", 1280, 800}, {"phone", 390, 820}} {
		t.Run(view.name, func(t *testing.T) {
			t.Parallel()
			var health struct {
				DataDir string `json:"dataDir"`
			}
			if err := requestJSON(t.Context(), http.MethodGet, "/api/health", nil, &health); err != nil || !strings.Contains(health.DataDir, "/T/tmp.") {
				t.Fatalf("not the disposable gateway: %v", err)
			}
			var bot struct {
				ID       string `json:"id"`
				Provider string `json:"provider"`
			}
			name := "Automation " + uuid.New().String()[:8]
			if err := requestJSON(t.Context(), http.MethodPost, "/api/bots", map[string]string{"name": name, "provider": "faux", "modelId": "tool", "runnerId": "local", "fallbackModelId": ""}, &bot); err != nil {
				t.Fatal(err)
			}
			if bot.ID == "" || bot.ID == "default" || bot.Provider != "faux" {
				t.Fatalf("cannot use Bot %+v", bot)
			}
			main, err := chatGetSession(t.Context(), bot.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				var list struct {
					Automations []automationsEntry `json:"automations"`
				}
				if err := requestJSON(ctx, http.MethodGet, automationsPath(bot.ID, ""), nil, &list); err != nil {
					t.Error(err)
					return
				}
				for _, item := range list.Automations {
					if err := requestJSON(ctx, http.MethodDelete, automationsPath(bot.ID, item.ID), nil, nil); err != nil {
						t.Error(err)
					}
				}
			})
			const wait = 18 * time.Second
			// Given a new Bot with no automations, create one from its list.
			var transcript int
			app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
				return automationsPage(cx, bot.ID, func(id int, _ string) { transcript = id })
			}, nsgotest.Size(view.w, view.h))
			app.WaitFor("text:No automations yet", wait)
			app.Press("id:automations-new")
			app.WaitFor("id:automations-form", wait)
			app.Press("id:automations-name")
			app.Commit("id:automations-name", name)
			app.Press("id:automations-prompt")
			app.Commit("id:automations-prompt", `automation_report {"outcome":"changed","summary":"Synthetic run changed"}`)
			app.Press("id:automations-submit")
			app.WaitForGone("id:automations-form", wait)
			load := func() automationsEntry {
				var result struct {
					Automations []automationsEntry `json:"automations"`
				}
				if err := requestJSON(t.Context(), http.MethodGet, automationsPath(bot.ID, ""), nil, &result); err != nil {
					t.Fatal(err)
				}
				if len(result.Automations) != 1 {
					t.Fatalf("automations = %+v", result.Automations)
				}
				return result.Automations[0]
			}
			a := load()
			if a.Schedule.Kind != "daily" || !a.Enabled {
				t.Fatalf("saved automation = %+v", a)
			}
			app.Press("id:automations-toggle-" + a.ID)
			app.WaitFor("text:0 of 1 on. Open one to see its runs.", wait)
			app.Press("id:automations-open-" + a.ID)
			app.WaitFor("text:No runs yet. Use Run now to try the prompt.", wait)
			// When the user edits the execution target, the existing session is stored by ID.
			app.Press("id:automations-edit")
			app.Press("id:automations-session-mode")
			app.Press("id:automations-session-mode-2")
			app.WaitFor("id:automations-target", wait)
			app.WaitUntil(wait, func() bool { return !strings.Contains(app.FindMounted("id:automations-target").Text, "Loading") })
			app.Press("id:automations-target")
			app.WaitFor("id:automations-target-1", wait)
			app.Press("id:automations-target-1")
			app.Press("id:automations-submit")
			app.WaitForGone("id:automations-form", wait)
			a = load()
			if a.Session != "session" || a.TargetSessionID != int64(main.ID) || a.Enabled {
				t.Fatalf("edited automation = %+v", a)
			}
			// Then a manual run works while paused and the history opens the saved target.
			app.WaitUntil(wait, func() bool { return !app.FindMounted("id:automations-run-" + a.ID).Props.Disabled })
			app.Press("id:automations-run-" + a.ID)
			var runs struct {
				Runs []automationsRun `json:"runs"`
			}
			app.WaitUntil(wait, func() bool {
				return requestJSON(t.Context(), http.MethodGet, automationsPath(bot.ID, a.ID)+"/runs", nil, &runs) == nil && len(runs.Runs) == 1 && runs.Runs[0].Status != "running"
			})
			if got := runs.Runs[0]; got.Outcome != "changed" || int64(got.SessionID) != a.TargetSessionID {
				t.Fatalf("run = %+v", got)
			}
			app.Advance(11 * time.Second)
			summary := app.WaitFor("id:automations-summary-"+runs.Runs[0].ID, wait)
			if summary.Text != "Synthetic run changed" {
				t.Fatalf("summary = %q", summary.Text)
			}
			app.Press("id:automations-chat-" + runs.Runs[0].ID)
			if int64(transcript) != a.TargetSessionID {
				t.Fatalf("opened session = %d", transcript)
			}
			app.Press("id:automations-back")
			app.WaitFor("id:automations-open-"+a.ID, wait)
			app.Press("id:automations-open-" + a.ID)
			app.WaitFor("id:automations-delete", wait)
			app.Press("id:automations-delete")
			app.WaitFor("id:automations-delete-dialog", wait)
			app.Press("id:automations-confirm-delete")
			app.WaitFor("text:No automations yet", wait)
		})
	}
}

func TestUserSchedulesOneRunInTheDisplayedTimeZone(t *testing.T) {
	t.Parallel()
	draft := automationsDraftFrom(nil, "UTC")
	draft.Name, draft.Prompt = "Once", "Check the notes."
	draft.Schedule.Kind, draft.TimeZone, draft.Once = "once", "Asia/Shanghai", "2031-10-15T08:30"
	input, err := automationsFormInput(draft, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2031, 10, 15, 0, 30, 0, 0, time.UTC).UnixMilli()
	if input.Schedule.At != want || input.Schedule.Time != "" {
		t.Fatalf("one-time schedule = %+v, want %d", input.Schedule, want)
	}
}
