package app_test

import (
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
)

// The tests in this file read child sessions through the HTTP API that the web UI uses.

type webChat struct {
	ID         int64  `json:"id"`
	Channel    string `json:"channel"`
	Busy       bool   `json:"busy"`
	ReadOnly   bool   `json:"readOnly"`
	ParentID   *int64 `json:"parentId"`
	ArchivedAt *int64 `json:"archivedAt"`
	// The list sets the activity fields while a session is busy.
	LastActivityAt *int64 `json:"lastActivityAt"`
	ToolStartedAt  *int64 `json:"toolStartedAt"`
}

func (d *dex) listWebChats(t *testing.T) map[int64]webChat {
	t.Helper()
	var list struct {
		Chats []webChat `json:"chats"`
	}
	d.must(t, http.MethodGet, "/api/agents/default/chats", nil, &list)
	out := map[int64]webChat{}
	for _, c := range list.Chats {
		out[c.ID] = c
	}
	return out
}

func TestUserOpensTheChildSessionOfAResultLink(t *testing.T) {
	t.Parallel()
	// Given a parent chat whose child session has answered once.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := newParent(t, d)
	child := spawnChild(t, d, parent, "Linked", "echo linked", "echo")
	turn := waitDoneChildTurns(t, conn, child.conversation(t), 1)[0].id
	waitResults(t, conn, parent, 1)

	// When the web opens the link /tasks/<turn> of the result mark.
	var opened webChat
	d.must(t, http.MethodGet, "/api/agents/default/turns/"+turn, nil, &opened)

	// Then it gets the child session, which is writable and names its parent.
	if opened.ID != child.conversation(t) || opened.ParentID == nil || *opened.ParentID != parent || opened.ReadOnly || opened.Channel != "web" {
		t.Fatalf("turn %s opened %+v; child=%+v parent=%d", turn, opened, child, parent)
	}
	// And the session list carries the same parent link.
	if listed := d.listWebChats(t)[opened.ID]; listed.ParentID == nil || *listed.ParentID != parent {
		t.Fatalf("listed child: %+v", listed)
	}

	// And an unknown turn and another bot's view of this turn are 404.
	var other apiAgent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "B", "provider": "faux", "modelId": "echo", "thinkingLevel": "off"}, &other, http.StatusCreated)
	for _, path := range []string{"/api/agents/default/turns/missing", "/api/agents/" + other.ID + "/turns/" + turn} {
		if status, raw := d.call(t, http.MethodGet, path, nil); status != http.StatusNotFound {
			t.Fatalf("GET %s: %d %s", path, status, raw)
		}
	}
}

func TestUserOpensAnOldTaskLinkAndArchivesItsSession(t *testing.T) {
	t.Parallel()
	// Given the data of a dex version with background tasks.
	dir := legacyData(t)
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	rows, err := conn.QueryContext(t.Context(), "SELECT id, conversation_id, title FROM tasks ORDER BY rowid")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	type task struct {
		id, title    string
		conversation int64
	}
	var tasks []task
	for rows.Next() {
		var k task
		if err := rows.Scan(&k.id, &k.conversation, &k.title); err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, k)
	}
	if err := rows.Err(); err != nil || len(tasks) != 3 {
		t.Fatalf("tasks %+v, %v", tasks, err)
	}

	for _, k := range tasks {
		// When the web opens the old link /tasks/<task id>.
		var opened webChat
		d.must(t, http.MethodGet, "/api/agents/default/turns/"+k.id, nil, &opened)

		// Then it gets the task conversation as a writable child session of its parent chat, not as a Task kind.
		if opened.ID != k.conversation || opened.ParentID == nil || *opened.ParentID != 1 || opened.ReadOnly || opened.Channel != "web" {
			t.Fatalf("%s: %+v", k.title, opened)
		}
	}

	// And the user can archive a finished old child, and restore it.
	ended := tasks[0].conversation
	if got := d.archive(t, http.MethodPut, ended, http.StatusOK); got.ArchivedAt == nil {
		t.Fatalf("archived: %+v", got)
	}
	if got := d.archive(t, http.MethodDelete, ended, http.StatusOK); got.ArchivedAt != nil {
		t.Fatalf("restored: %+v", got)
	}
}

func TestUserSeesBusyChildActivityInTheSessionList(t *testing.T) {
	t.Parallel()
	// Given a child session that runs a gated shell.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := newParent(t, d)
	child := spawnChild(t, d, parent, "Busy", taskGate(t, dir, "busy"), "bash")
	waitTaskFile(t, filepath.Join(dir, "busy.pid"))

	// When the web lists the sessions, then the busy child has a parent and the engine's activity,
	// which the stalled hint reads.
	var listed webChat
	eventually(t, "activity of the busy child", func() (bool, any) {
		listed = d.listWebChats(t)[child.conversation(t)]
		return listed.Busy && listed.LastActivityAt != nil && listed.ToolStartedAt != nil, listed
	})
	if listed.ParentID == nil || *listed.ParentID != parent {
		t.Fatalf("busy child: %+v", listed)
	}
	// And the parent, which is idle, has no activity.
	if idle := d.listWebChats(t)[parent]; idle.Busy || idle.LastActivityAt != nil || idle.ToolStartedAt != nil {
		t.Fatalf("idle parent: %+v", idle)
	}

	// And an idle child has none after its run ends.
	releaseResultGate(t, dir, "busy")
	waitResults(t, conn, parent, 1)
	eventually(t, "idle child", func() (bool, any) {
		done := d.listWebChats(t)[child.conversation(t)]
		return !done.Busy && done.LastActivityAt == nil && done.ToolStartedAt == nil, fmt.Sprintf("%+v", done)
	})
}
