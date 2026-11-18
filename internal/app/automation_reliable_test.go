package app_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/db"
)

type routineRun struct {
	runInfo
	Outcome    string `json:"outcome"`
	Summary    string `json:"summary"`
	ChangeKey  string `json:"changeKey"`
	Duplicate  bool   `json:"duplicate"`
	AdmittedAt *int64 `json:"admittedAt"`
	SettledAt  *int64 `json:"settledAt"`
}

func routineRuns(t *testing.T, d *dex, id string) []routineRun {
	t.Helper()
	var list struct {
		Runs []routineRun `json:"runs"`
	}
	d.must(t, http.MethodGet, "/api/agents/default/automations/"+id+"/runs", nil, &list)
	return list.Runs
}
func settledRoutine(t *testing.T, d *dex, id, runID string) routineRun {
	t.Helper()
	var found routineRun
	eventually(t, "structured routine result", func() (bool, any) {
		rows := routineRuns(t, d, id)
		for _, r := range rows {
			if r.ID == runID {
				found = r
				return r.Status != "running", r
			}
		}
		return false, rows
	})
	return found
}
func TestUserAutomationInputFailureCannotCommitARunWithoutItsMessage(t *testing.T) {
	t.Parallel()
	// Given an automation and a real SQLite failure at the input boundary.
	data := t.TempDir()
	d := startDexWith(t, app.Config{DataDir: data, AutomationCheck: time.Hour})
	d.setModel(t, "echo")
	a := d.createAutomation(t, "Atomic run", "check", "new", map[string]any{"kind": "daily", "time": "09:00"})
	conn, err := db.Open(t.Context(), filepath.Join(data, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = conn.ExecContext(t.Context(), `CREATE TRIGGER interrupt_automation_input BEFORE INSERT ON submissions WHEN NEW.origin = 'automation' BEGIN SELECT RAISE(ABORT, 'interrupted before input commit'); END`)
	if err != nil {
		t.Fatal(err)
	}
	// When input storage fails after the run insert.
	status, body := d.call(t, http.MethodPost, "/api/agents/default/automations/"+a.ID+"/run", nil)
	// Then no run can commit without a message intent.
	var count int
	if err = conn.QueryRowContext(t.Context(), "SELECT count(*) FROM automation_runs WHERE automation_id = ?", a.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("committed %d run(s) without an input intent: HTTP %d %s", count, status, body)
	}
	d.stop()
	if _, err = conn.ExecContext(t.Context(), "DROP TRIGGER interrupt_automation_input"); err != nil {
		t.Fatal(err)
	}
	// And after restart, one retry creates exactly one input and one run.
	d = startDexWith(t, app.Config{DataDir: data, AutomationCheck: time.Hour})
	run := d.runNow(t, a.ID)
	d.watch(t, run.ChatID).until(t, "recovered input", answered("echo: check"))
	settledRoutine(t, d, a.ID, run.ID)
	if err = conn.QueryRowContext(t.Context(), "SELECT count(*) FROM automation_runs WHERE automation_id = ?", a.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("retry produced %d runs", count)
	}
	if err = conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE id = ?", run.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("retry produced %d input intents", count)
	}
}
func TestUserAutomationReportsStructuredOutcomesAndDuplicates(t *testing.T) {
	t.Parallel()
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "tool")
	for _, outcome := range []string{"changed", "unchanged", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			// Given a routine that reports its result through the real tool boundary.
			key := "release-73-" + outcome
			prompt := fmt.Sprintf(`report_check {"outcome":%q,"summary":"fixture check completed","changeKey":%q}`, outcome, key)
			a := d.createAutomation(t, "Check", prompt, "new", map[string]any{"kind": "daily", "time": "09:00"})
			// When the routine completes.
			first := d.runNow(t, a.ID)
			r := settledRoutine(t, d, a.ID, first.ID)
			// Then the result is stored on that run.
			wantStatus := "done"
			if outcome == "failed" {
				wantStatus = "failed"
			}
			if r.Outcome != outcome || r.Summary != "fixture check completed" || r.Status != wantStatus || r.Duplicate {
				t.Fatalf("structured result = %+v", r)
			}
			if r.AdmittedAt == nil || r.SettledAt == nil || *r.SettledAt < *r.AdmittedAt {
				t.Fatalf("missing admission or settlement time: %+v", r)
			}
			// When a later run reports the same change key, it remains a separate run.
			second := d.runNow(t, a.ID)
			duplicate := settledRoutine(t, d, a.ID, second.ID)
			if !duplicate.Duplicate || duplicate.ChangeKey != key || duplicate.Outcome != outcome {
				t.Fatalf("duplicate result = %+v", duplicate)
			}
			if len(routineRuns(t, d, a.ID)) != 2 {
				t.Fatal("duplicate run was dropped")
			}
			// When a third run reports another outcome with the same key, it is not a duplicate.
			other := map[string]string{"changed": "unchanged", "unchanged": "changed", "failed": "changed"}[outcome]
			body := map[string]any{"name": "Check", "prompt": fmt.Sprintf(`report_check {"outcome":%q,"summary":"fixture check completed","changeKey":%q}`, other, key), "schedule": map[string]any{"kind": "daily", "time": "09:00"}, "timeZone": "Asia/Shanghai", "session": "new", "enabled": true}
			d.must(t, http.MethodPut, "/api/agents/default/automations/"+a.ID, body, nil)
			third := settledRoutine(t, d, a.ID, d.runNow(t, a.ID).ID)
			if third.Outcome != other || third.Duplicate {
				t.Fatalf("other outcome with the same key = %+v", third)
			}
		})
	}
}

func TestUserAutomationAppliesItsProjectOnEveryRun(t *testing.T) {
	t.Parallel()
	// Given a reusable routine with a project directory.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "bash")
	first, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	second, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"name": "Project check", "prompt": "pwd", "schedule": map[string]any{"kind": "daily", "time": "09:00"}, "timeZone": "Asia/Shanghai", "session": "reuse", "enabled": true, "cwd": first}
	var a automationInfo
	d.must(t, http.MethodPost, "/api/agents/default/automations", body, &a, http.StatusCreated)
	run := d.runNow(t, a.ID)
	d.watch(t, run.ChatID).until(t, "project cwd", answered("bash: "+first))
	settledRoutine(t, d, a.ID, run.ID)
	// When the project changes before the next run.
	body["cwd"] = second
	d.must(t, http.MethodPut, "/api/agents/default/automations/"+a.ID, body, nil)
	next := d.runNow(t, a.ID)
	// Then the same chat uses the new project.
	if next.ChatID != run.ChatID {
		t.Fatal("routine did not reuse its chat")
	}
	d.watch(t, next.ChatID).until(t, "updated project cwd", answered("bash: "+second))
	settledRoutine(t, d, a.ID, next.ID)
	// And clearing the override restores the agent workspace on the same chat.
	body["cwd"] = ""
	d.must(t, http.MethodPut, "/api/agents/default/automations/"+a.ID, body, nil)
	cleared := d.runNow(t, a.ID)
	d.watch(t, cleared.ChatID).until(t, "workspace restored", func(tr *transcript) bool {
		for _, item := range tr.items {
			if item.Kind == "assistant" && strings.HasPrefix(item.Text, "bash: ") && strings.HasSuffix(item.Text, "/agents/default/workspace") {
				return true
			}
		}
		return false
	})
}

func TestUserAutomationRecoversCommittedInputWithoutRepeatingScheduledRun(t *testing.T) {
	t.Parallel()
	// Given a crash boundary after the input commit but before the schedule advances.
	data := t.TempDir()
	d := startDexWith(t, app.Config{DataDir: data, AutomationCheck: 100 * time.Millisecond})
	d.setModel(t, "bash")
	d.must(t, http.MethodPost, "/api/maintenance", nil, nil)
	marker := filepath.Join(data, "executions.txt")
	at := time.Now().Add(time.Second).UnixMilli()
	a := d.createAutomation(t, "Recover", fmt.Sprintf("printf 'once\\n' >> %q; echo recovered", marker), "new", map[string]any{"kind": "once", "at": at})
	conn, err := db.Open(t.Context(), filepath.Join(data, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = conn.ExecContext(t.Context(), `CREATE TRIGGER interrupt_schedule_advance BEFORE UPDATE OF next_due_at ON automations BEGIN SELECT RAISE(ABORT, 'interrupted after input commit'); END`)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "committed unadmitted run", func() (bool, any) {
		var n int
		err := conn.QueryRowContext(t.Context(), `SELECT count(*) FROM automation_runs r JOIN submissions s ON s.id=r.id WHERE r.automation_id=? AND s.admitted_at IS NULL`, a.ID).Scan(&n)
		return err == nil && n == 1, fmt.Sprintf("count=%d err=%v", n, err)
	})
	// When dex restarts with the scheduled time still due.
	d.stop()
	if _, err = conn.ExecContext(t.Context(), "DROP TRIGGER interrupt_schedule_advance"); err != nil {
		t.Fatal(err)
	}
	d = startDexWith(t, app.Config{DataDir: data, AutomationCheck: 100 * time.Millisecond})
	eventually(t, "recovered scheduled input", func() (bool, any) {
		rows := routineRuns(t, d, a.ID)
		var pending int
		err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM automations WHERE id=? AND next_due_at IS NOT NULL", a.ID).Scan(&pending)
		return err == nil && pending == 0 && len(rows) == 1 && rows[0].Status != "running", rows
	})
	// Then one durable input runs once, even though scheduling rechecks the same due time.
	raw, err := os.ReadFile(marker)
	if err != nil || string(raw) != "once\n" {
		t.Fatalf("executions=%q err=%v", raw, err)
	}
	var n int
	if err = conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE origin='automation'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("input intents=%d err=%v", n, err)
	}
}

func TestUserAutomationExecutionErrorOverridesItsReport(t *testing.T) {
	t.Parallel()
	// Given a provider that reports success, then fails on its next model turn.
	var requests atomic.Int32
	boundary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) > 1 {
			http.Error(w, "fixture provider rejected the next turn", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintln(w, `data: {"id":"check","object":"chat.completion.chunk","created":1,"model":"routine","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"check_1","type":"function","function":{"name":"report_check","arguments":"{\"outcome\":\"unchanged\",\"summary\":\"provisional success\"}"}}]},"finish_reason":null}]}`)
		_, _ = fmt.Fprint(w, "\n"+`data: {"id":"check","object":"chat.completion.chunk","created":1,"model":"routine","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\ndata: [DONE]\n")
	}))
	defer boundary.Close()
	d := startDex(t, t.TempDir(), "")
	d.must(t, http.MethodPut, "/api/settings/providers/routine", map[string]any{"api": "openai-completions", "baseUrl": boundary.URL + "/v1", "models": []string{"routine"}}, nil)
	d.must(t, http.MethodPut, "/api/agents/default", map[string]string{"name": "Dex", "provider": "routine", "modelId": "routine", "thinkingLevel": "off", "instructions": ""}, nil)
	a := d.createAutomation(t, "Error after report", "run the check", "new", map[string]any{"kind": "daily", "time": "09:00"})
	// When the provider fails after report_check succeeds.
	run := d.runNow(t, a.ID)
	result := settledRoutine(t, d, a.ID, run.ID)
	// Then the provisional unchanged report cannot conceal an execution failure.
	if result.Outcome != "failed" || result.Status != "failed" || result.Summary == "" || result.Summary == "provisional success" {
		t.Fatalf("execution error result=%+v", result)
	}
	d.watch(t, run.ChatID).until(t, "successful provisional report", func(tr *transcript) bool {
		for _, item := range tr.items {
			if item.Kind == "tool" && item.Name == "report_check" && item.Status == "done" {
				return true
			}
		}
		return false
	})
}
