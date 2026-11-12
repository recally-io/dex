//go:build agenteval

package main

import (
	"fmt"
	"testing"

	"github.com/recally-io/dex/internal/engine"
)

func TestUserEvalForkReplayUsesRealHTTP(t *testing.T) {
	// Given a finished exchange on an isolated faux-provider chat.
	c, parent, _ := routineEvalClient(t, "echo")
	path := fmt.Sprintf("/api/agents/default/chats/%d", parent.ID)
	if err := c.send(path, "Remember the code word KIWI", "followUp"); err != nil {
		t.Fatal(err)
	}
	// When the scenario forks, retries, and asks the fork a question.
	ids, checks, err := c.sessionOperation(path, parent.ID, "fork")
	if err != nil {
		t.Fatal(err)
	}
	// Then replay and source isolation pass; echo cannot answer the question.
	if len(ids) != 2 {
		t.Fatalf("chat IDs: %v", ids)
	}
	requireCheck(t, checks, "fork replay returns same chat", true)
	requireCheck(t, checks, "fork replay with another entry conflicts", true)
	requireCheck(t, checks, "source history count unchanged", true)
	requireCheck(t, checks, "fork answer contains KIWI", false)
}

func TestUserEvalCompactReplayUsesRealHTTP(t *testing.T) {
	// Given a completed exchange on an isolated faux-provider chat.
	c, parent, _ := routineEvalClient(t, "echo")
	path := fmt.Sprintf("/api/agents/default/chats/%d", parent.ID)
	if err := c.send(path, "My favorite color is TEAL", "followUp"); err != nil {
		t.Fatal(err)
	}
	// When the scenario compacts and replays the request.
	_, checks, err := c.sessionOperation(path, parent.ID, "compact")
	if err != nil {
		t.Fatal(err)
	}
	// Then a single terminal outcome is retained and echo does not invent recall.
	requireCheck(t, checks, "compaction finished", true)
	requireCheck(t, checks, "compact replay adds no entry", true)
	requireCheck(t, checks, "answer contains TEAL", false)
}

func TestUserEvalDistinguishesToolTextFromFinalAnswers(t *testing.T) {
	// Given text from an assistant that also calls a tool and two final answers.
	items := []engine.Item{
		{Kind: "user", Text: "first", EntryID: 1},
		{Kind: "assistant", ID: "a", Text: "working", EntryID: 2},
		{Kind: "tool", EntryID: 2},
		{Kind: "assistant", ID: "b", Text: "first answer", EntryID: 3},
		{Kind: "user", Text: "follow", EntryID: 4},
		{Kind: "assistant", ID: "c", Text: "SECOND", EntryID: 5},
	}
	// When the eval reads the timeline.
	final := answers(items)
	// Then only the final answers count and their order relative to input remains visible.
	if len(final) != 2 || final[0].ID != "b" || final[1].ID != "c" || inputIndex(items, "follow") <= answerIndex(items, final[0]) {
		t.Fatalf("answers: %+v", final)
	}
}

func TestUserEvalRejectsFailedAndDuplicateCompactions(t *testing.T) {
	// Given the terminal outcomes exposed by the timeline.
	for _, tc := range []struct {
		name    string
		items   []engine.Item
		count   int
		success bool
	}{
		{"summary", []engine.Item{{Kind: "boundary", Boundary: "compaction", Text: "summary"}}, 1, true},
		{"nothing", []engine.Item{{Kind: "boundary", Boundary: "compaction", Status: "nothing"}}, 1, true},
		{"failed", []engine.Item{{Kind: "boundary", Boundary: "compaction", Status: "failed"}}, 1, false},
		{"pending", []engine.Item{{Kind: "boundary", Boundary: "compaction", Status: "pending"}}, 1, false},
		{"duplicate", []engine.Item{{Kind: "boundary", Boundary: "compaction", Text: "one"}, {Kind: "boundary", Boundary: "compaction", Text: "two"}}, 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// When the eval counts compactions, it keeps failure and duplicates visible.
			n, ok := compactions(tc.items)
			if n != tc.count || ok != tc.success {
				t.Fatalf("got %d %t", n, ok)
			}
		})
	}
}
