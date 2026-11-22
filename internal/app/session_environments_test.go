package app_test

import (
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"uuid"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/execenv/execenvtest"
)

type apiSessionEnvironment struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Default bool   `json:"default"`
	Status  string `json:"status"`
}

type apiChatEnvironment struct {
	ID            int64  `json:"id"`
	EnvironmentID string `json:"environmentId"`
}

// Given a local bot and saved SSH and E2B environments, when sessions select environments, then their
// command and file tools run only in the selected environment.
func TestUserRunsSessionsInSelectedEnvironments(t *testing.T) {
	const apiKey = "e2b_session_secret"
	sshFix := execenvtest.NewFakeSSH(t)
	fake := execenvtest.NewFakeE2B(t, apiKey)
	cfg := app.Config{DataDir: t.TempDir(), Home: t.TempDir(), SSHProgram: sshFix.SSHProgram, E2BAPIURL: fake.APIURL(), E2BEnvdURL: fake.EnvdURL()}
	d := startDexWith(t, cfg)
	writeFile(t, filepath.Join(cfg.DataDir, "agents", "default", "workspace", ".agents", "skills", "local-only", "SKILL.md"), "---\nname: local-only\ndescription: Local project skill\n---\n")
	writeFile(t, filepath.Join(cfg.DataDir, "agents", "default", ".agents", ".env"), "BOT_MARKER=explicit\n")
	writeFile(t, filepath.Join(cfg.DataDir, ".agents", "skills", "dex-shared", "SKILL.md"), "---\nname: dex-shared\ndescription: Shared skill\n---\n")

	var remoteEnv, sandbox apiEnvironment
	d.must(t, http.MethodPost, "/api/settings/environments", map[string]any{
		"name": "remote-box", "kind": "pi-env", "config": map[string]any{"host": "remote.internal", "user": "dev"},
	}, &remoteEnv, http.StatusCreated)
	d.must(t, http.MethodPost, "/api/settings/environments/"+remoteEnv.ID+"/test", nil, nil)
	d.must(t, http.MethodPost, "/api/settings/environments", map[string]any{
		"name": "sandbox-a", "kind": "e2b", "config": map[string]any{"template": "base", "timeoutSeconds": 300}, "secret": apiKey,
	}, &sandbox, http.StatusCreated)

	// Then the bot lists its own environment first, then the saved SSH and E2B environments.
	var listed struct {
		Environments []apiSessionEnvironment `json:"environments"`
	}
	status, raw := d.call(t, http.MethodGet, "/api/agents/default/environments", nil)
	if status != http.StatusOK || strings.Contains(raw, apiKey) {
		t.Fatalf("list environments: %d %s", status, raw)
	}
	d.must(t, http.MethodGet, "/api/agents/default/environments", nil, &listed)
	if len(listed.Environments) != 3 || listed.Environments[0].ID != "local" || !listed.Environments[0].Default ||
		listed.Environments[1] != (apiSessionEnvironment{ID: remoteEnv.ID, Name: "remote-box", Kind: "pi-env", Status: "available"}) ||
		listed.Environments[2] != (apiSessionEnvironment{ID: sandbox.ID, Name: "sandbox-a", Kind: "e2b", Status: "available"}) {
		t.Fatalf("environments=%+v", listed.Environments)
	}

	// When a new chat selects remote-box, then bash, write, and deliver_file use the SSH host.
	d.setModel(t, "bash")
	remote := d.newSession(t).ID
	var selected apiChatEnvironment
	d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/default/chats/%d/environment", remote), map[string]any{"environmentId": remoteEnv.ID}, &selected)
	if selected.EnvironmentID != remoteEnv.ID {
		t.Fatalf("selected=%+v", selected)
	}
	if out := taskTurn(t, d, remote, `printf %s "${REMOTE_SSH_MARKER:-local}"`); out != "bash: ssh-fake-ok" {
		t.Fatalf("remote bash=%q", out)
	}
	if out := taskTool(t, d, remote, "write", map[string]any{"path": "report.md", "content": "# Remote\n"}); strings.HasPrefix(out, "tool error:") {
		t.Fatalf("remote write=%q", out)
	}
	if delivered := deliverArtifact(t, d, remote, "report.md"); delivered.Name != "report.md" {
		t.Fatalf("delivered=%+v", delivered)
	}
	if _, body := readArtifact(t, d, "default", "report.md"); string(body) != "# Remote\n" {
		t.Fatalf("artifact=%q", body)
	}

	// Then a chat that selects nothing still runs on the bot's local environment.
	d.setModel(t, "bash")
	local := d.newSession(t).ID
	if out := taskTurn(t, d, local, `printf %s "${REMOTE_SSH_MARKER:-local}"`); out != "bash: local" {
		t.Fatalf("local bash=%q", out)
	}

	// A started chat keeps its environment. A missing environment is refused.
	d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/default/chats/%d/environment", remote), map[string]any{"environmentId": ""}, nil, http.StatusConflict)
	fresh := d.newSession(t).ID
	d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/default/chats/%d/environment", fresh), map[string]any{"environmentId": "missing"}, nil, http.StatusBadRequest)
	d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/default/chats/%d/environment", fresh), map[string]any{"environmentId": sandbox.ID}, nil)
	if out := taskTurn(t, d, fresh, `printf '%s:%s:%s:%s' "${REMOTE_E2B_MARKER:-local}" "$BOT_MARKER" "${DEX_DATA_DIR:-unset}" "$PWD"`); !strings.Contains(out, "e2b-fake-ok:explicit:unset:") || !strings.HasSuffix(out, "/home/user/workspace") {
		t.Fatalf("sandbox bash=%q", out)
	}

	// Then skill discovery and synchronization use the sandbox, not the bot's local project.
	d.must(t, http.MethodPut, chatPath(fresh, "settings"), map[string]string{"provider": "faux", "modelId": "section"}, nil)
	if out := taskTurn(t, d, fresh, "skills"); !strings.Contains(out, "/home/user/workspace/.dex/skills/dex/dex-shared/SKILL.md") || strings.Contains(out, "local-only") || strings.Contains(out, cfg.DataDir) {
		t.Fatalf("sandbox skills=%q", out)
	}
	d.must(t, http.MethodPut, chatPath(fresh, "settings"), map[string]string{"provider": "faux", "modelId": "bash"}, nil)
	if out := taskTurn(t, d, fresh, `test -f .dex/skills/dex/dex-shared/SKILL.md && printf synced`); out != "bash: synced" {
		t.Fatalf("sandbox skill file=%q", out)
	}

	// When the bot lists environments, then it sees both environments without secrets.
	tool := taskTool(t, d, local, "environment_list", map[string]any{})
	if !strings.Contains(tool, remoteEnv.ID) || !strings.Contains(tool, sandbox.ID) || strings.Contains(tool, apiKey) {
		t.Fatalf("environment_list=%q", tool)
	}

	// When the local chat creates a child on remote-box, then the child runs there.
	var child childRef
	toolJSON(t, callSessionTool(t, d, local, "session_new", map[string]any{"title": "Remote", "message": `printf %s "${REMOTE_SSH_MARKER:-local}"`, "provider": "faux", "modelId": "bash", "environment": remoteEnv.ID}), &child)
	results := waitResults(t, sessionDB(t, d), local, 1)
	if !strings.Contains(results[0].content, "ssh-fake-ok") {
		t.Fatalf("child result=%+v", results[0])
	}
	toolJSON(t, callSessionTool(t, d, local, "session_new", map[string]any{"title": "Sandbox", "message": `printf %s "$REMOTE_E2B_MARKER"`, "provider": "faux", "modelId": "bash", "environment": sandbox.ID}), &child)
	if results := waitResults(t, sessionDB(t, d), local, 2); !strings.Contains(results[1].content, "e2b-fake-ok") {
		t.Fatalf("sandbox child result=%+v", results[1])
	}
	if n := fake.Counts().Created; n != 1 {
		t.Fatalf("sessions of the same bot created %d sandboxes", n)
	}

	// After a restart the chat still runs on remote-box.
	d.stop()
	if n := fake.Counts().Killed; n != 1 {
		t.Fatalf("closing the bot deleted %d override sandboxes", n)
	}
	d = startDexWith(t, cfg)
	d.setModel(t, "bash")
	if out := taskTurn(t, d, remote, `printf %s "${REMOTE_SSH_MARKER:-local}"`); out != "bash: ssh-fake-ok" {
		t.Fatalf("bash after restart=%q", out)
	}

	// When the owner deletes remote-box, then the chat on it fails and never runs locally.
	d.must(t, http.MethodDelete, "/api/settings/environments/"+remoteEnv.ID, nil, nil)
	d.setModel(t, "bash")
	if out := taskTurn(t, d, remote, `printf %s "${REMOTE_SSH_MARKER:-local}"`); !strings.Contains(out, "was removed") || out == "bash: local" {
		t.Fatalf("bash after removal=%q", out)
	}
}

// Given a local chat with a project, when the owner forks it to an SSH environment, then the fork
// keeps the history, starts in the SSH workspace, and tells the user and the bot that it moved.
func TestUserForksSessionToAnotherEnvironment(t *testing.T) {
	sshFix := execenvtest.NewFakeSSH(t)
	fake := execenvtest.NewFakeE2B(t, "e2b_fork_secret")
	d := startDexWith(t, app.Config{DataDir: t.TempDir(), Home: t.TempDir(), SSHProgram: sshFix.SSHProgram, E2BAPIURL: fake.APIURL(), E2BEnvdURL: fake.EnvdURL()})
	var remoteEnv, sandbox apiEnvironment
	d.must(t, http.MethodPost, "/api/settings/environments", map[string]any{
		"name": "remote-box", "kind": "pi-env", "config": map[string]any{"host": "remote.internal", "user": "dev"},
	}, &remoteEnv, http.StatusCreated)
	d.must(t, http.MethodPost, "/api/settings/environments/"+remoteEnv.ID+"/test", nil, nil)
	d.must(t, http.MethodPost, "/api/settings/environments", map[string]any{
		"name": "sandbox-f", "kind": "e2b", "config": map[string]any{"template": "base", "timeoutSeconds": 300}, "secret": "e2b_fork_secret",
	}, &sandbox, http.StatusCreated)
	const marker = `printf %s "${REMOTE_SSH_MARKER:-local}"`

	d.setModel(t, "bash")
	src := d.newSession(t).ID
	project := t.TempDir()
	d.must(t, http.MethodPut, projectURL(src), map[string]string{"cwd": project}, nil)
	if out := taskTurn(t, d, src, marker); out != "bash: local" {
		t.Fatalf("source bash=%q", out)
	}
	w := d.watch(t, src)
	w.until(t, "source answer", answered("bash: local"))
	entry := findItem(t, &w.tr, "assistant", "bash: local").EntryID

	// When the target is a missing environment, then the fork fails and creates no chat.
	chats := len(d.chats(t))
	d.must(t, http.MethodPost, chatPath(src, "fork"), map[string]any{"entryId": entry, "environmentId": "missing"}, nil, http.StatusBadRequest)
	if n := len(d.chats(t)); n != chats {
		t.Fatalf("refused forks created chats: %d, want %d", n, chats)
	}

	// When the owner forks to remote-box, then a retry returns the same chat, and the same ID for
	// another environment is refused.
	var created, again struct {
		ChatID int64 `json:"chatId"`
	}
	d.must(t, http.MethodPost, chatPath(src, "fork"), map[string]any{"entryId": entry, "environmentId": sandbox.ID}, &created, http.StatusCreated)
	if out := taskTurn(t, d, created.ChatID, `printf %s "$REMOTE_E2B_MARKER"`); out != "bash: e2b-fake-ok" {
		t.Fatalf("sandbox fork bash=%q", out)
	}
	body := map[string]any{"id": uuid.NewV7().String(), "entryId": entry, "environmentId": remoteEnv.ID}
	d.must(t, http.MethodPost, chatPath(src, "fork"), body, &created, http.StatusCreated)
	d.must(t, http.MethodPost, chatPath(src, "fork"), body, &again, http.StatusCreated)
	if again.ChatID != created.ChatID {
		t.Fatalf("fork retry=%d, want %d", again.ChatID, created.ChatID)
	}
	d.must(t, http.MethodPost, chatPath(src, "fork"), map[string]any{"id": body["id"], "entryId": entry, "environmentId": "local"}, nil, http.StatusConflict)

	// Then the fork selects remote-box, has no project, and shows the notice once after the history.
	var fork struct {
		forkInfo
		EnvironmentID string `json:"environmentId"`
	}
	d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d", created.ChatID), nil, &fork)
	if fork.EnvironmentID != remoteEnv.ID || fork.Cwd != "" || fork.ForkedFrom == nil || fork.ForkedFrom.ID != src {
		t.Fatalf("fork=%+v", fork)
	}
	notice := "This chat moved from `local` to `remote-box`. Chat history was kept. Files and background tasks were not moved. Check that the files you need exist before continuing."
	fw := d.watch(t, fork.ID)
	fw.until(t, "fork notice", func(tr *transcript) bool {
		last := lastItem(tr)
		return countKind(tr, "boundary") == 1 && last.Kind == "boundary" && last.Boundary == "environment" && last.Text == notice
	})

	// When the user sends in the fork, then the bot gets the notice before the new message.
	d.must(t, http.MethodPut, chatPath(fork.ID, "settings"), map[string]string{"provider": "faux", "modelId": "context"}, nil)
	d.send(t, fork.ID, "where am I")
	want, err := json.Marshal([]string{marker, notice, "where am I"})
	if err != nil {
		t.Fatal(err)
	}
	fw.until(t, "fork answer", answered("context: "+string(want)))

	// Then the commands of the fork run on remote-box, and the source still runs locally.
	d.must(t, http.MethodPut, chatPath(fork.ID, "settings"), map[string]string{"provider": "faux", "modelId": "bash"}, nil)
	if out := taskTurn(t, d, fork.ID, marker); out != "bash: ssh-fake-ok" {
		t.Fatalf("fork bash=%q", out)
	}
	assertProject(t, d, src, project)
	if out := taskTurn(t, d, src, marker); out != "bash: local" {
		t.Fatalf("source bash after fork=%q", out)
	}

	// When the owner forks to the environment that the chat runs in, then nothing moves.
	d.must(t, http.MethodPost, chatPath(src, "fork"), map[string]any{"entryId": entry, "environmentId": "local"}, &created, http.StatusCreated)
	same := d.watch(t, created.ChatID)
	same.until(t, "same environment fork", answered("bash: local"))
	if n := countKind(&same.tr, "boundary"); n != 0 {
		t.Fatalf("same environment fork has %d boundaries: %+v", n, same.tr.items)
	}
	assertProject(t, d, created.ChatID, project)

	// When the bot forks a child session to remote-box, then the forked child runs there.
	var child childRef
	toolJSON(t, callSessionTool(t, d, src, "session_new", map[string]any{"title": "Child", "message": marker, "provider": "faux", "modelId": "bash"}), &child)
	if results := waitResults(t, sessionDB(t, d), src, 1); !strings.Contains(results[0].content, "bash: local") {
		t.Fatalf("child result=%+v", results[0])
	}
	toolJSON(t, callSessionTool(t, d, src, "session_fork", map[string]any{"id": child.ID, "message": marker, "environment": remoteEnv.ID}), &child)
	if results := waitResults(t, sessionDB(t, d), src, 2); !strings.Contains(results[1].content, "ssh-fake-ok") {
		t.Fatalf("forked child result=%+v", results[1])
	}

	// When the owner deletes remote-box, then a fork moves the chat on it back to local.
	fw.until(t, "fork remote answer", answered("bash: ssh-fake-ok"))
	remoteAnswer := findItem(t, &fw.tr, "assistant", "bash: ssh-fake-ok").EntryID
	d.must(t, http.MethodDelete, "/api/settings/environments/"+remoteEnv.ID, nil, nil)
	d.must(t, http.MethodPost, chatPath(fork.ID, "fork"), map[string]any{"entryId": remoteAnswer, "environmentId": "local"}, &created, http.StatusCreated)
	rescued := d.watch(t, created.ChatID)
	rescued.until(t, "rescued notice", func(tr *transcript) bool {
		last := lastItem(tr)
		return last.Kind == "boundary" && last.Boundary == "environment" && strings.HasPrefix(last.Text, "This chat moved from `"+remoteEnv.ID+"` to `local`.")
	})
	rescuedID := created.ChatID
	if out := taskTurn(t, d, rescuedID, marker); out != "bash: local" {
		t.Fatalf("rescued bash=%q", out)
	}

	// Then a retry of the first fork still returns its chat, although remote-box is gone.
	d.must(t, http.MethodPost, chatPath(src, "fork"), body, &again, http.StatusCreated)
	if again.ChatID != fork.ID {
		t.Fatalf("fork retry after removal=%d, want %d", again.ChatID, fork.ID)
	}

	// When the user forks the local chat at an answer before its move notice, then the fork gets
	// its own notice, because that history last ran on remote-box.
	rescued.until(t, "rescued local answer", func(tr *transcript) bool {
		last := lastItem(tr)
		return countKind(tr, "boundary") == 2 && last.Kind == "assistant" && last.Text == "bash: local"
	})
	afterMove := lastItem(&rescued.tr).EntryID
	beforeMove := findItem(t, &rescued.tr, "assistant", "bash: ssh-fake-ok").EntryID
	d.must(t, http.MethodPost, chatPath(rescuedID, "fork"), map[string]any{"entryId": beforeMove}, &created, http.StatusCreated)
	back := "This chat moved from `" + remoteEnv.ID + "` to `local`. Chat history was kept. Files and background tasks were not moved. Check that the files you need exist before continuing."
	second := d.watch(t, created.ChatID)
	second.until(t, "second fork notice", func(tr *transcript) bool {
		last := lastItem(tr)
		return countKind(tr, "boundary") == 2 && last.Kind == "boundary" && last.Boundary == "environment" && last.Text == back
	})
	d.must(t, http.MethodPut, chatPath(created.ChatID, "settings"), map[string]string{"provider": "faux", "modelId": "context"}, nil)
	d.send(t, created.ChatID, "check")
	if want, err = json.Marshal([]string{marker, notice, "where am I", marker, back, "check"}); err != nil {
		t.Fatal(err)
	}
	second.until(t, "second fork answer", answered("context: "+string(want)))

	// When the user forks the local chat after its move notice, then no notice is added.
	d.must(t, http.MethodPost, chatPath(rescuedID, "fork"), map[string]any{"entryId": afterMove}, &created, http.StatusCreated)
	d.must(t, http.MethodPut, chatPath(created.ChatID, "settings"), map[string]string{"provider": "faux", "modelId": "context"}, nil)
	d.send(t, created.ChatID, "check")
	if want, err = json.Marshal([]string{marker, notice, "where am I", marker, back, marker, "check"}); err != nil {
		t.Fatal(err)
	}
	d.watch(t, created.ChatID).until(t, "third fork answer", answered("context: "+string(want)))

	// When the user forks the local chat before both notices, then the oldest one tells where the
	// history ran. That was local, so no notice is added.
	d.must(t, http.MethodPost, chatPath(rescuedID, "fork"), map[string]any{"entryId": entry}, &created, http.StatusCreated)
	d.must(t, http.MethodPut, chatPath(created.ChatID, "settings"), map[string]string{"provider": "faux", "modelId": "context"}, nil)
	d.send(t, created.ChatID, "check")
	if want, err = json.Marshal([]string{marker, "check"}); err != nil {
		t.Fatal(err)
	}
	d.watch(t, created.ChatID).until(t, "fourth fork answer", answered("context: "+string(want)))
}

// Given sessions that follow the bot's environment, when the owner changes that environment,
// then running background work blocks the change, and after it the sessions drop the old cwd.
// A chat that selects local explicitly stays local.
func TestUserChangesBotEnvironmentForFollowingSessions(t *testing.T) {
	fake := execenvtest.NewFakeE2B(t, "e2b_change_secret")
	d := startDexWith(t, app.Config{DataDir: t.TempDir(), E2BAPIURL: fake.APIURL(), E2BEnvdURL: fake.EnvdURL()})
	var created apiEnvironment
	d.must(t, http.MethodPost, "/api/settings/environments", map[string]any{
		"name": "sandbox-b", "kind": "e2b", "config": map[string]any{"template": "base", "timeoutSeconds": 300}, "secret": "e2b_change_secret",
	}, &created, http.StatusCreated)

	chatID := newParent(t, d)
	project := t.TempDir()
	if out := taskTool(t, d, chatID, "set_project", map[string]any{"path": project}); strings.HasPrefix(out, "tool error:") {
		t.Fatalf("set_project=%q", out)
	}
	bg := bgStart(t, d, chatID, map[string]any{"command": "sleep 30", "wake": "none"})
	routine := func(name, environmentID string) routineEnvironment {
		var r routineEnvironment
		d.must(t, http.MethodPost, "/api/agents/default/automations", map[string]any{
			"name": name, "prompt": "pwd", "schedule": map[string]any{"kind": "daily", "time": "09:00"}, "timeZone": "UTC",
			"session": "reuse", "enabled": true, "cwd": project, "environmentId": environmentID,
		}, &r, http.StatusCreated)
		return r
	}
	following, pinnedRoutine := routine("Following", ""), routine("Pinned", "local")

	// When a background command runs in a following session, then the change fails.
	var bot apiAgent
	d.must(t, http.MethodGet, "/api/agents/default", nil, &bot)
	bot.EnvironmentID = created.ID
	d.must(t, http.MethodPut, "/api/agents/default", bot, nil, http.StatusConflict)

	pinned := d.newSession(t).ID
	d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/default/chats/%d/environment", pinned), map[string]any{"environmentId": "local"}, nil)

	callSessionTool(t, d, chatID, "background_stop", map[string]any{"id": bg.ID})

	// When PiG has accepted input that has not started, then the change fails too.
	d.setModel(t, "bash")
	d.send(t, chatID, "sleep 2")
	queued := d.send(t, chatID, `printf %s "${REMOTE_E2B_MARKER:-local}"`)
	db := sessionDB(t, d)
	eventually(t, "admitted queued input", func() (bool, any) {
		var admitted sql.NullInt64
		err := db.QueryRowContext(t.Context(), "SELECT admitted_at FROM submissions WHERE id = ?", queued).Scan(&admitted)
		return err == nil && admitted.Valid, err
	})
	d.must(t, http.MethodPut, "/api/agents/default", bot, nil, http.StatusConflict)

	// When the input settles, then the change succeeds and the session starts in the new workspace.
	eventually(t, "settled input", func() (bool, any) {
		var n int
		err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE status = 'pending'").Scan(&n)
		return err == nil && n == 0, n
	})
	d.must(t, http.MethodPut, "/api/agents/default", bot, nil)
	assertProject(t, d, chatID, "")
	// Then a routine that follows the bot drops its project, and a routine on local keeps it.
	for _, want := range []routineEnvironment{{ID: following.ID}, {ID: pinnedRoutine.ID, EnvironmentID: "local", Cwd: project}} {
		var got routineEnvironment
		d.must(t, http.MethodGet, "/api/agents/default/automations/"+want.ID, nil, &got)
		if got != want {
			t.Fatalf("routine after change=%+v, want %+v", got, want)
		}
	}
	d.setModel(t, "bash")
	if out := taskTurn(t, d, chatID, `printf %s "${REMOTE_E2B_MARKER:-local}"`); out != "bash: e2b-fake-ok" {
		t.Fatalf("bash after change=%q", out)
	}
	// Then a chat that selects local explicitly stays local.
	d.setModel(t, "bash")
	if out := taskTurn(t, d, pinned, `printf %s "${REMOTE_E2B_MARKER:-local}"`); out != "bash: local" {
		t.Fatalf("pinned bash after change=%q", out)
	}
}
