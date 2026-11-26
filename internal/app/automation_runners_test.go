package app_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/automation"
	"github.com/recally-io/dex/internal/runner/runnertest"
)

type routineRunner struct {
	ID       string `json:"id"`
	RunnerID string `json:"runnerId"`
	Cwd      string `json:"cwd"`
}

// Given an SSH environment, when routines select environments, then each run uses only the
// selected environment, a removed one skips the run, and a reused session never changes machine.
func TestUserRunsAutomationsInSelectedRunners(t *testing.T) {
	sshFix := runnertest.NewFakeSSH(t)
	fake := runnertest.NewFakeE2B(t, "e2b_routine_secret")
	d := startDexWith(t, app.Config{DataDir: t.TempDir(), Home: t.TempDir(), SSHProgram: sshFix.SSHProgram, E2BAPIURL: fake.APIURL(), E2BEnvdURL: fake.EnvdURL()})
	var remoteEnv, sandbox apiRunner
	d.must(t, http.MethodPost, "/api/settings/runners", map[string]any{
		"name": "remote-box", "kind": "ssh", "config": map[string]any{"host": "remote.internal", "user": "dev"},
	}, &remoteEnv, http.StatusCreated)
	d.must(t, http.MethodPost, "/api/settings/runners/"+remoteEnv.ID+"/test", nil, nil)
	d.must(t, http.MethodPost, "/api/settings/runners", map[string]any{
		"name": "sandbox-r", "kind": "e2b", "config": map[string]any{"template": "base", "timeoutSeconds": 300}, "secret": "e2b_routine_secret",
	}, &sandbox, http.StatusCreated)
	d.setModel(t, "bash")
	const marker = `printf %s "${REMOTE_SSH_MARKER:-local}"`
	routine := func(name, runnerID string) map[string]any {
		return map[string]any{
			"name": name, "prompt": marker, "schedule": map[string]any{"kind": "daily", "time": "09:00"},
			"timeZone": "UTC", "session": "reuse", "enabled": true, "runnerId": runnerID,
		}
	}
	runIn := func(id string, want string) runInfo {
		t.Helper()
		run := d.runNow(t, id)
		if run.Status != "running" {
			t.Fatalf("run=%+v", run)
		}
		d.watch(t, run.SessionID).until(t, "routine answer "+want, answered(want))
		settledRoutine(t, d, id, run.ID)
		return run
	}

	// When a routine selects an E2B or a missing environment, then it is refused.
	for _, env := range []string{sandbox.ID, "missing"} {
		d.must(t, http.MethodPost, "/api/bots/default/automations", routine("Refused", env), nil, http.StatusBadRequest)
	}

	// When a routine selects remote-box, then its runs use the SSH host.
	body := routine("Remote", remoteEnv.ID)
	var remote routineRunner
	d.must(t, http.MethodPost, "/api/bots/default/automations", body, &remote, http.StatusCreated)
	if remote.RunnerID != remoteEnv.ID {
		t.Fatalf("routine=%+v", remote)
	}
	onRemote := runIn(remote.ID, "bash: ssh-fake-ok")

	// Then a routine that selects nothing runs on the bot's local environment.
	var local routineRunner
	d.must(t, http.MethodPost, "/api/bots/default/automations", routine("Local", ""), &local, http.StatusCreated)
	firstLocal := runIn(local.ID, "bash: local")

	// When the owner moves the bot to remote-box, then the next run of the following routine starts
	// a new session there, although nobody edited the routine. Its runs tell where each one ran.
	setBotRunner := func(id string) {
		t.Helper()
		var bot apiBot
		d.must(t, http.MethodGet, "/api/bots/default", nil, &bot)
		bot.RunnerID = id
		d.must(t, http.MethodPut, "/api/bots/default", bot, nil)
	}
	setBotRunner(remoteEnv.ID)
	followed := runIn(local.ID, "bash: ssh-fake-ok")
	if followed.SessionID == firstLocal.SessionID {
		t.Fatalf("the following routine reused session %d after the bot moved", followed.SessionID)
	}
	d.watch(t, firstLocal.SessionID).until(t, "old local session", answered("bash: local"))
	var runs struct {
		Runs []struct {
			RunnerID string `json:"runnerId"`
		} `json:"runs"`
	}
	d.must(t, http.MethodGet, "/api/bots/default/automations/"+local.ID+"/runs", nil, &runs)
	if len(runs.Runs) != 2 || runs.Runs[0].RunnerID != remoteEnv.ID || runs.Runs[1].RunnerID != "local" {
		t.Fatalf("runs=%+v", runs.Runs)
	}
	setBotRunner("")

	// When the reused routine moves to local and back, then each move starts a new session, and the
	// earlier sessions stay readable.
	body["runnerId"] = "local"
	d.must(t, http.MethodPut, "/api/bots/default/automations/"+remote.ID, body, nil)
	onLocal := runIn(remote.ID, "bash: local")
	body["runnerId"] = remoteEnv.ID
	d.must(t, http.MethodPut, "/api/bots/default/automations/"+remote.ID, body, nil)
	back := runIn(remote.ID, "bash: ssh-fake-ok")
	if onLocal.SessionID == onRemote.SessionID || back.SessionID == onRemote.SessionID || back.SessionID == onLocal.SessionID {
		t.Fatalf("sessions: remote=%d local=%d back=%d", onRemote.SessionID, onLocal.SessionID, back.SessionID)
	}
	d.watch(t, onRemote.SessionID).until(t, "old session history", answered("bash: ssh-fake-ok"))
	// A run in the same environment continues the same session.
	again := runIn(remote.ID, "bash: ssh-fake-ok")
	if again.SessionID != back.SessionID {
		t.Fatalf("same environment session=%d, want %d", again.SessionID, back.SessionID)
	}
	// When the last run has no stored environment, as a run from before the upgrade, then where it
	// ran is not known, and the next run starts a new session.
	if _, err := sessionDB(t, d).ExecContext(t.Context(), `UPDATE automation_runs SET runner_id = '', cwd = '' WHERE id = ?`, again.ID); err != nil {
		t.Fatal(err)
	}
	if fresh := runIn(remote.ID, "bash: ssh-fake-ok"); fresh.SessionID == back.SessionID {
		t.Fatalf("a run after an unknown binding reused session %d", fresh.SessionID)
	}

	// When the bot creates a routine in a session on remote-box, then the routine runs there too.
	sessionID := d.newSession(t).ID
	d.must(t, http.MethodPut, fmt.Sprintf("/api/bots/default/sessions/%d/runner", sessionID), map[string]any{"runnerId": remoteEnv.ID}, nil)
	var created struct {
		RunnerID string `json:"runnerId"`
	}
	routineToolJSON(t, d, sessionID, "automation_create", map[string]any{"name": "From session", "prompt": marker, "schedule": map[string]any{"kind": "daily", "time": "09:00"}, "timeZone": "UTC", "enabled": true, "session": "new"}, &created)
	if created.RunnerID != remoteEnv.ID {
		t.Fatalf("created routine=%+v", created)
	}

	// When the host of a routine's SSH environment is down, then the run is skipped with a reason
	// that names it, and no session starts.
	var deadEnv apiRunner
	d.must(t, http.MethodPost, "/api/settings/runners", map[string]any{
		"name": "dead-box", "kind": "ssh", "config": map[string]any{"host": "dead.internal", "user": "dev"},
	}, &deadEnv, http.StatusCreated)
	sshFix.Refuse(t, "dead.internal")
	var dead routineRunner
	d.must(t, http.MethodPost, "/api/bots/default/automations", routine("Dead", deadEnv.ID), &dead, http.StatusCreated)
	if run := d.runNow(t, dead.ID); run.Status != "skipped" || run.SessionID != 0 || !strings.Contains(run.Reason, "dead-box is unavailable") {
		t.Fatalf("run on a dead host=%+v", run)
	}
	// Then automation_run skips it the same way.
	var toolRun automation.ToolRun
	routineToolJSON(t, d, sessionID, "automation_run", map[string]any{"id": dead.ID}, &toolRun)
	if toolRun.Status != "skipped" || toolRun.SessionID != 0 || !strings.Contains(toolRun.Reason, "dead-box is unavailable") {
		t.Fatalf("automation_run on a dead host=%+v", toolRun)
	}

	// When the owner removes remote-box, then a run is skipped with a reason that names it, and
	// nothing runs on another machine.
	d.must(t, http.MethodDelete, "/api/settings/runners/"+remoteEnv.ID, nil, nil)
	skipped := d.runNow(t, remote.ID)
	if skipped.Status != "skipped" || skipped.SessionID != 0 || !strings.Contains(skipped.Reason, remoteEnv.ID) {
		t.Fatalf("run after removal=%+v", skipped)
	}
	// Then the owner can still pause the routine without choosing another environment.
	body["enabled"] = false
	d.must(t, http.MethodPut, "/api/bots/default/automations/"+remote.ID, body, nil)
}
