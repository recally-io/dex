package app_test

import (
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type deliveredArtifact struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	Size                  int64  `json:"size"`
	ConversationID        int64  `json:"conversationId"`
	DisplayConversationID int64  `json:"displayConversationId"`
}

func artifactPath(chatID int64, artifactID string) string {
	return fmt.Sprintf("/api/agents/default/chats/%d/artifacts/%s", chatID, artifactID)
}

func listArtifacts(t *testing.T, d *dex, chatID int64) []deliveredArtifact {
	t.Helper()
	var result struct {
		Artifacts []deliveredArtifact `json:"artifacts"`
	}
	d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d/artifacts", chatID), nil, &result)
	return result.Artifacts
}

func readArtifact(t *testing.T, d *dex, chatID int64, artifactID string) (http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, d.srv.URL+artifactPath(chatID, artifactID), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := d.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(response.Body)
		t.Fatalf("read artifact: status=%d body=%s err=%v", response.StatusCode, body, readErr)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.Header, body
}

func deliverArtifact(t *testing.T, d *dex, chatID int64, path string) deliveredArtifact {
	t.Helper()
	answer := taskTool(t, d, chatID, "deliver_file", map[string]string{"path": path})
	var delivered deliveredArtifact
	if len(answer) < len("tool: ") || answer[:len("tool: ")] != "tool: " {
		t.Fatalf("deliver_file: %s", answer)
	}
	if err := json.Unmarshal([]byte(answer[len("tool: "):]), &delivered); err != nil {
		t.Fatalf("deliver_file: %s: %v", answer, err)
	}
	return delivered
}

func TestUserDeliversWorkspaceSnapshotsFromAProjectChat(t *testing.T) {
	t.Parallel()
	// Given a Chat that works in a project outside its agent workspace.
	data := t.TempDir()
	d := startDex(t, data, "")
	chat := d.newSession(t)
	project := t.TempDir()
	d.must(t, http.MethodPut, projectURL(chat.ID), map[string]string{"cwd": project}, nil)
	workspace := filepath.Join(data, "agents", "default", "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "report.md"), []byte("# Original report\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "unsafe.html"), []byte("<script>window.pwned = true</script>"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "chart.png"), pngImage(t), 0600); err != nil {
		t.Fatal(err)
	}

	// When the agent explicitly delivers workspace files and then edits the report source.
	report := deliverArtifact(t, d, chat.ID, "report.md")
	html := deliverArtifact(t, d, chat.ID, "unsafe.html")
	image := deliverArtifact(t, d, chat.ID, "chart.png")
	if err := os.WriteFile(filepath.Join(workspace, "report.md"), []byte("# Changed report\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// Then Dex lists immutable snapshots and only makes safe image types eligible for inline display.
	artifacts := listArtifacts(t, d, chat.ID)
	if len(artifacts) != 3 || artifacts[0].ID != report.ID || artifacts[0].Name != "report.md" || artifacts[0].ConversationID != chat.ID || artifacts[0].DisplayConversationID != chat.ID {
		t.Fatalf("artifacts=%+v", artifacts)
	}
	headers, body := readArtifact(t, d, chat.ID, report.ID)
	if string(body) != "# Original report\n" || headers.Get("Content-Type") != "application/octet-stream" || headers.Get("X-Content-Type-Options") != "nosniff" || headers.Get("Content-Disposition") == "" {
		t.Fatalf("report headers=%v body=%q", headers, body)
	}
	headers, body = readArtifact(t, d, chat.ID, html.ID)
	if string(body) != "<script>window.pwned = true</script>" || headers.Get("Content-Type") != "application/octet-stream" || headers.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("html headers=%v body=%q", headers, body)
	}
	headers, _ = readArtifact(t, d, chat.ID, image.ID)
	if headers.Get("Content-Type") != "image/png" || headers.Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(headers.Get("Content-Disposition"), "inline;") {
		t.Fatalf("image headers=%v", headers)
	}
	t.Logf("artifact: project=%s chat=%d report=%s html=%s image=%s", project, chat.ID, report.ID, html.ID, image.ID)
}

func TestUserDoesNotPublishPathsFromATaskReport(t *testing.T) {
	t.Parallel()
	// Given a background task report that names a workspace file without delivering it.
	data := t.TempDir()
	d := startDex(t, data, "")
	workspace := filepath.Join(data, "agents", "default", "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "private.md"), []byte("not delivered\n"), 0600); err != nil {
		t.Fatal(err)
	}
	parent := d.newSession(t).ID
	conn := taskResultDB(t, data)

	// When the task saves that free-form path in report_task.files.
	spawnExecution(t, d, parent, "Report only", `report_task {"outcome":"done","summary":"reported a private path","cause":"","reproduction":[],"files":["private.md"],"openItems":[]}`, "tool")
	waitExecution(t, conn, parent, "Report only", "done")

	// Then no HTTP-readable artifact is created.
	if artifacts := listArtifacts(t, d, parent); len(artifacts) != 0 {
		t.Fatalf("reported path became public: %+v", artifacts)
	}
}

func TestUserCleansAnUnpublishedSnapshotAfterDeliveryFails(t *testing.T) {
	t.Parallel()
	// Given a report and a database failure after the snapshot has been copied.
	data := t.TempDir()
	d := startDex(t, data, "")
	workspace := filepath.Join(data, "agents", "default", "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "report.md"), []byte("# report\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := sessionDB(t, d).ExecContext(t.Context(), "CREATE TRIGGER reject_artifact BEFORE INSERT ON artifacts BEGIN SELECT RAISE(ABORT, 'artifact receipt failed'); END"); err != nil {
		t.Fatal(err)
	}
	chat := d.newSession(t)

	// When the agent requests an explicit delivery.
	d.setModel(t, "tool")
	_ = taskTurn(t, d, chat.ID, `deliver_file {"path":"report.md"}`)

	// Then neither a public record nor an orphaned snapshot remains.
	if artifacts := listArtifacts(t, d, chat.ID); len(artifacts) != 0 {
		t.Fatalf("artifacts=%+v", artifacts)
	}
	entries, err := os.ReadDir(filepath.Join(data, "agents", "default", "artifacts"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("orphaned snapshots=%v", entries)
	}
}

func TestUserFindsBackgroundDeliveriesFromTheirParentChat(t *testing.T) {
	t.Parallel()
	// Given a task whose source file is already in the shared agent workspace.
	data := t.TempDir()
	d := startDex(t, data, "")
	workspace := filepath.Join(data, "agents", "default", "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "task.md"), []byte("# Task result\n"), 0600); err != nil {
		t.Fatal(err)
	}
	parent := d.newSession(t).ID
	conn := taskResultDB(t, data)

	// When the background agent calls the same explicit delivery tool.
	spawnExecution(t, d, parent, "Deliver task result", `deliver_file {"path":"task.md"}`, "tool")
	task := waitExecution(t, conn, parent, "Deliver task result", "done")
	var taskView taskInfo
	d.must(t, http.MethodGet, "/api/agents/default/tasks/"+task.ID, nil, &taskView)
	parentArtifacts := listArtifacts(t, d, parent)
	taskArtifacts := listArtifacts(t, d, taskView.ConversationID)
	if len(parentArtifacts) != 1 || len(taskArtifacts) != 1 || parentArtifacts[0] != taskArtifacts[0] || parentArtifacts[0].Name != "task.md" || parentArtifacts[0].ConversationID != taskView.ConversationID || parentArtifacts[0].DisplayConversationID != parent {
		t.Fatalf("task=%+v parent=%+v taskArtifacts=%+v", taskView, parentArtifacts, taskArtifacts)
	}

	// Then an unrelated Chat and another agent cannot fetch the snapshot.
	otherChat := d.newSession(t).ID
	d.must(t, http.MethodGet, artifactPath(otherChat, parentArtifacts[0].ID), nil, nil, http.StatusNotFound)
	var other apiAgent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "Other", "provider": "faux", "modelId": "echo", "thinkingLevel": "off"}, &other, http.StatusCreated)
	d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/%s/chats/%d/artifacts/%s", other.ID, parent, parentArtifacts[0].ID), nil, nil, http.StatusNotFound)
	t.Logf("artifact: task=%s parent=%d source=%d id=%s", task.ID, parent, taskView.ConversationID, parentArtifacts[0].ID)
}
