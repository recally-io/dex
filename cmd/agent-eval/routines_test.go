//go:build agenteval

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/automation"
)

func routineEvalClient(t *testing.T, model string) (client, chat, string) {
	t.Helper()
	home := t.TempDir()
	a, err := app.Start(t.Context(), app.Config{DataDir: t.TempDir(), Home: home, FakeModel: true, ToolEnv: map[string]string{"PATH": os.Getenv("PATH"), "HOME": home}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Error(err)
		}
	})
	server := httptest.NewServer(a.Handler())
	t.Cleanup(server.Close)
	c := client{base: server.URL, ctx: t.Context()}
	if err := c.request("PUT", "/api/agents/default", map[string]any{"name": "Eval", "provider": "faux", "modelId": model, "thinkingLevel": "off", "instructions": ""}, nil); err != nil {
		t.Fatal(err)
	}
	var parent chat
	if err := c.request("POST", "/api/agents/default/chats", nil, &parent); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	if err := c.setProject(fmt.Sprintf("/api/agents/default/chats/%d", parent.ID), project); err != nil {
		t.Fatal(err)
	}
	return c, parent, project
}

func createEvalRoutine(t *testing.T, c client, project, prompt string) automation.Automation {
	t.Helper()
	var saved automation.Automation
	in := automation.Input{Name: "Eval local marker check", Prompt: prompt, Cwd: project, Schedule: automation.Schedule{Kind: "every", Minutes: 10020}, TimeZone: "UTC", Session: "new", Enabled: false}
	if err := c.request("POST", automationsPath, in, &saved); err != nil {
		t.Fatal(err)
	}
	return saved
}

func requireCheck(t *testing.T, checks []check, name string, pass bool) {
	t.Helper()
	for _, check := range checks {
		if check.Name == name {
			if check.Pass != pass {
				t.Fatalf("%s pass=%t, want %t: %s", name, check.Pass, pass, check.Detail)
			}
			return
		}
	}
	t.Fatalf("missing check %q", name)
}

func TestUserEvalValidatesSavedRoutineInsteadOfAnswerClaims(t *testing.T) {
	// Given an isolated routine with a real durable report from the faux tool provider.
	c, parent, project := routineEvalClient(t, "tool")
	saved := createEvalRoutine(t, c, project, `report_check {"outcome":"unchanged","summary":"ROUTINE-LOCAL-7491"}`)
	var started automation.Run
	if err := c.request("POST", automationsPath+"/"+saved.ID+"/run", nil, &started); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	c.ctx = ctx

	// When the eval waits for work and validates the HTTP readback.
	ids, states, err := c.waitForWork(parent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ids, started.ChatID) {
		t.Fatal("routine transcript was not included")
	}
	want := routineExpectation{Name: saved.Name, Schedule: saved.Schedule, Enabled: false, Outcome: "unchanged", SummaryContains: "ROUTINE-LOCAL-7491"}
	answer := saved.ID + " unchanged ROUTINE-LOCAL-7491 " + started.ID
	checks, err := c.checkRoutine(want, states, project, answer)
	if err != nil {
		t.Fatal(err)
	}

	// Then all checks use saved state, and transcript links are accepted as real run references.
	for _, check := range checks {
		if !check.Pass {
			t.Errorf("%s: %s", check.Name, check.Detail)
		}
	}
	linked := fmt.Sprintf("%s unchanged ROUTINE-LOCAL-7491 /a/default/c/%d", saved.ID, started.ChatID)
	checks, err = c.checkRoutine(want, states, project, linked)
	if err != nil {
		t.Fatal(err)
	}
	requireCheck(t, checks, "answer references saved run", true)

	// A convincing answer cannot repair a saved schedule, pause state, cwd, or report mismatch.
	wrong := want
	wrong.Schedule.Minutes = 10080
	wrong.Enabled = true
	wrong.Outcome = "changed"
	wrong.SummaryContains = "invented"
	checks, err = c.checkRoutine(wrong, states, project+"/wrong", answer)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"saved routine schedule", "saved routine enabled", "routine inherited project", "routine run inherited project", "routine run reported outcome", "routine run reported summary"} {
		requireCheck(t, checks, name, false)
	}
	checks, err = c.checkRoutine(want, states, project, saved.ID+" unchanged ROUTINE-LOCAL-7491 invented-run-id")
	if err != nil {
		t.Fatal(err)
	}
	requireCheck(t, checks, "answer references saved run", false)

	// A duplicate in durable storage fails even if the first routine and the answer are correct.
	createEvalRoutine(t, c, project, "unused")
	states, err = c.routines()
	if err != nil {
		t.Fatal(err)
	}
	checks, err = c.checkRoutine(want, states, project, answer)
	if err != nil {
		t.Fatal(err)
	}
	requireCheck(t, checks, "exactly one saved routine", false)
}

func TestUserEvalWaitsForRoutineAfterParentIsIdle(t *testing.T) {
	// Given an idle parent and a routine blocked on a local FIFO, without a provider request.
	c, parent, project := routineEvalClient(t, "bash")
	gate := filepath.Join(project, "gate")
	if err := syscall.Mkfifo(gate, 0o600); err != nil {
		t.Fatal(err)
	}
	pipe, err := os.OpenFile(gate, os.O_RDWR, 0) //nolint:gosec // The test owns this FIFO.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pipe.Close() })
	released := false
	release := func() {
		if !released {
			_, _ = pipe.WriteString("continue\n")
			released = true
		}
	}
	t.Cleanup(release)
	saved := createEvalRoutine(t, c, project, "read -r marker < gate; printf '%s' \"$marker\"")
	var started automation.Run
	if err := c.request("POST", automationsPath+"/"+saved.ID+"/run", nil, &started); err != nil {
		t.Fatal(err)
	}

	// When the wait deadline expires before the routine exits.
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	c.ctx = ctx
	ids, states, err := c.waitForWork(parent.ID, true)

	// Then the wait fails with the active run ID and state, even though the parent is idle.
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), started.ID) || !strings.Contains(err.Error(), "running") {
		t.Fatalf("wait error = %v, want deadline and active run details", err)
	}
	if len(states) != 1 || len(states[0].Runs) != 1 || !slices.Contains(ids, started.ChatID) {
		t.Fatalf("missing observed routine/run or transcript: %+v, chats %v", states, ids)
	}

	// Once the run exits, the wait completes and a missing report stays a failed outcome.
	release()
	ctx, stop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stop()
	c.ctx = ctx
	_, states, err = c.waitForWork(parent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	checks, err := c.checkRoutine(routineExpectation{Name: saved.Name, Schedule: saved.Schedule, Outcome: "unchanged", SummaryContains: "continue"}, states, project, saved.ID+" "+started.ID+" unchanged continue")
	if err != nil {
		t.Fatal(err)
	}
	requireCheck(t, checks, "routine run reported outcome", false)
}
