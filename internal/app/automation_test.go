package app_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vaayne/dex/internal/app"
)

type automationInfo struct {
	ID       string  `json:"id"`
	NextRuns []int64 `json:"nextRuns"`
}

type runInfo struct {
	ID     string `json:"id"`
	DueAt  int64  `json:"dueAt"`
	Status string `json:"status"`
	Reason string `json:"reason"`
	ChatID int64  `json:"chatId"`
}

func (d *dex) createAutomation(t *testing.T, name, prompt, session string, schedule map[string]any) automationInfo {
	t.Helper()
	var a automationInfo
	d.must(t, http.MethodPost, "/api/automations", map[string]any{
		"name": name, "prompt": prompt, "schedule": schedule, "timeZone": "Asia/Shanghai", "session": session,
		"enabled": true,
	}, &a, http.StatusCreated)
	return a
}

func (d *dex) runs(t *testing.T, id string) []runInfo {
	t.Helper()
	var list struct {
		Runs []runInfo `json:"runs"`
	}
	d.must(t, http.MethodGet, "/api/automations/"+id+"/runs", nil, &list)
	return list.Runs
}

func (d *dex) runNow(t *testing.T, id string) runInfo {
	t.Helper()
	var run runInfo
	d.must(t, http.MethodPost, "/api/automations/"+id+"/run", nil, &run, http.StatusAccepted)
	return run
}

func TestUserAutomationRunsAtItsTimeInANewChat(t *testing.T) {
	// Given dex that checks automations every 100 ms, and an automation for 1 s from now
	d := startDexWith(t, app.Config{DataDir: t.TempDir(), AutomationCheck: 100 * time.Millisecond})
	d.setModel(t, "echo")
	at := time.Now().Add(time.Second).UnixMilli()
	a := d.createAutomation(t, "Morning brief", "summarize the day", "new", map[string]any{"kind": "once", "at": at})
	if len(a.NextRuns) != 1 || a.NextRuns[0] != at {
		t.Fatalf("next runs = %v, want [%d]", a.NextRuns, at)
	}

	// When the time comes
	// Then the agent runs the prompt in a new automation chat with the automation's name
	var run runInfo
	eventually(t, "run", func() (bool, any) {
		runs := d.runs(t, a.ID)
		if len(runs) == 1 {
			run = runs[0]
		}
		return len(runs) == 1 && runs[0].ChatID != 0, runs
	})
	if run.DueAt != at {
		t.Fatalf("run due at %d, want %d", run.DueAt, at)
	}
	d.watch(t, run.ChatID).until(t, "answer", answered("echo: summarize the day"))
	eventually(t, "done run", func() (bool, any) {
		runs := d.runs(t, a.ID)
		return len(runs) == 1 && runs[0].Status == "done", runs
	})
	var chat struct {
		chatInfo
		AutomationID string `json:"automationId"`
	}
	d.must(t, http.MethodGet, fmt.Sprintf("/api/chats/%d", run.ChatID), nil, &chat)
	if chat.Channel != "automation" || chat.AutomationID != a.ID || chat.Title != "Morning brief" {
		t.Fatalf("chat = %+v", chat)
	}

	// And a once automation has no next run
	var after automationInfo
	d.must(t, http.MethodGet, "/api/automations/"+a.ID, nil, &after)
	if len(after.NextRuns) != 0 {
		t.Fatalf("next runs = %v", after.NextRuns)
	}
}

func TestUserAutomationReusesItsChatAndSkipsWhileBusy(t *testing.T) {
	// Given an automation that reuses its chat, with a prompt that takes 2 s
	d := startDexWith(t, app.Config{DataDir: t.TempDir()})
	d.setModel(t, "bash")
	a := d.createAutomation(t, "Slow check", "sleep 2; echo checked", "reuse",
		map[string]any{"kind": "daily", "time": "09:00"})

	// When the user runs it twice at once
	first := d.runNow(t, a.ID)
	second := d.runNow(t, a.ID)

	// Then the second run is skipped, because the chat is busy
	if first.Status != "running" || second.Status != "skipped" || !strings.Contains(second.Reason, "busy") {
		t.Fatalf("runs = %+v, %+v", first, second)
	}
	d.watch(t, first.ChatID).until(t, "first answer", answered("bash: checked"))

	// When the user runs it again
	third := d.runNow(t, a.ID)

	// Then it continues the same chat
	if third.ChatID != first.ChatID {
		t.Fatalf("third run chat = %d, want %d", third.ChatID, first.ChatID)
	}
	d.watch(t, first.ChatID).until(t, "third answer", func(tr *transcript) bool {
		n := 0
		for _, it := range tr.items {
			if it.Kind == "assistant" && it.Text == "bash: checked" {
				n++
			}
		}
		return !tr.running && n == 2
	})

	// And the user can reply in that chat
	d.send(t, first.ChatID, "echo reply")
	d.watch(t, first.ChatID).until(t, "reply", answered("bash: reply"))
}

func TestInvalidAutomationsAreRefused(t *testing.T) {
	d := startDexWith(t, app.Config{DataDir: t.TempDir()})
	valid := func() map[string]any {
		return map[string]any{
			"name": "n", "prompt": "p", "timeZone": "Asia/Shanghai", "session": "new", "enabled": true,
			"schedule": map[string]any{"kind": "daily", "time": "09:00"},
		}
	}
	for name, change := range map[string]func(map[string]any){
		"unknown time zone":  func(m map[string]any) { m["timeZone"] = "Mars/Base" },
		"short interval":     func(m map[string]any) { m["schedule"] = map[string]any{"kind": "every", "minutes": 1} },
		"bad time":           func(m map[string]any) { m["schedule"] = map[string]any{"kind": "daily", "time": "25:00"} },
		"weekly without day": func(m map[string]any) { m["schedule"] = map[string]any{"kind": "weekly", "time": "09:00"} },
		"once in the past":   func(m map[string]any) { m["schedule"] = map[string]any{"kind": "once", "at": 1} },
		"empty prompt":       func(m map[string]any) { m["prompt"] = " " },
	} {
		body := valid()
		change(body)
		if status, raw := d.call(t, http.MethodPost, "/api/automations", body); status != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, status, raw)
		}
	}
}
