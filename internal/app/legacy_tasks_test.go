package app_test

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// legacyData writes the data directory of dex commit 096a9c7, the last one with background tasks.
// See testdata/legacy-tasks/README.md for its conversations.
func legacyData(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for dump, target := range map[string]string{
		"control.sql": filepath.Join(dir, "control.sqlite"),
		"state.sql":   filepath.Join(dir, "agents", "default", "state.sqlite"),
	} {
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		script, err := os.ReadFile(filepath.Join("testdata", "legacy-tasks", dump))
		if err != nil {
			t.Fatal(err)
		}
		store, err := sql.Open("sqlite", "file:"+target+"?_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatal(err)
		}
		_, loadErr := store.ExecContext(t.Context(), string(script))
		if err := errors.Join(loadErr, store.Close()); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// legacyTask is a row of the tasks table in the data of dex commit 096a9c7.
type legacyTask struct {
	id, source, title string
	conversation      int64
}

// legacyTasks reads the task rows of legacy data before dex upgrades it. The upgrade drops the table.
func legacyTasks(t *testing.T, dir string) []legacyTask {
	t.Helper()
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "control.sqlite")+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	rows, err := conn.QueryContext(t.Context(), "SELECT id, source_request_id, conversation_id, title FROM tasks ORDER BY rowid")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var tasks []legacyTask
	for rows.Next() {
		var k legacyTask
		if err := rows.Scan(&k.id, &k.source, &k.conversation, &k.title); err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, k)
	}
	if err := rows.Err(); err != nil || len(tasks) != 3 {
		t.Fatalf("tasks %+v, %v", tasks, err)
	}
	return tasks
}

func TestUserOpensLegacyTaskData(t *testing.T) {
	t.Parallel()
	// Given the data of a dex version with background tasks: two finished tasks and one running.
	dir := legacyData(t)

	tasks := legacyTasks(t, dir)

	// When the current dex starts on it.
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)

	// Then each task conversation is a child of its parent chat, and each task input is a parent turn.
	for _, k := range tasks {
		var parent int64
		var request string
		if err := conn.QueryRowContext(t.Context(), `SELECT c.parent_id, s.parent_request_id FROM submissions s JOIN conversations c ON c.id = s.conversation_id WHERE s.id = ?`, k.id).Scan(&parent, &request); err != nil {
			t.Fatalf("%s: %v", k.title, err)
		}
		if parent != 1 || request != k.source {
			t.Fatalf("%s: parent=%d parent_request_id=%s source=%s", k.title, parent, request, k.source)
		}
	}
	// And the task tables are gone.
	var tables int
	if err := conn.QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_master WHERE name IN ('tasks', 'task_steer_receipts')`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("task tables left: %d, %v", tables, err)
	}
	// And the old results keep their content in the parent.
	contents := map[string]string{}
	resultRows, err := conn.QueryContext(t.Context(), `SELECT id, content FROM submissions WHERE conversation_id = 1 AND kind = 'task_result' AND id LIKE 'task:%:result' AND status = 'done'`)
	if err != nil {
		t.Fatal(err)
	}
	defer resultRows.Close()
	for resultRows.Next() {
		var id, content string
		if err := resultRows.Scan(&id, &content); err != nil {
			t.Fatal(err)
		}
		contents[id] = content
	}
	if err := resultRows.Err(); err != nil || len(contents) != 2 {
		t.Fatalf("old results: %v, %v", contents, err)
	}
	for _, want := range []string{`"title":"Ended"`, `"report":"echo: ended goal"`, `"title":"Continued"`, `"report":"echo: continued goal"`} {
		found := false
		for _, content := range contents {
			found = found || strings.Contains(content, want)
		}
		if !found {
			t.Fatalf("no old result has %s: %v", want, contents)
		}
	}
	// And the transcript of the parent reads both results with their task titles.
	d.watch(t, 1).until(t, "old results in the transcript", func(tr *transcript) bool {
		titles := map[string]bool{}
		for _, it := range tr.items {
			if it.Kind == "task" {
				titles[it.Title] = true
			}
		}
		return titles["Ended"] && titles["Continued"]
	})
	d.chats(t)
}
