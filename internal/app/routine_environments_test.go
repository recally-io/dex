package app_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/execenv/execenvtest"
)

type routineEnvironment struct {
	ID            string `json:"id"`
	EnvironmentID string `json:"environmentId"`
	Cwd           string `json:"cwd"`
}

// Given an SSH environment, when routines select environments, then each run uses only the
// selected environment, a removed one skips the run, and a reused chat never changes machine.
func TestUserRunsRoutinesInSelectedEnvironments(t *testing.T) {
	sshFix := execenvtest.NewFakeSSH(t)
	fake := execenvtest.NewFakeE2B(t, "e2b_routine_secret")
	d := startDexWith(t, app.Config{DataDir: t.TempDir(), Home: t.TempDir(), SSHProgram: sshFix.SSHProgram, E2BAPIURL: fake.APIURL(), E2BEnvdURL: fake.EnvdURL()})
	var remoteEnv, sandbox apiEnvironment
	d.must(t, http.MethodPost, "/api/settings/environments", map[string]any{
		"name": "remote-box", "kind": "pi-env", "config": map[string]any{"host": "remote.internal", "user": "dev"},
	}, &remoteEnv, http.StatusCreated)
	d.must(t, http.MethodPost, "/api/settings/environments/"+remoteEnv.ID+"/test", nil, nil)
	d.must(t, http.MethodPost, "/api/settings/environments", map[string]any{
		"name": "sandbox-r", "kind": "e2b", "config": map[string]any{"template": "base", "timeoutSeconds": 300}, "secret": "e2b_routine_secret",
	}, &sandbox, http.StatusCreated)
	d.setModel(t, "bash")
	const marker = `printf %s "${REMOTE_SSH_MARKER:-local}"`
	routine := func(name, environmentID string) map[string]any {
		return map[string]any{
			"name": name, "prompt": marker, "schedule": map[string]any{"kind": "daily", "time": "09:00"},
			"timeZone": "UTC", "session": "reuse", "enabled": true, "environmentId": environmentID,
		}
	}
	runIn := func(id string, want string) runInfo {
		t.Helper()
		run := d.runNow(t, id)
		if run.Status != "running" {
			t.Fatalf("run=%+v", run)
		}
		d.watch(t, run.ChatID).until(t, "routine answer "+want, answered(want))
		settledRoutine(t, d, id, run.ID)
		return run
	}

	// When a routine selects an E2B or a missing environment, then it is refused.
	for _, env := range []string{sandbox.ID, "missing"} {
		d.must(t, http.MethodPost, "/api/agents/default/automations", routine("Refused", env), nil, http.StatusBadRequest)
	}

	// When a routine selects remote-box, then its runs use the SSH host.
	body := routine("Remote", remoteEnv.ID)
	var remote routineEnvironment
	d.must(t, http.MethodPost, "/api/agents/default/automations", body, &remote, http.StatusCreated)
	if remote.EnvironmentID != remoteEnv.ID {
		t.Fatalf("routine=%+v", remote)
	}
	onRemote := runIn(remote.ID, "bash: ssh-fake-ok")

	// Then a routine that selects nothing runs on the bot's local environment.
	var local routineEnvironment
	d.must(t, http.MethodPost, "/api/agents/default/automations", routine("Local", ""), &local, http.StatusCreated)
	firstLocal := runIn(local.ID, "bash: local")

	// When the owner moves the bot to remote-box, then the next run of the following routine starts
	// a new chat there, although nobody edited the routine. Its runs tell where each one ran.
	setBotEnvironment := func(id string) {
		t.Helper()
		var bot apiAgent
		d.must(t, http.MethodGet, "/api/agents/default", nil, &bot)
		bot.EnvironmentID = id
		d.must(t, http.MethodPut, "/api/agents/default", bot, nil)
	}
	setBotEnvironment(remoteEnv.ID)
	followed := runIn(local.ID, "bash: ssh-fake-ok")
	if followed.ChatID == firstLocal.ChatID {
		t.Fatalf("the following routine reused chat %d after the bot moved", followed.ChatID)
	}
	d.watch(t, firstLocal.ChatID).until(t, "old local chat", answered("bash: local"))
	var runs struct {
		Runs []struct {
			EnvironmentID string `json:"environmentId"`
		} `json:"runs"`
	}
	d.must(t, http.MethodGet, "/api/agents/default/automations/"+local.ID+"/runs", nil, &runs)
	if len(runs.Runs) != 2 || runs.Runs[0].EnvironmentID != remoteEnv.ID || runs.Runs[1].EnvironmentID != "local" {
		t.Fatalf("runs=%+v", runs.Runs)
	}
	setBotEnvironment("")

	// When the reused routine moves to local and back, then each move starts a new chat, and the
	// earlier chats stay readable.
	body["environmentId"] = "local"
	d.must(t, http.MethodPut, "/api/agents/default/automations/"+remote.ID, body, nil)
	onLocal := runIn(remote.ID, "bash: local")
	body["environmentId"] = remoteEnv.ID
	d.must(t, http.MethodPut, "/api/agents/default/automations/"+remote.ID, body, nil)
	back := runIn(remote.ID, "bash: ssh-fake-ok")
	if onLocal.ChatID == onRemote.ChatID || back.ChatID == onRemote.ChatID || back.ChatID == onLocal.ChatID {
		t.Fatalf("chats: remote=%d local=%d back=%d", onRemote.ChatID, onLocal.ChatID, back.ChatID)
	}
	d.watch(t, onRemote.ChatID).until(t, "old chat history", answered("bash: ssh-fake-ok"))
	// A run in the same environment continues the same chat.
	if again := runIn(remote.ID, "bash: ssh-fake-ok"); again.ChatID != back.ChatID {
		t.Fatalf("same environment chat=%d, want %d", again.ChatID, back.ChatID)
	}

	// When the bot creates a routine in a chat on remote-box, then the routine runs there too.
	chatID := d.newSession(t).ID
	d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/default/chats/%d/environment", chatID), map[string]any{"environmentId": remoteEnv.ID}, nil)
	var created routineEnvironment
	routineToolJSON(t, d, chatID, "create_routine", map[string]any{"name": "From chat", "prompt": marker, "schedule": map[string]any{"kind": "daily", "time": "09:00"}, "timeZone": "UTC", "enabled": true, "session": "new"}, &created)
	if created.EnvironmentID != remoteEnv.ID {
		t.Fatalf("created routine=%+v", created)
	}

	// When the host of a routine's SSH environment is down, then the run is skipped with a reason
	// that names it, and no chat starts.
	var deadEnv apiEnvironment
	d.must(t, http.MethodPost, "/api/settings/environments", map[string]any{
		"name": "dead-box", "kind": "pi-env", "config": map[string]any{"host": "dead.internal", "user": "dev"},
	}, &deadEnv, http.StatusCreated)
	sshFix.Refuse(t, "dead.internal")
	var dead routineEnvironment
	d.must(t, http.MethodPost, "/api/agents/default/automations", routine("Dead", deadEnv.ID), &dead, http.StatusCreated)
	if run := d.runNow(t, dead.ID); run.Status != "skipped" || run.ChatID != 0 || !strings.Contains(run.Reason, "dead-box is unavailable") {
		t.Fatalf("run on a dead host=%+v", run)
	}

	// When the owner removes remote-box, then a run is skipped with a reason that names it, and
	// nothing runs on another machine.
	d.must(t, http.MethodDelete, "/api/settings/environments/"+remoteEnv.ID, nil, nil)
	skipped := d.runNow(t, remote.ID)
	if skipped.Status != "skipped" || skipped.ChatID != 0 || !strings.Contains(skipped.Reason, remoteEnv.ID) {
		t.Fatalf("run after removal=%+v", skipped)
	}
	// Then the owner can still pause the routine without choosing another environment.
	body["enabled"] = false
	d.must(t, http.MethodPut, "/api/agents/default/automations/"+remote.ID, body, nil)
}
