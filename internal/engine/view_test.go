package engine

import (
	"encoding/json/v2"
	"reflect"
	"slices"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/chord/delta"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

func transcriptAssistant(text string) ai.AssistantMessage {
	return ai.AssistantMessage{Content: []ai.AssistantContentBlock{ai.TextContent{Text: text}}, Provider: "faux", Model: "echo", Timestamp: 2, Usage: ai.Usage{Input: 10, Output: 5}, StopReason: "stop"}
}

func transcriptEntry(id int64, kind string, msg ai.Message, data any) durable.EntryRecord {
	e := durable.EntryRecord{Id: durable.EntryId(id), Kind: kind, Data: data}
	if msg != nil {
		e.Model = []ai.Message{msg}
	}
	return e
}

func TestUserTranscriptStreamsTextAndCommitsStableItems(t *testing.T) {
	// Given a partial answer, the projection exposes append events for text growth.
	partial := transcriptAssistant("Hel")
	p := projection{snapshot: harness.SnapshotEvent{Run: &harness.RunEvent{}, Generation: &harness.SnapshotGeneration{Message: &partial}}}
	before := p.event()
	partial = transcriptAssistant("Hello")
	p.snapshot.Generation = &harness.SnapshotGeneration{Message: &partial}
	next := p.event()
	changes, ok := diff(before.Items, next.Items)
	want := []Event{{Type: "append", ID: "partial:2:0", Text: "lo"}}
	if !ok || !reflect.DeepEqual(changes, want) {
		t.Fatalf("stream = %#v, %v", changes, ok)
	}
	// When the message is committed, the temporary ID is replaced by the entry ID.
	p.snapshot = harness.SnapshotEvent{Entries: []durable.EntryRecord{transcriptEntry(20, "pi.assistant", partial, nil)}}
	committed := p.event()
	if _, ok := diff(next.Items, committed.Items); ok {
		t.Fatal("commit must replace temporary IDs with a snapshot")
	}
	if got := committed.Items[0]; got.ID != "e20:0" || got.EntryID != 20 || got.Model != "faux/echo" || got.Usage.Input != 10 {
		t.Fatalf("committed = %#v", got)
	}
	// A replacement block does not append to the previous text.
	replacement := next.Items[0]
	replacement.Text = "replaced"
	changes, ok = diff(next.Items, []Item{replacement})
	if !ok || len(changes) != 1 || changes[0].Type != "item" {
		t.Fatalf("replacement = %#v", changes)
	}
}

func TestUserTranscriptToolEndBeforeResult(t *testing.T) {
	// Given a committed tool call in a live run.
	call := transcriptAssistant("")
	call.Content = []ai.AssistantContentBlock{ai.ToolCall{ID: "call", Name: "bash", Arguments: map[string]any{"command": "ls"}}}
	p := projection{snapshot: harness.SnapshotEvent{Entries: []durable.EntryRecord{transcriptEntry(2, "pi.assistant", call, nil)}, Run: &harness.RunEvent{}}}
	if got := p.event().Items[0].Status; got != "running" {
		t.Fatal(got)
	}
	// When the slot ends without an entry, the item displays the retained output as failed.
	p.snapshot.Tools = []harness.ToolSlot{{CallId: "call", Status: harness.ToolSlotDone, Output: new("a.txt\n")}}
	ended := p.event()
	if got := ended.Items[0]; got.Status != "error" || got.Output != "a.txt\n" {
		t.Fatalf("ended = %#v", got)
	}
	// When the result arrives later, the same call ID gains the durable result.
	result := ai.ToolResultMessage{ToolCallID: "call", Content: []ai.ToolResultMessageContent{ai.TextContent{Text: "a.txt\n"}}, DurationMs: new(int64(40))}
	p.snapshot.Entries = append(p.snapshot.Entries, transcriptEntry(3, "pi.toolResult", result, nil))
	settled := p.event()
	changes, ok := diff(ended.Items, settled.Items)
	if !ok || len(changes) != 1 || changes[0].Item.ID != "call" || changes[0].Item.Status != "done" || *changes[0].Item.DurationMs != 40 {
		t.Fatalf("result = %#v", changes)
	}
	p.snapshot.Run = nil
	p.snapshot.Tools = nil
	if got := p.event().Items[0].Status; got != "done" {
		t.Fatal(got)
	}
}

func TestUserTranscriptResetHistoryAndTaskMarkers(t *testing.T) {
	// Given a reset, a durable task marker, and its answer in unordered snapshot entries.
	reset := transcriptEntry(21, "pi.reset", nil, delta.JsonObjectOf("at", float64(7)))
	reset.Head = new(durable.EntryId(21))
	task := transcriptEntry(22, "dex.task_result", ai.UserMessage{Content: ai.UserText("private report"), Timestamp: 8}, delta.JsonObjectOf("requestId", "result-1", "taskId", "task-1", "taskTitle", "Inspect"))
	answer := transcriptEntry(23, "pi.assistant", transcriptAssistant("checked"), nil)
	p := projection{snapshot: harness.SnapshotEvent{Entries: []durable.EntryRecord{task, answer, reset}}}
	snapshot := p.event()
	if snapshot.HistoryBefore != 21 || len(snapshot.Items) != 3 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if got := snapshot.Items[0]; got.Kind != "boundary" || got.Boundary != "reset" || got.At != 7 {
		t.Fatalf("reset = %#v", got)
	}
	if got := snapshot.Items[1]; got.Kind != "task" || got.TaskID != "task-1" || got.Title != "Inspect" || got.Text != "" {
		t.Fatalf("task = %#v", got)
	}
	// An older page and an active snapshot assign the same committed ID.
	history := (projection{snapshot: harness.SnapshotEvent{Entries: []durable.EntryRecord{answer}}}).event()
	if history.Items[0].ID != snapshot.Items[2].ID {
		t.Fatal("entry ID changed across history boundary")
	}
	if !historyBoundary(task) || !historyBoundary(reset) || historyBoundary(transcriptEntry(24, "pi.toolResult", ai.ToolResultMessage{}, nil)) {
		t.Fatal("history split a call/result group")
	}
}

func TestUserTranscriptQueuedAndWithdrawnInputs(t *testing.T) {
	// Given the native inbox carries display metadata and attachments.
	data := delta.JsonObjectOf("requestId", "r1", "content", "look", "origin", "web", "attachments", []any{delta.JsonObjectOf("id", "u1", "name", "a.png", "size", float64(3), "mimeType", "image/png", "path", "uploads/u1/a.png")})
	queued := harness.InboxItem{Id: 1, Mode: harness.InboxFollowUp, Entry: delta.JsonObjectOf("kind", "dex.user", "data", data)}
	p := projection{inbox: []harness.InboxItem{queued}}
	got := p.event().Items
	if len(got) != 1 || !got[0].Queued || got[0].Text != "look" || got[0].Origin != "web" || len(got[0].Attachments) != 1 {
		t.Fatalf("queued = %#v", got)
	}
	// Once placed, the durable input replaces the queued item without a duplicate.
	entry := transcriptEntry(8, "dex.user", ai.UserMessage{Content: ai.UserText("look\n\nAttached files:\n- uploads/u1/a.png"), Timestamp: 9}, data)
	p.snapshot.Entries = []durable.EntryRecord{entry}
	got = p.event().Items
	if len(got) != 1 || got[0].Queued || got[0].Text != "look" {
		t.Fatalf("placed = %#v", got)
	}
	// After withdrawal, a fresh projection needs no control receipt to show the input and error.
	p = projection{snapshot: harness.SnapshotEvent{Entries: []durable.EntryRecord{transcriptEntry(10, "dex.withdrawn", nil, data)}}}
	got = p.event().Items
	if len(got) != 2 || got[0].ID != "r1" || got[0].Text != "look" || !got[1].Aborted || got[1].ID != "r1:error" {
		t.Fatalf("withdrawn = %#v", got)
	}
}

func TestUserTranscriptJSONRequiredEmptyFields(t *testing.T) {
	for _, event := range []Event{{Type: "snapshot", Items: []Item{}}, {Type: "run", Running: false}, {Type: "item", Item: &Item{Kind: "tool", ID: "call", Name: "bash", Status: "running"}}, {Type: "item", Item: &Item{Kind: "assistant", ID: "partial"}}} {
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		switch event.Type {
		case "snapshot":
			if items, ok := got["items"].([]any); !ok || len(items) != 0 {
				t.Fatalf("snapshot items: %s", raw)
			}
			fallthrough
		case "run":
			if value, ok := got["running"]; !ok || value != false {
				t.Fatalf("running: %s", raw)
			}
		case "item":
			item := got["item"].(map[string]any)
			if event.Item.Kind == "tool" {
				if item["output"] != "" || item["args"] == nil {
					t.Fatalf("tool: %s", raw)
				}
			} else if item["text"] != "" {
				t.Fatalf("text: %s", raw)
			}
		}
	}
}

func TestUserTranscriptModelErrorsRetriesAndToolTaskLinks(t *testing.T) {
	failed := transcriptAssistant("")
	failed.Content = nil
	failed.StopReason = "error"
	failed.ErrorMessage = "HTTP 401"
	stopped := transcriptAssistant("par")
	stopped.StopReason = "aborted"
	call := transcriptAssistant("")
	call.Content = []ai.AssistantContentBlock{ai.ToolCall{ID: "spawn", Name: "spawn_task"}}
	result := ai.ToolResultMessage{ToolCallID: "spawn", Content: []ai.ToolResultMessageContent{ai.TextContent{Text: `{"id":"task-2"}`}}}
	p := projection{snapshot: harness.SnapshotEvent{Entries: []durable.EntryRecord{transcriptEntry(1, "pi.assistant", failed, nil), transcriptEntry(2, "pi.assistant", stopped, nil), transcriptEntry(3, "pi.assistant", call, nil), transcriptEntry(4, "pi.toolResult", result, nil)}, Generation: &harness.SnapshotGeneration{Attempt: 2, Retry: &harness.RetryStatus{At: 99, Error: "overloaded"}}}}
	e := p.event()
	if e.Retry == nil || e.Retry.Attempt != 2 || e.Retry.At != 99 {
		t.Fatalf("retry = %#v", e.Retry)
	}
	if !slices.ContainsFunc(e.Items, func(i Item) bool { return i.Reason == "HTTP 401" }) || !slices.ContainsFunc(e.Items, func(i Item) bool { return i.Aborted }) || !slices.ContainsFunc(e.Items, func(i Item) bool { return i.ID == "spawn" && i.TaskID == "task-2" }) {
		t.Fatalf("items = %#v", e.Items)
	}
}
