package app_test

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/runner/runnertest"
)

const routinesPath = "/api/agents/default/automations"

func boundRoutine(target int64, prompt string) map[string]any {
	return map[string]any{
		"name": "Bound routine", "prompt": prompt, "session": "session", "targetConversationId": target,
		"timeZone": "UTC", "enabled": true, "schedule": map[string]any{"kind": "every", "minutes": 5},
	}
}

func TestUserRunsAutomationInExistingSessionAfterRestart(t *testing.T) {
	t.Parallel()
	// Given a session and a saved Routine that refers to it.
	data := t.TempDir()
	d := startDex(t, data, "")
	d.setModel(t, "echo")
	id := d.newChat(t, "earlier context")
	d.watch(t, id).until(t, "earlier answer", answered("echo: earlier context"))
	var routine automationInfo
	d.must(t, http.MethodPost, routinesPath, boundRoutine(id, "bound run"), &routine, http.StatusCreated)

	// When the owner starts a manual run just before a restart, maintenance keeps its input pending.
	eventually(t, "idle target allows maintenance", func() (bool, any) {
		status, raw := d.call(t, http.MethodPost, "/api/maintenance", nil)
		return status == http.StatusOK, raw
	})
	run := d.runNow(t, routine.ID)
	if run.ChatID != id || run.Status != "running" {
		t.Fatalf("run = %+v, want running in chat %d", run, id)
	}
	var admitted bool
	conn := sessionDB(t, d)
	if err := conn.QueryRowContext(t.Context(), "SELECT admitted_at IS NOT NULL FROM submissions WHERE id = ?", run.ID).Scan(&admitted); err != nil || admitted {
		t.Fatalf("manual input before restart: admitted=%v, err=%v", admitted, err)
	}
	d.stop()

	// Then the committed run resumes in its original session after restart.
	d = startDex(t, data, "")
	d.watch(t, id).until(t, "bound routine answer", answeredWith("echo: bound run"))
	if got := settledRoutine(t, d, routine.ID, run.ID); got.ChatID != id {
		t.Fatalf("recovered run = %+v, want chat %d", got, id)
	}
}

func TestUserScheduledBoundAutomationKeepsItsTargetAfterRestart(t *testing.T) {
	t.Parallel()
	// Given a once Routine that becomes due while dex is closed.
	data := t.TempDir()
	d := startDex(t, data, "")
	d.setModel(t, "echo")
	id := d.newSession(t).ID
	body := boundRoutine(id, "scheduled bound run")
	at := time.Now().Add(time.Second).UnixMilli()
	body["schedule"] = map[string]any{"kind": "once", "at": at}
	var routine automationInfo
	d.must(t, http.MethodPost, routinesPath, body, &routine, http.StatusCreated)
	d.stop()

	// When the scheduler runs after restart.
	d = startDexWith(t, app.Config{DataDir: data, AutomationCheck: 50 * time.Millisecond})

	// Then the scheduled input goes to the saved session.
	d.watch(t, id).until(t, "scheduled bound routine answer", answeredWith("echo: scheduled bound run"))
	runs := d.runs(t, routine.ID)
	if len(runs) != 1 || runs[0].ChatID != id || runs[0].DueAt != at {
		t.Fatalf("runs = %+v, want one run in chat %d at %d", runs, id, at)
	}
	settledRoutine(t, d, routine.ID, runs[0].ID)
	// Replay the due cursor left by a crash before cursor advancement.
	conn := sessionDB(t, d)
	d.stop()
	if _, err := conn.ExecContext(t.Context(), "UPDATE automations SET next_due_at = ? WHERE id = ?", at, routine.ID); err != nil {
		t.Fatal(err)
	}
	d = startDexWith(t, app.Config{DataDir: data, AutomationCheck: 50 * time.Millisecond})
	eventually(t, "replayed schedule advanced", func() (bool, any) {
		var saved automationInfo
		d.must(t, http.MethodGet, routinesPath+"/"+routine.ID, nil, &saved)
		return len(saved.NextRuns) == 0, saved
	})
	if replay := d.runs(t, routine.ID); len(replay) != 1 || replay[0].ID != runs[0].ID {
		t.Fatalf("schedule replay created another run: %+v", replay)
	}
}

func TestUserBoundAutomationCanReportItsOwnResult(t *testing.T) {
	t.Parallel()
	// Given a Routine with a structured check in an existing session.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "tool")
	id := d.newSession(t).ID
	const report = `automation_report {"outcome":"unchanged","summary":"bound check completed"}`
	var routine automationInfo
	d.must(t, http.MethodPost, routinesPath, boundRoutine(id, report), &routine, http.StatusCreated)

	// When the Automation runs.
	run := d.runNow(t, routine.ID)
	got := settledRoutine(t, d, routine.ID, run.ID)

	// Then only its own run can store a report.
	if got.Outcome != "unchanged" || got.Summary != "bound check completed" || got.ChatID != id {
		t.Fatalf("run did not store its report: %+v", got)
	}
	d.send(t, id, `automation_report {"outcome":"changed","summary":"not a routine"}`)
	d.watch(t, id).until(t, "ordinary input is denied at the reporting boundary", answeredWith("automation_report is only available during an Automation run"))
	if runs := d.runs(t, routine.ID); len(runs) != 1 {
		t.Fatalf("ordinary input created an Automation run: %+v", runs)
	}
	after := settledRoutine(t, d, routine.ID, run.ID)
	if after.Outcome != got.Outcome || after.Summary != got.Summary {
		t.Fatalf("ordinary input changed the run: %+v", after)
	}
}

func TestUserBoundAutomationWaitsBehindAnOrdinaryTurn(t *testing.T) {
	t.Parallel()
	// Given a busy session with an ordinary turn that waits on a local fixture gate.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "bash")
	work := t.TempDir()
	id := d.newChat(t, taskGate(t, work, "ordinary"))
	waitTaskFile(t, filepath.Join(work, "ordinary.pid"))
	marker := filepath.Join(work, "bound-order")
	var routine automationInfo
	prompt := fmt.Sprintf(`if ! test -f %q; then exit 1; fi; printf 'once\n' >> %q; echo bound-follow-up`, filepath.Join(work, "ordinary.gate"), marker)
	d.must(t, http.MethodPost, routinesPath, boundRoutine(id, prompt), &routine, http.StatusCreated)

	// When the Automation runs twice while the ordinary turn is still active.
	run := d.runNow(t, routine.ID)
	overlap := d.runNow(t, routine.ID)

	// Then the first input is queued, and only its duplicate overlap skips.
	if run.Status != "running" || run.ChatID != id || overlap.Status != "skipped" || !strings.Contains(overlap.Reason, "busy") {
		t.Fatalf("runnable input and overlap = %+v, %+v", run, overlap)
	}
	w := d.watch(t, id)
	w.until(t, "queued Routine", func(tr *transcript) bool {
		return slices.ContainsFunc(tr.items, func(it item) bool { return it.Queued && !it.Steer && it.Text == prompt })
	})
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("queued Routine executed during ordinary turn: %v", err)
	}
	writeFile(t, filepath.Join(work, "ordinary.gate"), "release")
	w.until(t, "Routine follow-up answer", answered("bash: bound-follow-up"))
	settledRoutine(t, d, routine.ID, run.ID)
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != "once\n" {
		t.Fatalf("executions = %q, %v", raw, err)
	}
}

func TestUserCannotBindAAutomationToAnotherBotOrRestrictedSession(t *testing.T) {
	t.Parallel()
	// Given same-bot Main, an empty Chat, and cross-bot, read-only, archived and scoped sessions.
	d := startDex(t, t.TempDir(), "")
	main := d.mainChat(t)
	archived := d.newSession(t).ID
	d.archive(t, http.MethodPut, archived, http.StatusOK)
	eligible := d.newSession(t).ID
	readOnly := sessionFixture(t, d, "automation:readonly", 1)
	scoped := sessionFixture(t, d, "chat:restricted-fixture", 2)
	if _, err := sessionDB(t, d).ExecContext(t.Context(), "UPDATE conversations SET execution_scope = 'restricted-fixture' WHERE id = ?", scoped); err != nil {
		t.Fatal(err)
	}
	var other apiAgent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "Other fixture bot"}, &other, http.StatusCreated)
	var foreign sessionInfo
	d.must(t, http.MethodPost, "/api/agents/"+other.ID+"/chats", nil, &foreign, http.StatusCreated)
	var routine automationInfo
	d.must(t, http.MethodPost, routinesPath, boundRoutine(eligible, "check"), &routine, http.StatusCreated)

	// When the owner lists targets or uses create and update directly.
	var listed struct {
		Targets []struct {
			ID int64 `json:"id"`
		} `json:"targets"`
	}
	d.must(t, http.MethodGet, routinesPath+"/targets", nil, &listed)
	ids := make([]int64, 0, len(listed.Targets))
	for _, target := range listed.Targets {
		ids = append(ids, target.ID)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []int64{main, eligible}) {
		t.Fatalf("eligible sessions = %v, want Main %d and Chat %d", ids, main, eligible)
	}
	for _, id := range []int64{foreign.ID, scoped, archived, readOnly} {
		d.must(t, http.MethodPost, routinesPath, boundRoutine(id, "check"), nil, http.StatusBadRequest)
		d.must(t, http.MethodPut, routinesPath+"/"+routine.ID, boundRoutine(id, "check"), nil, http.StatusBadRequest)
	}
}

func TestUserSeesAAutomationSkipWhenItsSessionIsGone(t *testing.T) {
	t.Parallel()
	// Given a Routine whose saved session is deleted before admission.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "echo")
	id := d.newSession(t).ID
	var routine automationInfo
	d.must(t, http.MethodPost, routinesPath, boundRoutine(id, "check"), &routine, http.StatusCreated)
	conn := sessionDB(t, d)
	if _, err := conn.ExecContext(t.Context(), "DELETE FROM conversations WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}

	// When the owner runs it.
	run := d.runNow(t, routine.ID)

	// Then it skips and leaves no input or fallback conversation.
	if run.Status != "skipped" || run.ChatID != 0 || !strings.Contains(run.Reason, "target session") {
		t.Fatalf("run = %+v", run)
	}
	var binding struct {
		TargetConversationID *int64 `json:"targetConversationId"`
	}
	d.must(t, http.MethodGet, routinesPath+"/"+routine.ID, nil, &binding)
	if binding.TargetConversationID != nil {
		t.Fatalf("deleted target survived in binding: %+v", binding)
	}
	var count int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE origin = 'automation'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("stored inputs = %d, %v", count, err)
	}
}

func TestUserBoundAutomationUsesTheSessionRunnerAndProject(t *testing.T) {
	t.Parallel()
	// Given a session with a local pin and a project, and a bot that defaults to an SSH environment.
	ssh := runnertest.NewFakeSSH(t)
	d := startDexWith(t, app.Config{DataDir: t.TempDir(), SSHProgram: ssh.SSHProgram})
	d.setModel(t, "bash")
	var remote apiRunner
	d.must(t, http.MethodPost, "/api/settings/runners", map[string]any{
		"name": "not-the-target", "kind": "pi-env", "config": map[string]string{"host": "fixture.internal", "user": "dev"},
	}, &remote, http.StatusCreated)
	var bot apiAgent
	d.must(t, http.MethodGet, "/api/agents/default", nil, &bot)
	bot.RunnerID = remote.ID
	d.must(t, http.MethodPut, "/api/agents/default", bot, nil)
	ssh.Refuse(t, "fixture.internal")
	id := d.newSession(t).ID
	chatPath := fmt.Sprintf("/api/agents/default/chats/%d", id)
	d.must(t, http.MethodPut, chatPath+"/runner", map[string]string{"runnerId": "local"}, nil)
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d.must(t, http.MethodPut, chatPath+"/project", map[string]string{"cwd": project}, nil)
	body := boundRoutine(id, "pwd")
	body["runnerId"], body["cwd"] = remote.ID, "/wrong-project"
	var routine automationInfo
	d.must(t, http.MethodPost, routinesPath, body, &routine, http.StatusCreated)

	// When the Automation runs, its own stale settings and the bot default cannot override the target.
	run := d.runNow(t, routine.ID)
	if run.Status != "running" || run.ChatID != id {
		t.Fatalf("run = %+v", run)
	}
	d.watch(t, id).until(t, "target project cwd", answered("bash: "+project))
	settledRoutine(t, d, routine.ID, run.ID)
	var settings struct {
		RunnerID string `json:"runnerId"`
		Cwd      string `json:"cwd"`
	}
	d.must(t, http.MethodGet, chatPath, nil, &settings)
	if settings.RunnerID != "local" || settings.Cwd != project {
		t.Fatalf("target changed: %+v", settings)
	}

	// A different bound session that follows the offline SSH default skips without input.
	offline := d.newSession(t).ID
	var unavailable automationInfo
	d.must(t, http.MethodPost, routinesPath, boundRoutine(offline, "pwd"), &unavailable, http.StatusCreated)
	if skipped := d.runNow(t, unavailable.ID); skipped.Status != "skipped" || skipped.ChatID != 0 || !strings.Contains(skipped.Reason, "unavailable") {
		t.Fatalf("unavailable run = %+v", skipped)
	}
}

func TestUserFailedBoundAutomationDoesNotWakeTheChildsParent(t *testing.T) {
	t.Parallel()
	// Given a child in an ordinary parent session, and a local provider that rejects requests.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "echo")
	parent := d.mainChat(t)
	child := spawnChild(t, d, parent, "Routine target child", "initial child turn", "echo").conversation(t)
	conn := sessionDB(t, d)
	waitChildTurnsEnded(t, conn, child, 1)
	boundary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "fixture provider rejected request", http.StatusUnauthorized)
	}))
	defer boundary.Close()
	d.must(t, http.MethodPut, "/api/settings/providers/routine", map[string]any{"api": "openai-completions", "baseUrl": boundary.URL + "/v1", "models": []string{"routine"}}, nil)
	d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/default/chats/%d/settings", child), map[string]string{"provider": "routine", "modelId": "routine", "thinkingLevel": "off"}, nil)
	var routine automationInfo
	d.must(t, http.MethodPost, routinesPath, boundRoutine(child, "check"), &routine, http.StatusCreated)

	// When the Routine execution fails inside the child.
	run := d.runNow(t, routine.ID)
	got := settledRoutine(t, d, routine.ID, run.ID)

	// Then the run owns that failure; no automatic child error result wakes the parent.
	if got.Outcome != "failed" || got.Status != "failed" || got.ChatID != child || got.Reason == "run ended without automation_report" {
		t.Fatalf("run = %+v", got)
	}
	if rows := resultRows(t, conn, parent); slices.ContainsFunc(rows, func(row resultRow) bool { return row.id == "err:"+run.ID }) {
		t.Fatalf("unexpected parent failure result: %+v", rows)
	}
}

func TestUserCanStopAndClearABoundAutomationSession(t *testing.T) {
	t.Parallel()
	// Given a queued bound Routine behind an active ordinary turn.
	d := startDexWith(t, app.Config{DataDir: t.TempDir(), AutomationCheck: 50 * time.Millisecond})
	d.setModel(t, "bash")
	work := t.TempDir()
	id := d.newChat(t, taskGate(t, work, "active"))
	waitTaskFile(t, filepath.Join(work, "active.pid"))
	var routine automationInfo
	d.must(t, http.MethodPost, routinesPath, boundRoutine(id, "next scheduled check"), &routine, http.StatusCreated)
	run := d.runNow(t, routine.ID)
	if run.Status != "running" || run.ChatID != id {
		t.Fatalf("first run = %+v", run)
	}
	w := d.watch(t, id)
	w.until(t, "queued routine", func(tr *transcript) bool {
		return slices.ContainsFunc(tr.items, func(it item) bool { return it.Queued && it.ID == run.ID })
	})

	// When Stop withdraws the queued run, and the owner later clears the target's context.
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/abort", id), nil, nil)
	if stopped := settledRoutine(t, d, routine.ID, run.ID); stopped.Status != "failed" || stopped.Reason == "" {
		t.Fatalf("stopped run = %+v", stopped)
	}
	d.setModel(t, "context")
	d.send(t, id, "erase this context")
	w.until(t, "ordinary turn before clear", answeredWith("erase this context"))
	d.clearMain(t, id, uuid.NewV7().String())
	w.until(t, "reset context", func(tr *transcript) bool {
		return slices.ContainsFunc(tr.items, func(it item) bool { return it.Kind == "boundary" })
	})

	// Then the next scheduled run uses the same binding but sees only the new context.
	dueAt := time.Now().Add(-time.Second).UnixMilli()
	if _, err := sessionDB(t, d).ExecContext(t.Context(), "UPDATE automations SET next_due_at = ? WHERE id = ?", dueAt, routine.ID); err != nil {
		t.Fatal(err)
	}
	w.until(t, "scheduled check after clear", answeredWith(`context: ["next scheduled check\n\nRoutine reporting instructions`))
	runs := d.runs(t, routine.ID)
	if len(runs) != 2 || runs[0].ChatID != id || runs[0].DueAt != dueAt {
		t.Fatalf("runs after Stop and Clear = %+v", runs)
	}
}

func TestUserBoundAutomationKeepsReportingWhenAnOrdinaryTurnQueues(t *testing.T) {
	t.Parallel()
	// Given an existing session and a local model boundary that records each offered tool set.
	type reportRequest struct{ offered, instructed bool }
	offers := make(chan reportRequest, 8)
	release := make(chan struct{}, 1)
	var requests atomic.Int32
	boundary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.UnmarshalRead(r.Body, &request); err != nil {
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		offered := false
		for _, tool := range request.Tools {
			offered = offered || tool.Function.Name == "automation_report"
		}
		instructed := false
		for _, message := range slices.Backward(request.Messages) {
			if message.Role == "user" {
				instructed = strings.Contains(fmt.Sprint(message.Content), "Before this automation run ends, call automation_report once")
				break
			}
		}
		offers <- reportRequest{offered, instructed}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests.Add(1) == 1 {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			_, _ = fmt.Fprintln(w, `data: {"id":"fixture-tool","object":"chat.completion.chunk","created":1,"model":"routine","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"check_1","type":"function","function":{"name":"automation_report","arguments":"{\"outcome\":\"unchanged\",\"summary\":\"fixture checked\"}"}}]},"finish_reason":null}]}`)
			_, _ = fmt.Fprint(w, "\n"+`data: {"id":"fixture-tool","object":"chat.completion.chunk","created":1,"model":"routine","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\ndata: [DONE]\n\n")
			return
		}
		_, _ = fmt.Fprint(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"routine\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"checked\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer func() {
		select {
		case release <- struct{}{}:
		default:
		}
		boundary.Close()
	}()
	d := startDex(t, t.TempDir(), "")
	d.must(t, http.MethodPut, "/api/settings/providers/routine", map[string]any{"api": "openai-completions", "baseUrl": boundary.URL + "/v1", "models": []string{"routine"}}, nil)
	d.must(t, http.MethodPut, "/api/agents/default", map[string]string{"name": "Dex", "provider": "routine", "modelId": "routine", "thinkingLevel": "off"}, nil)
	id := d.newSession(t).ID
	var routine automationInfo
	d.must(t, http.MethodPost, routinesPath, boundRoutine(id, "routine check"), &routine, http.StatusCreated)

	// When an ordinary message queues in the same session while the Routine is active.
	run := d.runNow(t, routine.ID)
	eventually(t, "Routine model request", func() (bool, any) {
		return requests.Load() == 1, requests.Load()
	})
	ordinary := d.send(t, id, "ordinary turn")
	d.watch(t, id).until(t, "ordinary input queued", func(tr *transcript) bool {
		return slices.ContainsFunc(tr.items, func(it item) bool { return it.Queued && it.ID == ordinary })
	})
	release <- struct{}{}
	result := settledRoutine(t, d, routine.ID, run.ID)
	waitResultSubmission(t, sessionDB(t, d), ordinary, "done")

	// Then the Routine can report even with a later admission. Both turns see the tool.
	// Only the Routine input includes its reporting instructions.
	if result.Outcome != "unchanged" || result.Summary != "fixture checked" {
		t.Fatalf("queued owner message changed Routine reporting: %+v", result)
	}
	for _, want := range []reportRequest{{true, true}, {true, true}, {true, false}} {
		select {
		case got := <-offers:
			if got != want {
				t.Fatalf("automation_report request = %+v, want %+v", got, want)
			}
		default:
			t.Fatal("missing fixture model request")
		}
	}
}
