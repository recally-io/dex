package app_test

import (
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/db"
)

type inboxItem struct {
	RunID          string `json:"runId"`
	AgentID        string `json:"agentId"`
	AgentName      string `json:"agentName"`
	AutomationID   string `json:"automationId"`
	AutomationName string `json:"automationName"`
	ChatID         int64  `json:"chatId"`
	Outcome        string `json:"outcome"`
	Summary        string `json:"summary"`
	Read           bool   `json:"read"`
	Dismissed      bool   `json:"dismissed"`
}

func inbox(t *testing.T, d *dex, path string) []inboxItem {
	t.Helper()
	var list struct {
		Items []inboxItem `json:"items"`
	}
	d.must(t, http.MethodGet, path, nil, &list)
	if list.Items == nil {
		t.Fatal("inbox must return an array")
	}
	return list.Items
}
func TestUserCanHandleFailedRunWithoutTranscript(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d := startDexWith(t, app.Config{DataDir: dir, AutomationCheck: time.Hour})
	var a automationInfo
	d.must(t, http.MethodPost, "/api/agents/default/automations", map[string]any{"name": "Failed run", "prompt": "Check", "schedule": map[string]string{"kind": "daily", "time": "09:00"}, "timeZone": "UTC", "session": "new", "enabled": true}, &a, http.StatusCreated)
	conn, err := db.Open(t.Context(), filepath.Join(dir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(t.Context(), `INSERT INTO automation_runs(id,automation_id,status,reason,created_at) VALUES ('failed-before-input',?,'failed','dex stopped',1)`, a.ID); err != nil {
		t.Fatal(err)
	}
	// When the Owner opens Inbox, a failed run without a transcript is visible.
	items := inbox(t, d, "/api/inbox")
	if len(items) != 1 || items[0].RunID != "failed-before-input" || items[0].Outcome != "failed" {
		t.Fatalf("failed run missing: %+v", items)
	}
	d.must(t, http.MethodPost, "/api/inbox/failed-before-input/dismiss", nil, nil)
	if got := inbox(t, d, "/api/inbox"); len(got) != 0 {
		t.Fatalf("handled run still visible: %+v", got)
	}
}

func TestUserInboxCollectsOnlyActionableResultsAcrossBots(t *testing.T) {
	t.Parallel()
	// Given two bots with real tool routines and an old run without an outcome.
	data := t.TempDir()
	d := startDexWith(t, app.Config{DataDir: data, AutomationCheck: time.Hour})
	d.setModel(t, "tool")
	var bot struct {
		ID string `json:"id"`
	}
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "Second bot", "provider": "faux", "modelId": "tool", "thinkingLevel": "off"}, &bot, http.StatusCreated)
	for _, agent := range []string{"default", bot.ID} {
		for _, outcome := range []string{"changed", "failed", "unchanged"} {
			var a automationInfo
			d.must(t, http.MethodPost, "/api/agents/"+agent+"/automations", map[string]any{"name": outcome + " check", "prompt": fmt.Sprintf(`automation_report {"outcome":%q,"summary":"check result","changeKey":"same"}`, outcome), "schedule": map[string]string{"kind": "daily", "time": "09:00"}, "timeZone": "UTC", "session": "new", "enabled": true}, &a, http.StatusCreated)
			for range 2 {
				var r runInfo
				d.must(t, http.MethodPost, "/api/agents/"+agent+"/automations/"+a.ID+"/run", nil, &r, http.StatusAccepted)
				eventually(t, "routine settled", func() (bool, any) {
					var list struct {
						Runs []routineRun `json:"runs"`
					}
					d.must(t, http.MethodGet, "/api/agents/"+agent+"/automations/"+a.ID+"/runs", nil, &list)
					for _, row := range list.Runs {
						if row.ID == r.ID {
							return row.Status != "running", row
						}
					}
					return false, list
				})
			}
		}
	}
	conn, err := db.Open(t.Context(), filepath.Join(data, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(t.Context(), `INSERT INTO automation_runs(id,automation_id,status,created_at) SELECT 'legacy',id,'started',1 FROM automations LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	// When the owner opens the global Inbox.
	rows := inbox(t, d, "/api/inbox")
	// Then both bots have one changed result and both failed results, including repeated failures.
	if len(rows) != 6 {
		t.Fatalf("want 6 actionable results, got %+v", rows)
	}
	counts := map[string]int{}
	for _, r := range rows {
		counts[r.AgentID]++
		if r.ChatID == 0 || r.Summary != "check result" || r.Read || r.Dismissed || r.AgentName == "" || r.AutomationName == "" || (r.Outcome != "changed" && r.Outcome != "failed") {
			t.Fatalf("invalid inbox result: %+v", r)
		}
	}
	if counts["default"] != 3 || counts[bot.ID] != 3 {
		t.Fatalf("bot counts: %v", counts)
	}
	var response struct {
		Agents []struct {
			ID       string `json:"id"`
			Activity struct {
				AutomationFailed bool `json:"automationFailed"`
			} `json:"activity"`
		} `json:"agents"`
	}
	d.must(t, http.MethodGet, "/api/agents", nil, &response)
	for _, agent := range response.Agents {
		if !agent.Activity.AutomationFailed {
			t.Fatalf("agent %s activity=%+v, want automationFailed=true", agent.ID, agent.Activity)
		}
	}
	// When the owner reads and dismisses one bot's results, repeated writes are safe.
	target := rows[0]
	for range 2 {
		d.must(t, http.MethodPost, "/api/inbox/"+target.RunID+"/read", nil, nil)
		d.must(t, http.MethodPost, "/api/inbox/"+target.RunID+"/dismiss", nil, nil)
	}
	// Then read and dismissed state survive restart and other results stay unread.
	d.stop()
	d = startDexWith(t, app.Config{DataDir: data, AutomationCheck: time.Hour})
	if got := inbox(t, d, "/api/inbox"); len(got) != 5 {
		t.Fatalf("dismissed result visible: %+v", got)
	}
	all := inbox(t, d, "/api/inbox?includeDismissed=true")
	if len(all) != 6 {
		t.Fatalf("all results: %+v", all)
	}
	for _, r := range all {
		if r.RunID == target.RunID {
			if !r.Read || !r.Dismissed {
				t.Fatalf("lost state: %+v", r)
			}
		} else if r.Read || r.Dismissed {
			t.Fatalf("another result changed: %+v", r)
		}
	}
	for _, id := range []string{"missing", "legacy"} {
		for _, action := range []string{"read", "dismiss"} {
			status, body := d.call(t, http.MethodPost, "/api/inbox/"+id+"/"+action, nil)
			if status != http.StatusNotFound {
				t.Fatalf("%s %s: %d %s", id, action, status, body)
			}
		}
	}
}
