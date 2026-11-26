package app_test

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/automation"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/runner/runnertest"
)

func routineToolJSON(t *testing.T, d *dex, sessionID int64, name string, args any, out any) {
	t.Helper()
	got := taskTool(t, d, sessionID, name, args)
	if !strings.HasPrefix(got, "tool: ") {
		t.Fatalf("%s: %s", name, got)
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(got, "tool: ")), out); err != nil {
		t.Fatalf("%s: %s: %v", name, got, err)
	}
}

func TestUserManagesAutomationsThroughSession(t *testing.T) {
	t.Parallel()
	// Given a writable Session with a project selected.
	d := startDex(t, t.TempDir(), "")
	id := d.newSession(t).ID
	project := t.TempDir()
	d.must(t, http.MethodPut, projectURL(id), map[string]string{"cwd": project}, nil)
	// When the user creates a routine, omitted cwd inherits the Session project.
	var created automation.ToolInfo
	routineToolJSON(t, d, id, "automation_create", map[string]any{"name": "Morning check", "prompt": "automation_report {\"outcome\":\"changed\",\"summary\":\"New result\",\"changeKey\":\"same-result\"}", "schedule": map[string]any{"kind": "every", "minutes": 30}, "timeZone": "UTC", "enabled": true, "session": "new"}, &created)
	if created.ID == "" || !created.Enabled || created.Cwd != project || created.TimeZone != "UTC" || len(created.NextRuns) == 0 {
		t.Fatalf("created: %+v", created)
	}
	// Then partial updates keep the saved prompt and change only requested state.
	var renamed automation.ToolInfo
	routineToolJSON(t, d, id, "automation_update", map[string]any{"id": created.ID, "name": "Renamed check", "prompt": created.Prompt}, &renamed)
	if renamed.Name != "Renamed check" || !slices.Equal(renamed.NextRuns, created.NextRuns) || renamed.UpdatedAt <= created.UpdatedAt {
		t.Fatalf("non-scheduling edit: before=%+v after=%+v", created, renamed)
	}
	for _, enabled := range []bool{false, true} {
		var updated automation.ToolInfo
		routineToolJSON(t, d, id, "automation_update", map[string]any{"id": created.ID, "enabled": enabled}, &updated)
		if updated.Enabled != enabled || updated.Prompt != created.Prompt {
			t.Fatalf("updated: %+v", updated)
		}
		if !enabled && len(updated.NextRuns) != 0 {
			t.Fatalf("paused next runs: %+v", updated)
		}
	}
	var updated automation.ToolInfo
	routineToolJSON(t, d, id, "automation_update", map[string]any{"id": created.ID, "schedule": map[string]any{"kind": "weekly", "days": []int{1, 5}, "time": "09:00"}, "timeZone": "Asia/Shanghai"}, &updated)
	if updated.Schedule.Kind != "weekly" || updated.TimeZone != "Asia/Shanghai" || updated.Prompt != created.Prompt {
		t.Fatalf("schedule update: %+v", updated)
	}
	// When the user runs a paused routine now, get exposes the saved outcome and transcript reference.
	routineToolJSON(t, d, id, "automation_update", map[string]any{"id": created.ID, "enabled": false}, &updated)
	var run automation.ToolRun
	routineToolJSON(t, d, id, "automation_run", map[string]string{"id": created.ID}, &run)
	if run.ID == "" || run.SessionID == 0 {
		t.Fatalf("run: %+v", run)
	}
	eventually(t, "routine report", func() (bool, any) {
		var runs struct {
			Runs []automation.Run `json:"runs"`
		}
		d.must(t, http.MethodGet, fmt.Sprintf("/api/bots/default/automations/%s/runs", created.ID), nil, &runs)
		return len(runs.Runs) > 0 && runs.Runs[0].Status == "done", runs
	})
	var detail automation.ToolInfo
	routineToolJSON(t, d, id, "automation_get", map[string]string{"id": created.ID}, &detail)
	if len(detail.Runs) != 1 || detail.Runs[0].ID != run.ID || detail.Runs[0].SessionID != run.SessionID || detail.Runs[0].Summary != "New result" {
		t.Fatalf("detail: %+v", detail)
	}
	// And main can list routines created in Session for the same bot.
	var listed struct {
		Automations []automation.ToolInfo `json:"automations"`
	}
	routineToolJSON(t, d, d.mainSession(t), "automation_list", map[string]any{}, &listed)
	if len(listed.Automations) != 1 || listed.Automations[0].ID != created.ID || listed.Automations[0].LastRun == nil {
		t.Fatalf("list: %+v", listed)
	}
	// A second requested execution can report the same finding.
	var second automation.ToolRun
	routineToolJSON(t, d, id, "automation_run", map[string]string{"id": created.ID}, &second)
	settledRoutine(t, d, created.ID, second.ID)
	var findings struct {
		Runs []struct {
			ID              string `json:"id"`
			RepeatedFinding bool   `json:"repeatedFinding"`
		} `json:"runs"`
	}
	routineToolJSON(t, d, id, "automation_get", map[string]string{"id": created.ID}, &findings)
	if len(findings.Runs) != 2 || findings.Runs[0].ID != second.ID || second.ID == run.ID || !findings.Runs[0].RepeatedFinding || findings.Runs[1].RepeatedFinding {
		t.Fatalf("execution count and repeated finding: %+v", findings)
	}
	t.Logf("routine=%s run=%s transcript session=%d repeated finding run=%s", created.ID, run.ID, run.SessionID, second.ID)
}

// savedRoutineCaller reads the business receipt of a real native tool invocation.
func savedRoutineCaller(t *testing.T, conn *sql.DB, action string) engine.TaskContext {
	t.Helper()
	var c engine.TaskContext
	err := conn.QueryRowContext(t.Context(), `SELECT o.bot_id,c.execution_id,o.source_request_id,o.key FROM automation_tool_operations o JOIN sessions c ON c.id=o.session_id WHERE action=? ORDER BY o.rowid DESC LIMIT 1`, action).Scan(&c.BotID, &c.ConversationID, &c.SourceRequestID, &c.Key)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestUserAutomationToolReplayReturnsCommittedMutationsAfterRestart(t *testing.T) {
	t.Parallel()
	// Given completed native tool calls with durable business receipts.
	data := t.TempDir()
	d := startDex(t, data, "")
	id := d.mainSession(t)
	var created, updated automation.ToolInfo
	args := map[string]any{"name": "Replay check", "prompt": "automation_report {\"outcome\":\"unchanged\",\"summary\":\"Checked\"}", "schedule": map[string]any{"kind": "every", "minutes": 30}, "timeZone": "UTC"}
	routineToolJSON(t, d, id, "automation_create", args, &created)
	routineToolJSON(t, d, id, "automation_update", map[string]any{"id": created.ID, "schedule": map[string]any{"kind": "every", "minutes": 45}}, &updated)
	var run automation.ToolRun
	routineToolJSON(t, d, id, "automation_run", map[string]string{"id": created.ID}, &run)
	settledRoutine(t, d, created.ID, run.ID)
	conn := sessionDB(t, d)
	createSource := savedRoutineCaller(t, conn, "create")
	updateSource := savedRoutineCaller(t, conn, "update")
	runSource := savedRoutineCaller(t, conn, "run")
	d.stop()
	startDex(t, data, "")
	// When durable execution retries the same identity after the result boundary,
	// a fresh business service uses the committed operation, even after settlement.
	service := automation.New(sqlc.New(conn), nil, time.Now) // Read service; mutations bind the real transactional input writer.
	tools := automation.NewToolService(conn, service, func(int64) {})
	for range 2 {
		replay, err := tools.Create(t.Context(), createSource, automation.ToolPatch{})
		if err != nil || replay.ID != created.ID {
			t.Fatalf("create replay: %+v %v", replay, err)
		}
		edit, err := tools.Update(t.Context(), updateSource, automation.ToolPatch{ID: created.ID, Schedule: &automation.ToolSchedule{Kind: "every", Minutes: 45}})
		if err != nil || !slices.Equal(edit.NextRuns, updated.NextRuns) || edit.UpdatedAt != updated.UpdatedAt {
			t.Fatalf("update replay: %+v %v", edit, err)
		}
		rerun, err := tools.Run(t.Context(), runSource, created.ID)
		if err != nil || rerun.ID != run.ID || rerun.SessionID != run.SessionID {
			t.Fatalf("run replay: %+v %v", rerun, err)
		}
	}
	// Then there remains exactly one routine and one run/input intent.
	for _, query := range []string{"SELECT count(*) FROM automations", "SELECT count(*) FROM automation_runs", "SELECT count(*) FROM submissions WHERE origin='automation'"} {
		var n int
		if err := conn.QueryRowContext(t.Context(), query).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s: %d %v", query, n, err)
		}
	}
	t.Logf("replayed create=%s update=%s run=%s after restart; counts=1/1/1", createSource.Key, updateSource.Key, runSource.Key)
}

func TestUserAutomationBusinessBoundaryRejectsUntrustedSources(t *testing.T) {
	t.Parallel()
	// Given an actual user tool invocation and its persisted source authority.
	data := t.TempDir()
	d := startDex(t, data, "")
	id := d.mainSession(t)
	var a automation.ToolInfo
	routineToolJSON(t, d, id, "automation_create", map[string]any{"name": "Scoped", "prompt": "check", "schedule": map[string]any{"kind": "every", "minutes": 30}, "timeZone": "UTC"}, &a)
	conn := sessionDB(t, d)
	source := savedRoutineCaller(t, conn, "create")
	tools := automation.NewToolService(conn, automation.New(sqlc.New(conn), nil, time.Now), func(int64) {})
	// When another bot, execution, input kind, or hidden conversation claims that source,
	// every business operation rejects it, including reads and replay.
	reject := func(c engine.TaskContext) {
		t.Helper()
		if _, err := tools.List(t.Context(), c); err == nil {
			t.Fatal("list accepted untrusted source")
		}
		if _, err := tools.Get(t.Context(), c, a.ID); err == nil {
			t.Fatal("get accepted untrusted source")
		}
		if _, err := tools.Create(t.Context(), c, automation.ToolPatch{}); err == nil {
			t.Fatal("create accepted untrusted source")
		}
		if _, err := tools.Update(t.Context(), c, automation.ToolPatch{ID: a.ID, Enabled: new(false)}); err == nil {
			t.Fatal("update accepted untrusted source")
		}
		if _, err := tools.Run(t.Context(), c, a.ID); err == nil {
			t.Fatal("run accepted untrusted source")
		}
	}
	wrong := source
	wrong.BotID = "other"
	reject(wrong)
	wrong = source
	wrong.ConversationID++
	reject(wrong)
	for _, kind := range []string{"session_result", "wake", "task_wake", "context_reset"} {
		if _, err := conn.ExecContext(t.Context(), "UPDATE submissions SET kind=? WHERE id=?", kind, source.SourceRequestID); err != nil {
			t.Fatal(err)
		}
		reject(source)
	}
	if _, err := conn.ExecContext(t.Context(), "UPDATE submissions SET kind='message' WHERE id=?", source.SourceRequestID); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"automation", "task", "dream"} {
		if _, err := conn.ExecContext(t.Context(), "UPDATE submissions SET origin=? WHERE id=?", origin, source.SourceRequestID); err != nil {
			t.Fatal(err)
		}
		reject(source)
	}
	if _, err := conn.ExecContext(t.Context(), "UPDATE submissions SET origin='web' WHERE id=?", source.SourceRequestID); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"automation:boundary", "task:boundary", "dream:boundary", "telegram:7"} {
		if _, err := conn.ExecContext(t.Context(), "UPDATE sessions SET channel_key=? WHERE id=?", key, id); err != nil {
			t.Fatal(err)
		}
		reject(source)
	}
}

func TestUserAutomationMutationFailurePreservesCauseAndRollsBack(t *testing.T) {
	t.Parallel()
	// Given a real SQLite failure after a routine mutation but before its replay receipt.
	d := startDex(t, t.TempDir(), "")
	id := d.mainSession(t)
	conn := sessionDB(t, d)
	args := map[string]any{"name": "Atomic", "prompt": "check", "schedule": map[string]any{"kind": "every", "minutes": 30}, "timeZone": "UTC"}
	fail := func(action string) {
		t.Helper()
		_, err := conn.ExecContext(t.Context(), `CREATE TRIGGER fail_routine_receipt BEFORE INSERT ON automation_tool_operations WHEN NEW.action='`+action+`' BEGIN SELECT RAISE(ABORT,'routine receipt write failed'); END`)
		if err != nil {
			t.Fatal(err)
		}
	}
	dropFailure := func() {
		t.Helper()
		if _, err := conn.ExecContext(t.Context(), "DROP TRIGGER fail_routine_receipt"); err != nil {
			t.Fatal(err)
		}
	}
	fail("create")
	got := taskTool(t, d, id, "automation_create", args)
	if !strings.Contains(got, "routine receipt write failed") {
		t.Fatalf("cause lost: %s", got)
	}
	var n int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM automations").Scan(&n); err != nil || n != 0 {
		t.Fatalf("partial create: %d %v", n, err)
	}
	dropFailure()
	var a automation.ToolInfo
	routineToolJSON(t, d, id, "automation_create", args, &a)
	// When a run receipt fails, neither the run nor its durable input can survive.
	fail("run")
	got = taskTool(t, d, id, "automation_run", map[string]string{"id": a.ID})
	if !strings.Contains(got, "routine receipt write failed") {
		t.Fatalf("run cause lost: %s", got)
	}
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM automation_runs").Scan(&n); err != nil || n != 0 {
		t.Fatalf("partial run: %d %v", n, err)
	}
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE origin='automation'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("partial input: %d %v", n, err)
	}
	dropFailure()
	// Then an update receipt failure also leaves the original scheduling state intact.
	fail("update")
	got = taskTool(t, d, id, "automation_update", map[string]any{"id": a.ID, "enabled": false})
	if !strings.Contains(got, "routine receipt write failed") {
		t.Fatalf("update cause lost: %s", got)
	}
	var detail automation.ToolInfo
	routineToolJSON(t, d, id, "automation_get", map[string]string{"id": a.ID}, &detail)
	if !detail.Enabled || !slices.Equal(detail.NextRuns, a.NextRuns) {
		t.Fatalf("partial update: %+v", detail)
	}
}

func TestUserAutomationOtherBotCannotReadOrChangeAutomation(t *testing.T) {
	t.Parallel()
	// Given a routine owned by the first bot and a second bot using native tools.
	d := startDex(t, t.TempDir(), "")
	id := d.mainSession(t)
	var a automation.ToolInfo
	routineToolJSON(t, d, id, "automation_create", map[string]any{"name": "Private routine", "prompt": "check", "schedule": map[string]any{"kind": "every", "minutes": 30}, "timeZone": "UTC"}, &a)
	var beta apiBot
	d.must(t, http.MethodPost, "/api/bots", map[string]string{"name": "Beta", "provider": "faux", "modelId": "tool", "thinkingLevel": "off"}, &beta, http.StatusCreated)
	var main sessionListItem
	d.must(t, http.MethodGet, "/api/bots/"+beta.ID+"/sessions/main", nil, &main)
	conn := sessionDB(t, d)
	// When Beta attempts to inspect or mutate the first bot's ID, ownership remains enforced.
	for _, name := range []string{"automation_list", "automation_get", "automation_update", "automation_run"} {
		args := map[string]any{"id": a.ID}
		if name == "automation_list" {
			delete(args, "id")
		}
		if name == "automation_update" {
			args["enabled"] = false
		}
		request := uuid.NewV7().String()
		d.must(t, http.MethodPost, fmt.Sprintf("/api/bots/%s/sessions/%d/messages", beta.ID, main.ID), map[string]string{"id": request, "text": name + " " + taskJSON(t, args)}, nil, http.StatusAccepted)
		var status, answer string
		eventually(t, "Beta "+name, func() (bool, any) {
			err := conn.QueryRowContext(t.Context(), "SELECT status,answer FROM submissions WHERE id=?", request).Scan(&status, &answer)
			return err == nil && status == "done", fmt.Sprintf("status=%s answer=%s err=%v", status, answer, err)
		})
		if name == "automation_list" {
			if strings.Contains(answer, a.ID) || !strings.Contains(answer, `"automations":[]`) {
				t.Fatalf("leaked list: %s", answer)
			}
		} else if !strings.Contains(answer, "not found") {
			t.Fatalf("%s crossed bots: %s", name, answer)
		}
	}
	var detail automation.ToolInfo
	routineToolJSON(t, d, id, "automation_get", map[string]string{"id": a.ID}, &detail)
	if !detail.Enabled {
		t.Fatal("other bot paused routine")
	}
}

func TestUserAutomationToolDoesNotRunInASessionSelectedDuringPreflight(t *testing.T) {
	t.Parallel()
	// Given a Routine and two eligible sessions on the same synthetic SSH environment.
	ssh := runnertest.NewFakeSSH(t)
	d := startDexWith(t, app.Config{DataDir: t.TempDir(), SSHProgram: ssh.SSHProgram})
	d.setModel(t, "bash")
	main := d.mainSession(t)
	first := sessionFixture(t, d, "chat:first-fixture", 1)
	second := sessionFixture(t, d, "chat:second-fixture", 2)
	conn := sessionDB(t, d)
	var remote apiRunner
	d.must(t, http.MethodPost, "/api/settings/runners", map[string]any{
		"name": "fixture box", "kind": "ssh", "config": map[string]string{"host": "fixture.internal", "user": "dev"},
	}, &remote, http.StatusCreated)
	for _, target := range []int64{first, second} {
		d.must(t, http.MethodPut, fmt.Sprintf("/api/bots/default/sessions/%d/runner", target), map[string]string{"runnerId": remote.ID}, nil)
	}
	body := boundRoutine(first, "report the fixture")
	var routine automationInfo
	d.must(t, http.MethodPost, routinesPath, body, &routine, http.StatusCreated)
	gate := t.TempDir()
	id := d.send(t, main, taskGate(t, gate, "caller"))
	waitTaskFile(t, filepath.Join(gate, "caller.pid"))
	var executionID int64
	if err := conn.QueryRowContext(t.Context(), "SELECT execution_id FROM sessions WHERE id = ?", main).Scan(&executionID); err != nil {
		t.Fatal(err)
	}

	// When the owner selects the other session while the tool's preflight is in progress.
	service := automation.New(sqlc.New(conn), nil, time.Now)
	service.ConfigureProbe(func(ctx context.Context, botID, runnerID string) error {
		if runnerID != remote.ID {
			t.Errorf("probe checked %q, want %q", runnerID, remote.ID)
		}
		saved, err := service.Get(ctx, botID, routine.ID)
		if err != nil {
			return err
		}
		saved.TargetSessionID = second
		_, err = service.Update(ctx, botID, routine.ID, saved.Input)
		return err
	})
	tools := automation.NewToolService(conn, service, func(int64) {})
	_, err := tools.Run(t.Context(), engine.TaskContext{BotID: "default", ConversationID: executionID, SourceRequestID: id, Key: "default/fixture-run"}, routine.ID)

	// Then the tool asks the caller to try again without sending a run to either session.
	if !errors.Is(err, engine.ErrBusy) || len(d.runs(t, routine.ID)) != 0 {
		t.Fatalf("run during target edit: %v", err)
	}
}
