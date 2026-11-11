package app_test

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
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

func TestUserOpensLegacyTaskData(t *testing.T) {
	t.Parallel()
	// Given the data of a dex version with background tasks: two finished tasks and one running.
	dir := legacyData(t)

	// When the current dex starts on it.
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)

	// Then each task conversation is a child of its parent chat, and each task input is a parent turn.
	rows, err := conn.QueryContext(t.Context(), `SELECT t.title, c.parent_id, s.parent_request_id, source.id
		FROM tasks t
		JOIN conversations c ON c.id = t.conversation_id
		JOIN submissions s ON s.id = t.id
		JOIN submissions source ON source.id = t.source_request_id
		ORDER BY t.rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	titles := []string{}
	for rows.Next() {
		var title, requestID, sourceID string
		var parent int64
		if err := rows.Scan(&title, &parent, &requestID, &sourceID); err != nil {
			t.Fatal(err)
		}
		if parent != 1 || requestID != sourceID {
			t.Fatalf("%s: parent=%d parent_request_id=%s source=%s", title, parent, requestID, sourceID)
		}
		titles = append(titles, title)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(titles) != 3 {
		t.Fatalf("tasks: %v", titles)
	}
	// And the old results stay readable in the parent.
	var results int
	if err := conn.QueryRowContext(t.Context(), `SELECT count(*) FROM submissions WHERE conversation_id = 1 AND kind = 'task_result' AND id LIKE 'task:%:result' AND status = 'done'`).Scan(&results); err != nil || results != 2 {
		t.Fatalf("old results: %d, %v", results, err)
	}
	d.chats(t)
}
