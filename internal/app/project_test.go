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

func projectURL(id int64) string { return fmt.Sprintf("/api/bots/default/sessions/%d/project", id) }

func assertProject(t *testing.T, d *dex, id int64, want string) {
	t.Helper()
	var got struct {
		Cwd *string `json:"cwd"`
	}
	d.must(t, http.MethodGet, fmt.Sprintf("/api/bots/default/sessions/%d", id), nil, &got)
	if got.Cwd == nil || *got.Cwd != want {
		t.Fatalf("Session cwd = %v, want %q", got.Cwd, want)
	}
}

func TestUserSessionProjectRunsAndClearsToWorkspace(t *testing.T) {
	t.Parallel()
	// Given a session and an existing project directory.
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

func TestUserSessionProjectRejectsInvalidPathsAndReadOnlySessions(t *testing.T) {
	t.Parallel()
	d := startDex(t, t.TempDir(), "")
	id := d.newSession(t).ID
	for _, kind := range []string{"relative", "missing", "file"} {
		t.Run("invalid_"+kind, func(t *testing.T) {
			path := "relative/path"
			if kind == "missing" {
				path = filepath.Join(t.TempDir(), "missing")
			}
			if kind == "file" {
				path = filepath.Join(t.TempDir(), "file")
				writeFile(t, path, "file")
			}
			// When the user sets an invalid path, the API refuses it without changing the Session.
			d.must(t, http.MethodPut, projectURL(id), map[string]string{"cwd": path}, nil, http.StatusBadRequest)
			assertProject(t, d, id, "")
		})
	}
	for _, key := range []string{"telegram:7", "task:test", "automation:test", "dream:test"} {
		t.Run("readonly_"+key, func(t *testing.T) {
			roID := sessionFixture(t, d, key, 1)
			// When a user tries to set or clear its project, the API returns 403.
			for _, path := range []string{t.TempDir(), ""} {
				d.must(t, http.MethodPut, projectURL(roID), map[string]string{"cwd": path}, nil, http.StatusForbidden)
			}
		})
	}
}

func TestUserSessionToolSetsProjectForLaterMessages(t *testing.T) {
	t.Parallel()
	// Given a Session using the real tool boundary.
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

func TestUserSessionProjectExpandsHomeAndSurvivesRestart(t *testing.T) {
	t.Parallel()
	// Given a Session with a project expressed relative to the server's home.
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

func TestUserSessionProjectKeepsUploadedFilesAccessible(t *testing.T) {
	t.Parallel()
	// Given an upload in the workspace and a Session in a different project directory.
	data := t.TempDir()
	d := startDex(t, data, "")
	d.setModel(t, "echo")
	file := d.mustUpload(t, "project-notes.txt", []byte("project evidence"))
	id := d.newSession(t).ID
	d.must(t, http.MethodPut, projectURL(id), map[string]string{"cwd": t.TempDir()}, nil)
	// When the message has an attachment, the model gets its absolute workspace path.
	d.must(t, http.MethodPost, fmt.Sprintf("/api/bots/default/sessions/%d/messages", id), map[string]any{
		"id": uuid.NewV7().String(), "text": "read notes", "attachments": []string{file.ID},
	}, nil, http.StatusAccepted)
	path := filepath.Join(data, "agents", "default", "workspace", file.Path)
	w := d.watch(t, id)
	w.until(t, "absolute attachment path", answered("echo: read notes\n\nAttached files:\n- "+path))
	// And the transcript shows the user's text without the file list.
	if user := w.tr.items[0]; user.Kind != "user" || user.Text != "read notes" || len(user.Attachments) != 1 {
		t.Fatalf("user item = %+v", user)
	}
	// Then that path resolves to the uploaded file, regardless of the selected project.
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "project evidence" {
		t.Fatalf("attachment: %q %v", content, err)
	}
}
