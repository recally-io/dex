package app_test

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"uuid"
)

func projectURL(id int64) string { return fmt.Sprintf("/api/agents/default/chats/%d/project", id) }

func assertProject(t *testing.T, d *dex, id int64, want string) {
	t.Helper()
	var got struct {
		Cwd *string `json:"cwd"`
	}
	d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d", id), nil, &got)
	if got.Cwd == nil || *got.Cwd != want {
		t.Fatalf("Chat cwd = %v, want %q", got.Cwd, want)
	}
}

func TestUserChatProjectRunsAndClearsToWorkspace(t *testing.T) {
	// Given a Chat session and an existing project directory.
	data, project := t.TempDir(), t.TempDir()
	project, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	d := startDex(t, data, "")
	d.setModel(t, "bash")
	id := d.newSession(t).ID
	// When the user sets a project, the next command runs there.
	d.must(t, http.MethodPut, projectURL(id), map[string]string{"cwd": "  " + project + "/./  "}, nil)
	assertProject(t, d, id, project)
	if got := taskTurn(t, d, id, "pwd"); got != "bash: "+project {
		t.Fatal(got)
	}
	// Then clearing the project returns commands to the workspace.
	d.must(t, http.MethodPut, projectURL(id), map[string]string{"cwd": " "}, nil)
	assertProject(t, d, id, "")
	workspace, err := filepath.EvalSymlinks(filepath.Join(data, "agents", "default", "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	if got := taskTurn(t, d, id, "pwd"); got != "bash: "+workspace {
		t.Fatal(got)
	}
}

func TestUserChatProjectRejectsInvalidPaths(t *testing.T) {
	for _, kind := range []string{"relative", "missing", "file"} {
		t.Run(kind, func(t *testing.T) {
			// Given an invalid project path.
			d := startDex(t, t.TempDir(), "")
			id := d.newSession(t).ID
			path := "relative/path"
			if kind == "missing" {
				path = filepath.Join(t.TempDir(), "missing")
			}
			if kind == "file" {
				path = filepath.Join(t.TempDir(), "file")
				writeFile(t, path, "file")
			}
			// When the user sets it, the API refuses it without changing the Chat.
			d.must(t, http.MethodPut, projectURL(id), map[string]string{"cwd": path}, nil, http.StatusBadRequest)
			assertProject(t, d, id, "")
		})
	}
}

func TestUserMainRefusesProject(t *testing.T) {
	// Given Main, even a valid path is forbidden.
	d := startDex(t, t.TempDir(), "")
	id := d.mainChat(t)
	// When the user sets a project, the API returns 403.
	d.must(t, http.MethodPut, projectURL(id), map[string]string{"cwd": t.TempDir()}, nil, http.StatusForbidden)
	assertProject(t, d, id, "")
}

func TestUserChatProjectLoadsSkills(t *testing.T) {
	// Given a skill that exists only in a project directory.
	project := t.TempDir()
	writeSkill(t, filepath.Join(project, ".agents", "skills"), "project-check", "description: Project-only check")
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "section")
	id := d.newSession(t).ID
	// When the user selects the project, its skill enters the prompt.
	d.must(t, http.MethodPut, projectURL(id), map[string]string{"cwd": project}, nil)
	if got := taskTurn(t, d, id, "skills"); !strings.Contains(got, "Project-only check") {
		t.Fatal(got)
	}
	if got := taskTurn(t, d, id, "project"); !strings.Contains(got, project) || !strings.Contains(got, "set_project") {
		t.Fatal(got)
	}
}

func TestUserChatToolSetsProjectForLaterMessages(t *testing.T) {
	// Given a Chat using the real tool boundary.
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := startDex(t, t.TempDir(), "")
	id := d.newSession(t).ID
	// When set_project runs, it returns the stored path and updates the API.
	got := taskTool(t, d, id, "set_project", map[string]string{"path": project})
	if !strings.Contains(got, project) {
		t.Fatalf("set_project result: %s", got)
	}
	assertProject(t, d, id, project)
	d.setModel(t, "bash")
	// Then a later message runs in the selected directory.
	if got := taskTurn(t, d, id, "pwd"); got != "bash: "+project {
		t.Fatal(got)
	}
	got = taskTool(t, d, id, "set_project", map[string]string{"path": ""})
	if !strings.Contains(got, `""`) {
		t.Fatal(got)
	}
	assertProject(t, d, id, "")
}

func TestUserMainToolRefusesProject(t *testing.T) {
	// Given Main with a valid directory.
	d := startDex(t, t.TempDir(), "")
	id := d.mainChat(t)
	// When set_project runs, it returns a tool error and preserves the workspace.
	got := taskTool(t, d, id, "set_project", map[string]string{"path": t.TempDir()})
	if !strings.Contains(got, "only Chat sessions") {
		t.Fatalf("set_project error: %s", got)
	}
	assertProject(t, d, id, "")
}

func TestUserChatTaskInheritsProject(t *testing.T) {
	// Given a Chat with a selected project.
	data, project := t.TempDir(), t.TempDir()
	project, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	d := startDex(t, data, "")
	id := d.newSession(t).ID
	d.must(t, http.MethodPut, projectURL(id), map[string]string{"cwd": project}, nil)
	// When the Chat spawns a bash task, its command uses the parent's project.
	spawnExecution(t, d, id, "Project task", "pwd > task-cwd", "bash")
	task := waitExecution(t, taskResultDB(t, data), id, "Project task", "done")
	// Then both the shell artifact and the saved report prove the directory.
	content, err := os.ReadFile(filepath.Join(project, "task-cwd"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(content)) != project {
		t.Fatalf("task cwd: %s", content)
	}
	t.Logf("task=%s cwd=%s report=%q", task.ID, project, task.Report)
}

func TestUserChatProjectExpandsHomeAndSurvivesRestart(t *testing.T) {
	// Given a Chat with a project expressed relative to the server's home.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	d := startDex(t, data, "")
	id := d.newSession(t).ID
	for _, path := range []string{"~", "~/./"} {
		// When the same setting is saved again, it stores the same clean absolute path.
		d.must(t, http.MethodPut, projectURL(id), map[string]string{"cwd": path}, nil)
		assertProject(t, d, id, filepath.Clean(home))
	}
	d.stop()
	// Then restarting dex keeps the saved project directory.
	d = startDex(t, data, "")
	assertProject(t, d, id, filepath.Clean(home))
}

func TestUserOtherSessionsRefuseProject(t *testing.T) {
	for _, key := range []string{"telegram:7", "task:test", "automation:test", "dream:test"} {
		t.Run(key, func(t *testing.T) {
			// Given a stored conversation outside Chat sessions.
			data := t.TempDir()
			d := startDex(t, data, "")
			id := sessionFixture(t, d, key, 1)
			// When a user tries to set or clear its project, the API returns 403.
			for _, path := range []string{t.TempDir(), ""} {
				d.must(t, http.MethodPut, projectURL(id), map[string]string{"cwd": path}, nil, http.StatusForbidden)
			}
		})
	}
}

func TestUserChatProjectKeepsUploadedFilesAccessible(t *testing.T) {
	// Given an upload in the workspace and a Chat in a different project directory.
	data := t.TempDir()
	d := startDex(t, data, "")
	d.setModel(t, "echo")
	file := d.mustUpload(t, "project-notes.txt", []byte("project evidence"))
	id := d.newSession(t).ID
	d.must(t, http.MethodPut, projectURL(id), map[string]string{"cwd": t.TempDir()}, nil)
	// When the message has an attachment, the model gets its absolute workspace path.
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/messages", id), map[string]any{
		"id": uuid.NewV7().String(), "text": "read notes", "attachments": []string{file.ID},
	}, nil, http.StatusAccepted)
	path := filepath.Join(data, "agents", "default", "workspace", file.Path)
	d.watch(t, id).until(t, "absolute attachment path", answered("echo: read notes\n\nAttached files:\n- "+path))
	// Then that path resolves to the uploaded file, regardless of the selected project.
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "project evidence" {
		t.Fatalf("attachment: %q %v", content, err)
	}
}
