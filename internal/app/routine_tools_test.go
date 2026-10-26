package app_test

import (
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/automation"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func routineToolJSON(t *testing.T, d *dex, chatID int64, name string, args any, out any) {
	t.Helper()
	got := taskTool(t, d, chatID, name, args)
	if !strings.HasPrefix(got, "tool: ") {
		t.Fatalf("%s: %s", name, got)
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(got, "tool: ")), out); err != nil {
		t.Fatalf("%s: %s: %v", name, got, err)
	}
}

func TestUserManagesRoutinesThroughChat(t *testing.T) {
	t.Parallel()
	// Given a writable Chat with a project selected.
	d := startDex(t, t.TempDir(), "")
	id := d.newSession(t).ID
	project := t.TempDir()
	d.must(t, http.MethodPut, projectURL(id), map[string]string{"cwd": project}, nil)
	// When the user creates a routine, omitted cwd inherits the Chat project.
	var created automation.Automation
	routineToolJSON(t, d, id, "create_routine", map[string]any{"name": "Morning check", "prompt": "report_check {\"outcome\":\"changed\",\"summary\":\"New result\"}", "schedule": map[string]any{"kind": "every", "minutes": 30}, "timeZone": "UTC", "enabled": true, "session": "new"}, &created)
	if created.ID == "" || !created.Enabled || created.Cwd != project || created.TimeZone != "UTC" || len(created.NextRuns) == 0 {
		t.Fatalf("created: %+v", created)
	}
	// Then partial updates keep the saved prompt and change only requested state.
	var renamed automation.Automation
	routineToolJSON(t, d, id, "update_routine", map[string]any{"id": created.ID, "name": "Renamed check", "prompt": created.Prompt}, &renamed)
	if renamed.Name != "Renamed check" || !slices.Equal(renamed.NextRuns, created.NextRuns) || renamed.UpdatedAt <= created.UpdatedAt {
		t.Fatalf("non-scheduling edit: before=%+v after=%+v", created, renamed)
	}
	for _, enabled := range []bool{false, true} {
		var updated automation.Automation
		routineToolJSON(t, d, id, "update_routine", map[string]any{"id": created.ID, "enabled": enabled}, &updated)
		if updated.Enabled != enabled || updated.Prompt != created.Prompt {
			t.Fatalf("updated: %+v", updated)
		}
		if !enabled && len(updated.NextRuns) != 0 {
			t.Fatalf("paused next runs: %+v", updated)
		}
	}
	var updated automation.Automation
	routineToolJSON(t, d, id, "update_routine", map[string]any{"id": created.ID, "schedule": map[string]any{"kind": "weekly", "days": []int{1, 5}, "time": "09:00"}, "timeZone": "Asia/Shanghai"}, &updated)
	if updated.Schedule.Kind != "weekly" || updated.TimeZone != "Asia/Shanghai" || updated.Prompt != created.Prompt {
		t.Fatalf("schedule update: %+v", updated)
	}
	// When the user runs a paused routine now, get exposes the saved outcome and transcript reference.
	routineToolJSON(t, d, id, "update_routine", map[string]any{"id": created.ID, "enabled": false}, &updated)
	var run automation.Run
	routineToolJSON(t, d, id, "run_routine", map[string]string{"id": created.ID}, &run)
	if run.ID == "" || run.ChatID == 0 {
		t.Fatalf("run: %+v", run)
	}
	eventually(t, "routine report", func() (bool, any) {
		var runs struct {
			Runs []automation.Run `json:"runs"`
		}
		d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/automations/%s/runs", created.ID), nil, &runs)
		return len(runs.Runs) > 0 && runs.Runs[0].Status == "done", runs
	})
	var detail struct {
		automation.Automation
		Runs []automation.Run `json:"runs"`
	}
	routineToolJSON(t, d, id, "get_routine", map[string]string{"id": created.ID}, &detail)
	if len(detail.Runs) != 1 || detail.Runs[0].ID != run.ID || detail.Runs[0].ChatID != run.ChatID || detail.Runs[0].Summary != "New result" {
		t.Fatalf("detail: %+v", detail)
	}
	// And main can list routines created in Chat for the same bot.
	var listed struct {
		Routines []automation.Automation `json:"routines"`
	}
	routineToolJSON(t, d, d.mainChat(t), "list_routines", map[string]any{}, &listed)
	if len(listed.Routines) != 1 || listed.Routines[0].ID != created.ID || listed.Routines[0].LastRun == nil {
		t.Fatalf("list: %+v", listed)
	}
	t.Logf("routine=%s run=%s transcript chat=%d", created.ID, run.ID, run.ChatID)
}

// savedRoutineCaller reads the business receipt of a real native tool invocation.
func savedRoutineCaller(t *testing.T, conn *sql.DB, action string) engine.TaskContext {
	t.Helper()
	var c engine.TaskContext
	err := conn.QueryRowContext(t.Context(), `SELECT o.agent_id,c.execution_id,o.source_request_id,o.key FROM routine_tool_operations o JOIN conversations c ON c.id=o.conversation_id WHERE action=? ORDER BY o.rowid DESC LIMIT 1`, action).Scan(&c.AgentID, &c.ConversationID, &c.SourceRequestID, &c.Key)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestUserRoutineToolReplayReturnsCommittedMutationsAfterRestart(t *testing.T) {
	t.Parallel()
	// Given completed native tool calls with durable business receipts.
	data := t.TempDir()
	d := startDex(t, data, "")
	id := d.mainChat(t)
	var created, updated automation.Automation
	args := map[string]any{"name": "Replay check", "prompt": "report_check {\"outcome\":\"unchanged\",\"summary\":\"Checked\"}", "schedule": map[string]any{"kind": "every", "minutes": 30}, "timeZone": "UTC"}
	routineToolJSON(t, d, id, "create_routine", args, &created)
	routineToolJSON(t, d, id, "update_routine", map[string]any{"id": created.ID, "schedule": map[string]any{"kind": "every", "minutes": 45}}, &updated)
	var run automation.Run
	routineToolJSON(t, d, id, "run_routine", map[string]string{"id": created.ID}, &run)
	settledRoutine(t, d, created.ID, run.ID)
	conn := sessionDB(t, d)
	createSource := savedRoutineCaller(t, conn, "create")
	updateSource := savedRoutineCaller(t, conn, "update")
	runSource := savedRoutineCaller(t, conn, "run")
	d.stop()
	startDex(t, data, "")
	// When durable execution retries the same identity after the result boundary,
	// a fresh business service uses the committed operation, even after settlement.
	q := sqlc.New(conn)
	service := automation.New(q, nil, time.Now) // Read service; mutations bind the real transactional input writer.
	tools := automation.NewToolService(conn, service, func(int64) {})
	for range 2 {
		replay, err := tools.Create(t.Context(), createSource, engine.RoutinePatch{})
		if err != nil || replay.ID != created.ID {
			t.Fatalf("create replay: %+v %v", replay, err)
		}
		edit, err := tools.Update(t.Context(), updateSource, engine.RoutinePatch{ID: created.ID, Schedule: &engine.RoutineSchedule{Kind: "every", Minutes: 45}})
		if err != nil || !slices.Equal(edit.NextRuns, updated.NextRuns) || edit.UpdatedAt != updated.UpdatedAt {
			t.Fatalf("update replay: %+v %v", edit, err)
		}
		rerun, err := tools.Run(t.Context(), runSource, created.ID)
		if err != nil || rerun.ID != run.ID || rerun.ChatID != run.ChatID {
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

func TestUserRoutineBusinessBoundaryRejectsUntrustedSources(t *testing.T) {
	t.Parallel()
	// Given an actual user tool invocation and its persisted source authority.
	data := t.TempDir()
	d := startDex(t, data, "")
	id := d.mainChat(t)
	var a automation.Automation
	routineToolJSON(t, d, id, "create_routine", map[string]any{"name": "Scoped", "prompt": "check", "schedule": map[string]any{"kind": "every", "minutes": 30}, "timeZone": "UTC"}, &a)
	conn := sessionDB(t, d)
	source := savedRoutineCaller(t, conn, "create")
	q := sqlc.New(conn)
	tools := automation.NewToolService(conn, automation.New(q, nil, time.Now), func(int64) {})
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
		if _, err := tools.Create(t.Context(), c, engine.RoutinePatch{}); err == nil {
			t.Fatal("create accepted untrusted source")
		}
		if _, err := tools.Update(t.Context(), c, engine.RoutinePatch{ID: a.ID, Enabled: new(false)}); err == nil {
			t.Fatal("update accepted untrusted source")
		}
		if _, err := tools.Run(t.Context(), c, a.ID); err == nil {
			t.Fatal("run accepted untrusted source")
		}
	}
	wrong := source
	wrong.AgentID = "other"
	reject(wrong)
	wrong = source
	wrong.ConversationID++
	reject(wrong)
	for _, kind := range []string{"task_result", "context_reset"} {
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
		if _, err := conn.ExecContext(t.Context(), "UPDATE conversations SET channel_key=? WHERE id=?", key, id); err != nil {
			t.Fatal(err)
		}
		reject(source)
	}
}

func TestUserRoutineToolsAreUnavailableInAutomationAndBackground(t *testing.T) {
	t.Parallel()
	// Given a model that attempts routine discovery in automatic and delegated work.
	data := t.TempDir()
	d := startDex(t, data, "")
	d.setModel(t, "tool")
	a := d.createAutomation(t, "No self scheduling", "list_routines {}", "new", map[string]any{"kind": "daily", "time": "09:00"})
	run := d.runNow(t, a.ID)
	d.watch(t, run.ChatID).until(t, "routine tool unavailable", answeredWith("Tool list_routines is not available"))
	// When a background task attempts the same action, the native tool set also refuses it.
	parent := d.newSession(t).ID
	spawnExecution(t, d, parent, "No background scheduling", "list_routines {}", "tool")
	task := waitExecution(t, taskResultDB(t, data), parent, "No background scheduling", "done")
	if !strings.Contains(task.Report, "Tool list_routines is not available") {
		t.Fatalf("background report: %s", task.Report)
	}
}

func TestUserRoutineMutationFailurePreservesCauseAndRollsBack(t *testing.T) {
	t.Parallel()
	// Given a real SQLite failure after a routine mutation but before its replay receipt.
	d := startDex(t, t.TempDir(), "")
	id := d.mainChat(t)
	conn := sessionDB(t, d)
	args := map[string]any{"name": "Atomic", "prompt": "check", "schedule": map[string]any{"kind": "every", "minutes": 30}, "timeZone": "UTC"}
	fail := func(action string) {
		t.Helper()
		_, err := conn.ExecContext(t.Context(), `CREATE TRIGGER fail_routine_receipt BEFORE INSERT ON routine_tool_operations WHEN NEW.action='`+action+`' BEGIN SELECT RAISE(ABORT,'routine receipt write failed'); END`)
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
	got := taskTool(t, d, id, "create_routine", args)
	if !strings.Contains(got, "routine receipt write failed") {
		t.Fatalf("cause lost: %s", got)
	}
	var n int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM automations").Scan(&n); err != nil || n != 0 {
		t.Fatalf("partial create: %d %v", n, err)
	}
	dropFailure()
	var a automation.Automation
	routineToolJSON(t, d, id, "create_routine", args, &a)
	// When a run receipt fails, neither the run nor its durable input can survive.
	fail("run")
	got = taskTool(t, d, id, "run_routine", map[string]string{"id": a.ID})
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
	got = taskTool(t, d, id, "update_routine", map[string]any{"id": a.ID, "enabled": false})
	if !strings.Contains(got, "routine receipt write failed") {
		t.Fatalf("update cause lost: %s", got)
	}
	var detail automation.Automation
	routineToolJSON(t, d, id, "get_routine", map[string]string{"id": a.ID}, &detail)
	if !detail.Enabled || !slices.Equal(detail.NextRuns, a.NextRuns) {
		t.Fatalf("partial update: %+v", detail)
	}
}

func TestUserRoutineMetadataEditKeepsOverdueIntervalSeries(t *testing.T) {
	t.Parallel()
	// Given an enabled interval whose cursor is overdue, with no model to execute work.
	data := t.TempDir()
	d := startDex(t, data, "")
	id := d.mainChat(t)
	var a automation.Automation
	routineToolJSON(t, d, id, "create_routine", map[string]any{"name": "Interval", "prompt": "check", "schedule": map[string]any{"kind": "every", "minutes": 30}, "timeZone": "UTC"}, &a)
	conn := sessionDB(t, d)
	due := time.Now().Add(-75 * time.Minute).UnixMilli()
	if _, err := conn.ExecContext(t.Context(), "UPDATE automations SET next_due_at=? WHERE id=?", due, a.ID); err != nil {
		t.Fatal(err)
	}
	// When only the prompt changes, preview still starts at the saved aligned cursor.
	var edited automation.Automation
	routineToolJSON(t, d, id, "update_routine", map[string]any{"id": a.ID, "prompt": "changed prompt"}, &edited)
	if len(edited.NextRuns) != 3 || edited.NextRuns[0] != due || edited.NextRuns[1] != due+30*time.Minute.Milliseconds() || edited.NextRuns[2] != due+60*time.Minute.Milliseconds() {
		t.Fatalf("preview drift: %+v", edited)
	}
	d.stop()
	// Then the scheduler advances from that cursor, independent of metadata update time.
	if _, err := conn.ExecContext(t.Context(), "UPDATE agents SET model_id='' WHERE id='default'"); err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(due).Add(75 * time.Minute)
	service := automation.New(sqlc.New(conn), nil, func() time.Time { return now })
	service.Check(t.Context())
	var next int64
	if err := conn.QueryRowContext(t.Context(), "SELECT next_due_at FROM automations WHERE id=?", a.ID).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if want := due + 90*time.Minute.Milliseconds(); next != want {
		t.Fatalf("next=%d want aligned %d", next, want)
	}
}

func TestUserRoutineScheduleEditWinsOverInFlightCheck(t *testing.T) {
	t.Parallel()
	// Given a scheduler check that has read the old cursor before a schedule edit commits.
	data := t.TempDir()
	d := startDex(t, data, "")
	id := d.mainChat(t)
	var a automation.Automation
	routineToolJSON(t, d, id, "create_routine", map[string]any{"name": "Concurrent", "prompt": "check", "schedule": map[string]any{"kind": "every", "minutes": 30}, "timeZone": "UTC"}, &a)
	conn := sessionDB(t, d)
	d.stop()
	now := time.Now()
	due := now.Add(-5 * time.Minute).UnixMilli()
	editedDue := now.Add(45 * time.Minute).UnixMilli()
	if _, err := conn.ExecContext(t.Context(), "UPDATE agents SET model_id='' WHERE id='default'"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), "UPDATE automations SET next_due_at=? WHERE id=?", due, a.ID); err != nil {
		t.Fatal(err)
	}
	// This database boundary commits the edit between run insertion and cursor advancement.
	_, err := conn.ExecContext(t.Context(), fmt.Sprintf(`CREATE TRIGGER edit_during_check AFTER INSERT ON automation_runs BEGIN UPDATE automations SET schedule='{"kind":"every","minutes":45}', next_due_at=%d WHERE id=NEW.automation_id; END`, editedDue))
	if err != nil {
		t.Fatal(err)
	}
	service := automation.New(sqlc.New(conn), nil, func() time.Time { return now })
	service.Check(t.Context())
	// Then the stale check cannot overwrite the edited schedule's cursor.
	var next int64
	if err := conn.QueryRowContext(t.Context(), "SELECT next_due_at FROM automations WHERE id=?", a.ID).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if next != editedDue {
		t.Fatalf("scheduler overwrote edited due time: got %d want %d", next, editedDue)
	}
}

func TestUserRoutineOtherBotCannotReadOrChangeRoutine(t *testing.T) {
	t.Parallel()
	// Given a routine owned by the first bot and a second bot using native tools.
	d := startDex(t, t.TempDir(), "")
	id := d.mainChat(t)
	var a automation.Automation
	routineToolJSON(t, d, id, "create_routine", map[string]any{"name": "Private routine", "prompt": "check", "schedule": map[string]any{"kind": "every", "minutes": 30}, "timeZone": "UTC"}, &a)
	var beta apiAgent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "Beta", "provider": "faux", "modelId": "tool", "thinkingLevel": "off"}, &beta, http.StatusCreated)
	var main chatInfo
	d.must(t, http.MethodGet, "/api/agents/"+beta.ID+"/chats/main", nil, &main)
	conn := sessionDB(t, d)
	// When Beta attempts to inspect or mutate the first bot's ID, ownership remains enforced.
	for _, name := range []string{"list_routines", "get_routine", "update_routine", "run_routine"} {
		args := map[string]any{"id": a.ID}
		if name == "list_routines" {
			delete(args, "id")
		}
		if name == "update_routine" {
			args["enabled"] = false
		}
		request := uuid.NewV7().String()
		d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/%s/chats/%d/messages", beta.ID, main.ID), map[string]string{"id": request, "text": name + " " + taskJSON(t, args)}, nil, http.StatusAccepted)
		var status, answer string
		eventually(t, "Beta "+name, func() (bool, any) {
			err := conn.QueryRowContext(t.Context(), "SELECT status,answer FROM submissions WHERE id=?", request).Scan(&status, &answer)
			return err == nil && status == "done", fmt.Sprintf("status=%s answer=%s err=%v", status, answer, err)
		})
		if name == "list_routines" {
			if strings.Contains(answer, a.ID) || !strings.Contains(answer, `"routines":[]`) {
				t.Fatalf("leaked list: %s", answer)
			}
		} else if !strings.Contains(answer, "not found") {
			t.Fatalf("%s crossed bots: %s", name, answer)
		}
	}
	var detail automation.Automation
	routineToolJSON(t, d, id, "get_routine", map[string]string{"id": a.ID}, &detail)
	if !detail.Enabled {
		t.Fatal("other bot paused routine")
	}
}
