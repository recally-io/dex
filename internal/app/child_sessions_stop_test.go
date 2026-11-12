package app_test

import (
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
)

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

func TestUserParentStopKeepsQueuedUserInputAndTheRunOfTheParent(t *testing.T) {
	t.Parallel()
	// Given a child that runs a parent turn, with a user input and a second parent input queued.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := newParent(t, d)
	child := spawnChild(t, d, parent, "Queued user", taskGate(t, dir, "parent-active"), "bash")
	waitTaskFile(t, filepath.Join(dir, "parent-active.pid"))
	d.send(t, child.conversation(t), "echo keep queued user")
	var ref childRef
	toolJSON(t, callSessionTool(t, d, parent, "session_send", map[string]any{"id": child.ID, "message": "echo parent second"}), &ref)
	// When the user stops the parent chat.
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/abort", parent), nil, nil)
	d.send(t, child.conversation(t), "echo after stop")
	releaseResultGate(t, dir, "parent-active")
	// Then the queued parent input is stopped, and the other three turns run to the end.
	turns := waitChildTurnsEnded(t, conn, child.conversation(t), 4)
	want := []string{"done", "done", "unanswered", "done"}
	for i, turn := range turns {
		if turn.status != want[i] {
			t.Fatalf("turn %d is %s, want %s: %+v", i, turn.status, want[i], turns)
		}
	}
	if turns[2].reason != "stopped" {
		t.Fatalf("the queued parent input: %+v", turns[2])
	}
	// And the run that the parent started still sends its result, after Stop.
	waitResults(t, conn, parent, 1)
}
