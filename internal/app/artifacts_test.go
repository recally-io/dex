package app_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
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
	replay, err := service.Deliver(t.Context(), caller, "parallel.md")
	if err != nil {
		t.Fatal(err)
	}
	d.must(t, http.MethodDelete, artifactPath(chat, replay.ID), nil, nil)
	if again, err := service.Deliver(t.Context(), caller, "parallel.md"); err != nil || again.ID != replay.ID {
		t.Fatalf("deleted replay=%+v err=%v", again, err)
	}
	if rows := listArtifacts(t, d, chat); len(rows) != count-1 {
		t.Fatalf("replay restored deleted delivery: %+v", rows)
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
	if _, err := sessionDB(t, d).ExecContext(t.Context(), "CREATE TRIGGER reject_artifact BEFORE UPDATE OF id ON submissions BEGIN SELECT RAISE(ABORT, 'artifact receipt failed'); END"); err != nil {
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
	entries, err := os.ReadDir(filepath.Join(data, "artifacts"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			t.Fatalf("orphaned snapshot=%s", entry.Name())
		}
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

func TestUserManagesDeliveredSnapshotsWithoutChangingSources(t *testing.T) {
	t.Parallel()
	// Given a delivered report shared by a child and its parent.
	data := t.TempDir()
	d := startDex(t, data, "")
	workspace := filepath.Join(data, "agents", "default", "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(workspace, "report.md")
	if err := os.WriteFile(source, []byte("# Source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	parent := d.newSession(t).ID
	child := spawnChild(t, d, parent, "Report", `deliver_file {"path":"report.md"}`, "tool")
	waitResults(t, taskResultDB(t, data), parent, 1)
	childID := child.conversation(t)
	item := listArtifacts(t, d, parent)[0]

	// When the user renames the delivery, both views and its download use the new name.
	d.must(t, http.MethodPatch, artifactPath(parent, item.ID), map[string]string{"name": "renamed.md"}, nil)
	for _, chat := range []int64{parent, childID} {
		rows := listArtifacts(t, d, chat)
		if len(rows) != 1 || rows[0].Name != "renamed.md" {
			t.Fatalf("renamed artifacts=%+v", rows)
		}
	}
	headers, body := readArtifact(t, d, parent, item.ID)
	if string(body) != "# Source\n" || !strings.Contains(headers.Get("Content-Disposition"), "renamed.md") {
		t.Fatalf("headers=%v body=%q", headers, body)
	}
	for _, name := range []string{"", " ", "..", "../outside", "a/b", `a\b`, "bad\nname", strings.Repeat("a", 256)} {
		status, _ := d.call(t, http.MethodPatch, artifactPath(parent, item.ID), map[string]string{"name": name})
		if status != http.StatusBadRequest {
			t.Fatalf("name %q: status=%d", name, status)
		}
	}
	other := d.newSession(t).ID
	for _, method := range []string{http.MethodPatch, http.MethodDelete} {
		status, _ := d.call(t, method, artifactPath(other, item.ID), map[string]string{"name": "intruder.md"})
		if status != http.StatusNotFound {
			t.Fatalf("unrelated chat %s: status=%d", method, status)
		}
	}

	// Then deletion removes the shared snapshot, but leaves the source file untouched.
	snapshotPath := artifactFile(t, data, item.ID)
	d.must(t, http.MethodDelete, artifactPath(childID, item.ID), nil, nil)
	for _, chat := range []int64{parent, childID} {
		if rows := listArtifacts(t, d, chat); len(rows) != 0 {
			t.Fatalf("deleted artifacts=%+v", rows)
		}
		status, _ := d.call(t, http.MethodGet, artifactPath(chat, item.ID), nil)
		if status != http.StatusNotFound {
			t.Fatalf("deleted download status=%d", status)
		}
	}
	if _, err := os.Stat(snapshotPath); !os.IsNotExist(err) {
		t.Fatalf("snapshot remains: %v", err)
	}
	if contents, err := os.ReadFile(source); err != nil || string(contents) != "# Source\n" {
		t.Fatalf("source=%q err=%v", contents, err)
	}

	// A removed snapshot is absent from the list even if its delivery record remains.
	d.setModel(t, "tool")
	missing := deliverArtifact(t, d, parent, "report.md")
	if err := os.Remove(artifactFile(t, data, missing.ID)); err != nil {
		t.Fatal(err)
	}
	if rows := listArtifacts(t, d, parent); len(rows) != 0 {
		t.Fatalf("missing snapshot listed: %+v", rows)
	}
	d.must(t, http.MethodDelete, artifactPath(parent, missing.ID), nil, nil)
}

func TestUserUsesDiskAsTheOnlyArtifactState(t *testing.T) {
	t.Parallel()
	// Given a delivered snapshot, whose physical file name is visible to the user.
	data := t.TempDir()
	d := startDex(t, data, "")
	chat := d.newSession(t).ID
	workspace := filepath.Join(data, "agents", "default", "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "disk.md"), []byte("# Disk\n"), 0600); err != nil {
		t.Fatal(err)
	}
	item := deliverArtifact(t, d, chat, "disk.md")
	file := artifactFile(t, data, item.ID)
	if filepath.Base(file) != "disk.md" {
		t.Fatalf("physical snapshot name=%q, want disk.md", filepath.Base(file))
	}
	// When a user changes its real name and bytes outside dex, the API reads the disk state.
	renamed := filepath.Join(filepath.Dir(file), "external.markdown")
	if err := os.Rename(file, renamed); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(renamed, []byte("# Changed on disk\n"), 0600); err != nil {
		t.Fatal(err)
	}
	rows := listArtifacts(t, d, chat)
	if len(rows) != 1 || rows[0].Name != "external.markdown" || rows[0].Size != 18 || rows[0].ID != item.ID {
		t.Fatalf("disk artifacts=%+v", rows)
	}
	// No mutable artifact state or delivery receipt was written into SQLite.
	var records int
	if err := sessionDB(t, d).QueryRowContext(t.Context(), "SELECT count(*) FROM artifacts").Scan(&records); err != nil || records != 0 {
		t.Fatalf("artifact records=%d err=%v", records, err)
	}
	// Then internal rename and delete change the same physical file, without a second name index.
	d.must(t, http.MethodPatch, artifactPath(chat, item.ID), map[string]string{"name": "real.md"}, nil)
	if _, err := os.Stat(filepath.Join(filepath.Dir(file), "real.md")); err != nil {
		t.Fatal(err)
	}
	d.must(t, http.MethodDelete, artifactPath(chat, item.ID), nil, nil)
	if rows := listArtifacts(t, d, chat); len(rows) != 0 {
		t.Fatalf("deleted files=%+v", rows)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(file), "restored.md"), []byte("restored"), 0600); err != nil {
		t.Fatal(err)
	}
	if rows := listArtifacts(t, d, chat); len(rows) != 1 || rows[0].Name != "restored.md" {
		t.Fatalf("restored files=%+v", rows)
	}
	// A new disk-only snapshot in the conversation directory also appears without a DB row.
	added := filepath.Join(data, "artifacts", fmt.Sprintf("%d_%d", chat, chat), uuid.NewV7().String(), "added.md")
	if err := os.MkdirAll(filepath.Dir(added), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(added, []byte("added"), 0600); err != nil {
		t.Fatal(err)
	}
	if rows := listArtifacts(t, d, chat); len(rows) != 2 {
		t.Fatalf("added files=%+v", rows)
	}
}

func artifactFile(t *testing.T, data, id string) string {
	t.Helper()
	var found string
	err := filepath.WalkDir(filepath.Join(data, "artifacts"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && (entry.Name() == id || strings.HasPrefix(filepath.Base(filepath.Dir(path)), id+"_")) {
			found = path
		}
		return nil
	})
	if err != nil || found == "" {
		t.Fatalf("snapshot %s: path=%s err=%v", id, found, err)
	}
	return found
}

func TestUserRejectsUnsafeArtifactDeliveries(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	d := startDex(t, data, "")
	d.setModel(t, "tool")
	chat := d.newSession(t).ID
	workspace := filepath.Join(data, "agents", "default", "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "link")); err != nil {
		t.Fatal(err)
	}
	large := filepath.Join(workspace, "large.bin")
	if err := os.WriteFile(large, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(large, artifact.MaxSize+1); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../outside.md", outside, "link", "large.bin"} {
		body, err := json.Marshal(map[string]string{"path": name})
		if err != nil {
			t.Fatal(err)
		}
		_ = taskTurn(t, d, chat, "deliver_file "+string(body))
		if rows := listArtifacts(t, d, chat); len(rows) != 0 {
			t.Fatalf("unsafe path %s delivered %+v", name, rows)
		}
	}
}

func TestUserMigratesLegacyArtifactFilesOnce(t *testing.T) {
	t.Parallel()
	// Given legacy SQLite receipts and UUID files from the released layout.
	data := t.TempDir()
	d := startDex(t, data, "")
	d.setModel(t, "echo")
	source := d.newChat(t, "seed")
	parent := d.mainChat(t)
	conn := sessionDB(t, d)
	// Also accept databases that ran the earlier, unreleased PR build.
	if _, err := conn.ExecContext(t.Context(), "ALTER TABLE artifacts ADD COLUMN deleted INTEGER NOT NULL DEFAULT 0"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), "INSERT INTO goose_db_version (version_id,is_applied) VALUES (20261114090000,1)"); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(data, "agents", "default", "artifacts")
	if err := os.MkdirAll(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	id, missing, interrupted := uuid.NewV7().String(), uuid.NewV7().String(), uuid.NewV7().String()
	orphan := uuid.NewV7().String()
	if err := os.WriteFile(filepath.Join(legacy, orphan), []byte("unclaimed bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	for i, item := range []struct{ id, name string }{{id, "legacy.md"}, {missing, "missing.md"}, {interrupted, "before.md"}} {
		if _, err := conn.ExecContext(t.Context(), `INSERT INTO artifacts (id,agent_id,conversation_id,display_conversation_id,operation_key,name,size,created_at) VALUES (?,'default',?,?,?,?,999,?)`, item.id, source, parent, "legacy/"+item.id, item.name, i); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(legacy, id), []byte("legacy bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, interrupted), []byte("old bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after a prior import published a file but before it removed the old copy.
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte("default\x00legacy/"+interrupted)))
	recovered := filepath.Join(data, "artifacts", fmt.Sprintf("%d_%d", source, parent), interrupted+"_"+hash, "after.md")
	if err := os.MkdirAll(filepath.Dir(recovered), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recovered, []byte("recovered bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	d.stop()
	if err := os.WriteFile(filepath.Join(data, "artifacts", ".legacy-imported"), []byte("1"), 0600); err != nil {
		t.Fatal(err)
	}
	// When the app imports them before it serves HTTP, identity and sharing stay intact.
	d = startDex(t, data, "")
	for _, chat := range []int64{source, parent} {
		rows := listArtifacts(t, d, chat)
		if len(rows) != 2 {
			t.Fatalf("migrated files=%+v", rows)
		}
		for _, row := range rows {
			switch row.ID {
			case id:
				if row.Name != "legacy.md" || row.Size != 12 {
					t.Fatalf("legacy stat=%+v", row)
				}
			case interrupted:
				if row.Name != "after.md" || row.Size != 15 {
					t.Fatalf("recovered stat=%+v", row)
				}
			default:
				t.Fatalf("unexpected migrated file=%+v", row)
			}
		}
	}
	if _, body := readArtifact(t, d, parent, id); string(body) != "legacy bytes" {
		t.Fatalf("legacy bytes=%q", body)
	}
	if files, err := os.ReadDir(legacy); err != nil || len(files) != 1 || files[0].Name() != orphan {
		t.Fatalf("legacy duplicates or lost unclaimed file=%v err=%v", files, err)
	}
	if bytes, err := os.ReadFile(filepath.Join(legacy, orphan)); err != nil || string(bytes) != "unclaimed bytes" {
		t.Fatalf("unclaimed file changed: %q err=%v", bytes, err)
	}
	// Then future starts do not read or reconstruct the artifact index from SQLite.
	if _, err := sessionDB(t, d).ExecContext(t.Context(), "DROP TABLE artifacts"); err != nil {
		t.Fatal(err)
	}
	disk := artifactFile(t, data, id)
	renamed := filepath.Join(filepath.Dir(disk), "disk-name.md")
	if err := os.Rename(disk, renamed); err != nil {
		t.Fatal(err)
	}
	d.stop()
	d = startDex(t, data, "")
	rows := listArtifacts(t, d, parent)
	if len(rows) != 2 {
		t.Fatalf("disk-only restart=%+v", rows)
	}
	if !slices.ContainsFunc(rows, func(item deliveredArtifact) bool { return item.ID == id && item.Name == "disk-name.md" }) {
		t.Fatalf("disk-only restart=%+v", rows)
	}
	d.must(t, http.MethodDelete, artifactPath(parent, id), nil, nil)
	d.stop()
	d = startDex(t, data, "")
	if rows := listArtifacts(t, d, parent); len(rows) != 1 || rows[0].ID != interrupted {
		t.Fatalf("deleted file restored on restart=%+v", rows)
	}
}
