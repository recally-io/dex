package app_test

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestUserDeliversFileInContinuedTaskWithoutSendingItToParent(t *testing.T) {
	t.Parallel()
	// Given a finished task with a report and a workspace file for follow-up work.
	data := t.TempDir()
	d := startDex(t, data, "")
	conn := taskResultDB(t, data)
	d.setModel(t, "spawn")
	parent := d.newChat(t, taskJSON(t, map[string]any{"title": "Continue delivery", "goal": continueReport, "provider": "faux", "modelId": "tool"}))
	task := waitExecution(t, conn, parent, "Continue delivery", "done")
	waitResultSubmission(t, conn, "task:"+task.ID+":result", "done")
	conv := taskConversation(t, d, task.ID)
	workspace := filepath.Join(data, "agents", "default", "workspace")
	if err := os.WriteFile(filepath.Join(workspace, "continued.md"), []byte("follow-up report\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var before taskInfo
	d.must(t, http.MethodGet, "/api/agents/default/tasks/"+task.ID, nil, &before)

	// When the user continues the task session and delivers the new file.
	delivered := deliverArtifact(t, d, conv, "continued.md")

	// Then the snapshot belongs only to the continued session.
	if delivered.ConversationID != conv || delivered.DisplayConversationID != conv {
		t.Fatalf("continued delivery: %+v", delivered)
	}
	if rows := listArtifacts(t, d, parent); len(rows) != 0 {
		t.Fatalf("parent received follow-up artifacts: %+v", rows)
	}
	if rows := listArtifacts(t, d, conv); len(rows) != 1 || rows[0].ID != delivered.ID {
		t.Fatalf("continued session artifacts: %+v", rows)
	}
	_, body := readArtifact(t, d, conv, delivered.ID)
	if string(body) != "follow-up report\n" {
		t.Fatalf("snapshot: %q", body)
	}

	// And follow-up work does not replace the task report or send another result.
	var after taskInfo
	d.must(t, http.MethodGet, "/api/agents/default/tasks/"+task.ID, nil, &after)
	if after.Report != before.Report || after.Status != "done" || countResults(t, conn, parent) != 1 {
		t.Fatalf("continued delivery changed the task: before=%+v after=%+v", before, after)
	}
}
