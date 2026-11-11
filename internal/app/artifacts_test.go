package app_test

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"uuid"

	"github.com/recally-io/dex/internal/artifact"
	"github.com/recally-io/dex/internal/engine"
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

func TestUserConcurrentDeliveriesRemainAvailable(t *testing.T) {
	data := t.TempDir()
	d := startDex(t, data, "")
	d.setModel(t, "bash")
	chat := d.newSession(t).ID
	gate := filepath.Join(data, "delivery")
	request := d.send(t, chat, taskGate(t, data, "delivery"))
	waitTaskFile(t, gate+".pid")
	workspace := filepath.Join(data, "agents", "default", "workspace")
	if err := os.WriteFile(filepath.Join(workspace, "parallel.md"), []byte("parallel\n"), 0600); err != nil {
		t.Fatal(err)
	}
	conn := sessionDB(t, d)
	var execution int64
	if err := conn.QueryRowContext(t.Context(), "SELECT execution_id FROM conversations WHERE id=?", chat).Scan(&execution); err != nil {
		t.Fatal(err)
	}
	service := artifact.New(conn, data)
	const count = 16
	results := make(chan error, count)
	start := make(chan struct{})
	for i := range count {
		go func() {
			<-start
			_, err := service.Deliver(t.Context(), engine.TaskContext{AgentID: "default", ConversationID: execution, SourceRequestID: request, Key: fmt.Sprintf("parallel/%d", i)}, "parallel.md")
			results <- err
		}()
	}
	close(start)
	for range count {
		if err := <-results; err != nil {
			t.Errorf("concurrent delivery: %v", err)
		}
	}
	rows := listArtifacts(t, d, chat)
	if len(rows) != count {
		t.Fatalf("HTTP returned %d snapshots, want %d", len(rows), count)
	}
	// Stop closes the input. Replay still returns the committed snapshot, but a new call is refused.
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/abort", chat), nil, nil)
	releaseResultGate(t, data, "delivery")
	eventually(t, "delivery source settled", func() (bool, any) {
		var status string
		err := conn.QueryRowContext(t.Context(), "SELECT status FROM submissions WHERE id=?", request).Scan(&status)
		return err == nil && status != "pending", status
	})
	caller := engine.TaskContext{AgentID: "default", ConversationID: execution, SourceRequestID: request, Key: "parallel/0"}
	if replay, err := service.Deliver(t.Context(), caller, "parallel.md"); err != nil || replay.ID == "" {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	caller.Key = "parallel/new"
	if _, err := service.Deliver(t.Context(), caller, "parallel.md"); err == nil {
		t.Fatal("settled input accepted a new delivery")
	}
	t.Logf("concurrent HTTP snapshots=%d", len(rows))
}

func TestUserConcurrentHTTPDeliveries(t *testing.T) {
	data := t.TempDir()
	d := startDex(t, data, "")
	d.setModel(t, "tool")
	workspace := filepath.Join(data, "agents", "default", "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "http.md"), []byte("HTTP\n"), 0600); err != nil {
		t.Fatal(err)
	}
	const count = 8
	chats := make([]int64, count)
	for i := range count {
		// Populate each Chat so the next create does not reuse the empty session.
		chats[i] = d.newChat(t, "unknown_tool {}")
		d.watch(t, chats[i]).until(t, "first input", answeredWith("not available"))
	}
	start := make(chan struct{})
	results := make(chan error, count)
	for _, chatID := range chats {
		go func() {
			<-start
			body, err := json.Marshal(map[string]string{"id": uuid.NewV7().String(), "text": `deliver_file {"path":"http.md"}`})
			if err == nil {
				var req *http.Request
				req, err = http.NewRequestWithContext(t.Context(), http.MethodPost, fmt.Sprintf("%s/api/agents/default/chats/%d/messages", d.srv.URL, chatID), bytes.NewReader(body))
				if err == nil {
					req.Header.Set("Content-Type", "application/json")
					var response *http.Response
					response, err = d.srv.Client().Do(req)
					if err == nil {
						_ = response.Body.Close()
						if response.StatusCode != http.StatusAccepted {
							err = fmt.Errorf("chat %d: HTTP %d", chatID, response.StatusCode)
						}
					}
				}
			}
			results <- err
		}()
	}
	close(start)
	for range count {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	for _, chatID := range chats {
		eventually(t, "HTTP delivery", func() (bool, any) {
			rows := listArtifacts(t, d, chatID)
			return len(rows) == 1, rows
		})
	}
}

func TestUserRoutineAndItsTaskDeliverFiles(t *testing.T) {
	data := t.TempDir()
	d := startDex(t, data, "")
	workspace := filepath.Join(data, "agents", "default", "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "routine.markdown"), []byte("# Routine\n"), 0600); err != nil {
		t.Fatal(err)
	}
	d.setModel(t, "tool")
	a := d.createAutomation(t, "Delivery", `deliver_file {"path":"routine.markdown"}`, "new", map[string]any{"kind": "daily", "time": "09:00"})
	run := d.runNow(t, a.ID)
	settledRoutine(t, d, a.ID, run.ID)
	if rows := listArtifacts(t, d, run.ChatID); len(rows) != 1 || rows[0].ConversationID != run.ChatID {
		t.Fatalf("routine artifacts=%+v", rows)
	}
	d.setModel(t, "spawn")
	b := d.createAutomation(t, "Task delivery", childJSON(t, map[string]any{"title": "Routine child", "message": `deliver_file {"path":"routine.markdown"}`, "provider": "faux", "modelId": "tool"}), "new", map[string]any{"kind": "daily", "time": "09:00"})
	run = d.runNow(t, b.ID)
	waitResults(t, taskResultDB(t, data), run.ChatID, 1)
	var child int64
	if err := taskResultDB(t, data).QueryRowContext(t.Context(), `SELECT id FROM conversations WHERE parent_id = ?`, run.ChatID).Scan(&child); err != nil {
		t.Fatal(err)
	}
	rows := listArtifacts(t, d, run.ChatID)
	if len(rows) != 1 || rows[0].ConversationID != child || rows[0].DisplayConversationID != run.ChatID {
		t.Fatalf("child=%d artifacts=%+v", child, rows)
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

	// When the child session calls the same explicit delivery tool.
	child := spawnChild(t, d, parent, "Deliver task result", `deliver_file {"path":"task.md"}`, "tool")
	waitResults(t, conn, parent, 1)
	childID := child.conversation(t)
	parentArtifacts := listArtifacts(t, d, parent)
	childArtifacts := listArtifacts(t, d, childID)
	if len(parentArtifacts) != 1 || len(childArtifacts) != 1 || parentArtifacts[0] != childArtifacts[0] || parentArtifacts[0].Name != "task.md" || parentArtifacts[0].ConversationID != childID || parentArtifacts[0].DisplayConversationID != parent {
		t.Fatalf("child=%d parent=%+v childArtifacts=%+v", childID, parentArtifacts, childArtifacts)
	}
}
