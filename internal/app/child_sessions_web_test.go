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
	// The list sets it when Stop on the parent would act on the child.
	StoppedWithParent *bool `json:"stoppedWithParent"`
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

	// When the web reads the result mark, it can open the canonical child Session URL.
	d.watch(t, parent).until(t, "linked child result", func(tr *transcript) bool {
		for _, it := range tr.items {
			if it.Kind == "session_result" && it.ID == "result:"+turn {
				return it.ChildSessionID == child.conversation(t)
			}
		}
		return false
	})
	var opened webChat
	d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d", child.conversation(t)), nil, &opened)

	// Then it gets the child session, which is writable and names its parent.
	if opened.ID != child.conversation(t) || opened.ParentID == nil || *opened.ParentID != parent || opened.ReadOnly || opened.Channel != "web" {
		t.Fatalf("turn %s opened %+v; child=%+v parent=%d", turn, opened, child, parent)
	}
	// And the session list carries the same parent link.
	if listed := d.listWebChats(t)[opened.ID]; listed.ParentID == nil || *listed.ParentID != parent {
		t.Fatalf("listed child: %+v", listed)
	}

	// And another Bot cannot open the child at its own Session URL.
	var other apiAgent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "B", "provider": "faux", "modelId": "echo", "thinkingLevel": "off"}, &other, http.StatusCreated)
	d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/%s/chats/%d", other.ID, opened.ID), nil, nil, http.StatusNotFound)
}

func TestUserParentStopWithdrawsParentInputQueuedBehindAUserRun(t *testing.T) {
	t.Parallel()
	// Given a child that runs a turn of the user, with a parent input queued behind it.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := newParent(t, d)
	child := spawnChild(t, d, parent, "Mixed origins", "echo initial", "bash")
	waitResults(t, conn, parent, 1)
	d.send(t, child.conversation(t), taskGate(t, dir, "user-active"))
	waitTaskFile(t, filepath.Join(dir, "user-active.pid"))
	var ref childRef
	toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": "echo parent queued"}), &ref)
	// When the user stops the parent chat.
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/abort", parent), nil, nil)
	releaseResultGate(t, dir, "user-active")
	// Then the run of the user finishes, and the parent input never runs.
	turns := waitChildTurnsEnded(t, conn, child.conversation(t), 3)
	if turns[1].status != "done" {
		t.Fatalf("parent Stop aborted the run of the user because a parent input was queued: %+v", turns)
	}
	if turns[2].status != "unanswered" || turns[2].reason != "stopped" {
		t.Fatalf("the queued parent input was not stopped: %+v", turns[2])
	}
	if rows := resultRows(t, conn, parent); len(rows) != 1 {
		t.Fatalf("results: %+v", rows)
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

func TestUserSeesStopOnlyForWithdrawableMixedChildInput(t *testing.T) {
	t.Parallel()
	// Given a parent-started run and a queued user input in the child.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := newParent(t, d)
	child := spawnChild(t, d, parent, "Mixed", taskGate(t, dir, "mixed-web"), "bash")
	waitTaskFile(t, filepath.Join(dir, "mixed-web.pid"))
	d.send(t, child.conversation(t), "echo user queued")

	// When no parent input is queued, Stop cannot act on the child.
	if got := d.listWebChats(t)[child.conversation(t)]; got.StoppedWithParent != nil {
		t.Fatalf("placed parent input must not enable Stop: %+v", got)
	}
	var ref childRef
	toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": "echo parent queued"}), &ref)
	if got := d.listWebChats(t)[child.conversation(t)]; got.StoppedWithParent == nil || !*got.StoppedWithParent {
		t.Fatalf("queued parent input must enable Stop: %+v", got)
	}

	// When the parent stops, only the queued parent input ends.
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/abort", parent), nil, nil)
	turns := childTurns(t, conn, child.conversation(t))
	if len(turns) != 3 || turns[0].status != "pending" || turns[1].status != "pending" || turns[2].status != "unanswered" || turns[2].reason != "stopped" {
		t.Fatalf("mixed child after Stop: %+v", turns)
	}
	if got := d.listWebChats(t)[child.conversation(t)]; got.StoppedWithParent != nil {
		t.Fatalf("withdrawn parent input must not enable Stop: %+v", got)
	}
	releaseResultGate(t, dir, "mixed-web")
	waitChildTurnsEnded(t, conn, child.conversation(t), 3)
}

func TestUserSeesWhichChildrenStopOnTheParentReaches(t *testing.T) {
	t.Parallel()
	// Given a child that runs a parent turn, and a child that runs only a turn that the user started.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := newParent(t, d)
	reached := spawnChild(t, d, parent, "Parent turn", taskGate(t, dir, "reached"), "bash")
	waitTaskFile(t, filepath.Join(dir, "reached.pid"))
	alone := spawnChild(t, d, parent, "User turn", "echo first", "bash")
	waitDoneChildTurns(t, conn, alone.conversation(t), 1)
	d.send(t, alone.conversation(t), taskGate(t, dir, "alone"))
	waitTaskFile(t, filepath.Join(dir, "alone.pid"))

	// When the web reads the session list, then only the first child is marked.
	listed := d.listWebChats(t)
	if got := listed[reached.conversation(t)]; !got.Busy || got.StoppedWithParent == nil || !*got.StoppedWithParent {
		t.Fatalf("child of a parent turn: %+v", got)
	}
	if got := listed[alone.conversation(t)]; !got.Busy || got.StoppedWithParent != nil {
		t.Fatalf("child of a user turn: %+v", got)
	}

	// And the mark is the effect of Stop: it ends the first child and leaves the second.
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/abort", parent), nil, nil)
	waitChildTurnsEnded(t, conn, reached.conversation(t), 1)
	if turns := childTurns(t, conn, alone.conversation(t)); turns[1].status != "pending" {
		t.Fatalf("the stop of the parent ended the turn of the user: %+v", turns[1])
	}
	if got := d.listWebChats(t)[reached.conversation(t)]; got.Busy || got.StoppedWithParent != nil {
		t.Fatalf("stopped child: %+v", got)
	}
	releaseResultGate(t, dir, "alone")
	waitDoneChildTurns(t, conn, alone.conversation(t), 2)
}
