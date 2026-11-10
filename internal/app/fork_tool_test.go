package app_test

import (
	"fmt"
	"net/http"
	"testing"
)

func TestUserAgentCanForkItsCurrentConversation(t *testing.T) {
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "echo")
	source := d.mainChat(t)
	input := d.send(t, source, "first")
	db := taskResultDB(t, dir)
	waitResultSubmission(t, db, input, "done")
	path := fmt.Sprintf("/api/agents/default/chats/%d", source)
	var history struct {
		Items []struct {
			Kind    string `json:"kind"`
			EntryID int64  `json:"entryId"`
		} `json:"items"`
	}
	d.must(t, http.MethodGet, path+"/history?before=9007199254740991", nil, &history)
	var entry int64
	for _, item := range history.Items {
		if item.Kind == "user" {
			entry = item.EntryID
			break
		}
	}
	if entry == 0 {
		t.Fatal("missing entry")
	}
	d.setModel(t, "tool")
	request := d.send(t, source, fmt.Sprintf(`fork_chat {"entryId":%d}`, entry))
	waitResultSubmission(t, db, request, "done")
	var target int64
	var status string
	if err := db.QueryRowContext(t.Context(), "SELECT target_conversation_id,status FROM fork_operations WHERE source_conversation_id=?", source).Scan(&target, &status); err != nil || status != "done" || target == 0 {
		t.Fatalf("fork receipt target=%d status=%s err=%v", target, status, err)
	}
	second := d.send(t, source, `fork_chat {}`)
	waitResultSubmission(t, db, second, "done")
	if err := db.QueryRowContext(t.Context(), "SELECT target_conversation_id,status FROM fork_operations WHERE source_conversation_id=? ORDER BY rowid DESC LIMIT 1", source).Scan(&target, &status); err != nil || status != "done" || target == 0 {
		t.Fatalf("default fork target=%d status=%s err=%v", target, status, err)
	}
	var forkHistory struct {
		Items []struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		} `json:"items"`
	}
	d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d/history?before=9007199254740991", target), nil, &forkHistory)
	found := false
	for _, item := range forkHistory.Items {
		if item.Kind == "user" && item.Text == `fork_chat {}` {
			found = true
		}
	}
	if !found {
		t.Fatalf("default fork did not include calling input: %+v", forkHistory)
	}
	t.Logf("tool created idle fork %d at the calling input", target)
}
