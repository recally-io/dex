package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

func TestUserNativeTranscriptSubscriptionsAndHistory(t *testing.T) {
	// Given two independent subscribers to one real SQLite execution.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	dir := t.TempDir()
	agents, err := New(Config{DataDir: filepath.Join(dir, "data"), Home: filepath.Join(dir, "home"), FakeModel: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := agents.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	options := Options{Model: ModelRef{Provider: "faux", ModelID: "echo"}, ThinkingLevel: "off"}
	ref, err := agents.Create(ctx, "test", options)
	if err != nil {
		t.Fatal(err)
	}
	first, err := agents.Watch(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := agents.Watch(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if second.Snapshot.Type != "snapshot" {
		t.Fatal(second.Snapshot.Type)
	}
	second.Close()
	// When one subscriber closes, the other still receives the durable user input and answer.
	sub, err := agents.Submit(ctx, ref, Input{RequestID: "r1", Content: "hello", Origin: "web", Source: "test", Options: options})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sub.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	found := false
	observed := []Event{first.Snapshot}
	for !found {
		select {
		case <-ctx.Done():
			t.Fatalf("answer not received: %v", ctx.Err())
		case event, ok := <-first.Events:
			if !ok {
				t.Fatal("closing the other subscriber stopped this stream")
			}
			observed = append(observed, event)
			items := event.Items
			if event.Item != nil {
				items = []Item{*event.Item}
			}
			for _, item := range items {
				if item.Kind == "assistant" && item.Text == "echo: hello" && item.EntryID > 0 {
					found = true
				}
			}
		}
	}
	// A reconnect and history use the same durable item projection.
	fresh, err := agents.Watch(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	history, err := agents.History(ctx, ref, math.MaxInt64, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(history.Items, fresh.Snapshot.Items) {
		t.Fatalf("history = %#v; snapshot = %#v", history.Items, fresh.Snapshot.Items)
	}
	if len(history.Items) < 2 || history.Items[0].Origin != "web" || history.Items[0].ID != "r1" {
		t.Fatalf("history metadata = %#v", history.Items)
	}
	// Cancellation closes the native subscription without requiring another commit.
	canceledCtx, cancelWatch := context.WithCancel(ctx)
	canceled, err := agents.Watch(canceledCtx, ref)
	if err != nil {
		t.Fatal(err)
	}
	cancelWatch()
	select {
	case <-canceled.Events:
	case <-ctx.Done():
		t.Fatal("canceled subscription did not close")
	}
	canceled.Close()
	// Cancellation racing acquisition must return an error or a closed watch, never panic.
	for range 16 {
		raceCtx, cancelRace := context.WithCancel(ctx)
		go cancelRace()
		watch, err := agents.Watch(raceCtx, ref)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			continue
		}
		watch.Close()
	}
	t.Logf("received %d native transcript events", len(observed))
}

// transcriptAgents starts an engine on a temporary data directory and returns the conversation of
// a new agent, to commit entries directly.
func transcriptAgents(t *testing.T) (*Agents, Ref, harness.Conversation) {
	t.Helper()
	dir := t.TempDir()
	agents, err := New(Config{DataDir: filepath.Join(dir, "data"), Home: filepath.Join(dir, "home"), FakeModel: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agents.Close(context.Background()) })
	ref, err := agents.Create(t.Context(), "test", Options{Model: ModelRef{Provider: "faux", ModelID: "echo"}})
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := agents.conversation(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	return agents, ref, conversation
}

func appendEntry(tx durable.Tx, conversation harness.Conversation, kind string, msg ai.Message) (durable.EntryId, error) {
	entry, err := tx.AppendEntry(conversation.Id(), durable.EntryDraft{Kind: kind, Model: []ai.Message{msg}})
	return entry.Id, err
}

func TestUserWatchShowsRecentTurnsAndPagesTheRest(t *testing.T) {
	// Given 25 turns, and a last turn with 60 tool calls that fold into one row.
	ctx := t.Context()
	agents, ref, conversation := transcriptAgents(t)
	user := ai.UserMessage{Content: ai.UserText("hello")}
	starts := []durable.EntryId{}
	_, err := conversation.Commit(ctx, func(tx durable.Tx) (any, error) {
		for turn := range 25 {
			id, err := appendEntry(tx, conversation, "pi.user", user)
			if err != nil {
				return nil, err
			}
			starts = append(starts, id)
			for call := range 60 * (turn / 24) {
				callID := fmt.Sprintf("call-%d", call)
				msg := transcriptAssistant("")
				msg.Content = []ai.AssistantContentBlock{ai.ToolCall{ID: callID, Name: "bash", Arguments: map[string]any{"command": "pwd"}}}
				if _, err := appendEntry(tx, conversation, "pi.assistant", msg); err != nil {
					return nil, err
				}
				result := ai.ToolResultMessage{ToolCallID: callID, Content: []ai.ToolResultMessageContent{ai.TextContent{Text: "/workspace"}}}
				if _, err := appendEntry(tx, conversation, "pi.toolResult", result); err != nil {
					return nil, err
				}
			}
			if _, err := appendEntry(tx, conversation, "pi.assistant", transcriptAssistant("answer")); err != nil {
				return nil, err
			}
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// When the user opens the chat.
	watch, err := agents.Watch(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Close()
	// Then the snapshot holds the last 20 turns, with every tool call of the long turn.
	items := watch.Snapshot.Items
	if len(items) != 19*2+62 || items[0].EntryID != int64(starts[5]) {
		t.Fatalf("window has %d items, first = %#v", len(items), items[0])
	}
	if watch.Snapshot.HistoryBefore != int64(starts[5]) {
		t.Fatalf("historyBefore = %d, want %d", watch.Snapshot.HistoryBefore, starts[5])
	}
	// And the first 5 turns stay available as history.
	history, err := agents.History(ctx, ref, watch.Snapshot.HistoryBefore, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Items) != 5*2 || history.NextBefore != 0 {
		t.Fatalf("history has %d items, nextBefore %d", len(history.Items), history.NextBefore)
	}
	// When a later compaction keeps a short context, a new reader gets all of it.
	_, err = conversation.Commit(ctx, func(tx durable.Tx) (any, error) {
		_, err := tx.AppendEntry(conversation.Id(), durable.EntryDraft{Kind: "pi.compaction", Head: &starts[23]})
		return nil, err
	})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := agents.Watch(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if n := len(fresh.Snapshot.Items); n != 2+62+1 || fresh.Snapshot.Items[n-1].Boundary != "compaction" {
		t.Fatalf("compacted timeline has %d items: %#v", n, fresh.Snapshot.Items[n-1])
	}
}

func TestUserWatchCapsALongTurn(t *testing.T) {
	// Given one input that ran 200 tool calls, as a child session can.
	ctx := t.Context()
	agents, ref, conversation := transcriptAgents(t)
	_, err := conversation.Commit(ctx, func(tx durable.Tx) (any, error) {
		if _, err := appendEntry(tx, conversation, "pi.user", ai.UserMessage{Content: ai.UserText("work")}); err != nil {
			return nil, err
		}
		for i := range 200 {
			callID := fmt.Sprintf("call-%d", i)
			msg := transcriptAssistant("")
			msg.Content = []ai.AssistantContentBlock{ai.ToolCall{ID: callID, Name: "bash", Arguments: map[string]any{"command": "pwd"}}}
			if _, err := appendEntry(tx, conversation, "pi.assistant", msg); err != nil {
				return nil, err
			}
			result := ai.ToolResultMessage{ToolCallID: callID, Content: []ai.ToolResultMessageContent{ai.TextContent{Text: "/workspace"}}}
			if _, err := appendEntry(tx, conversation, "pi.toolResult", result); err != nil {
				return nil, err
			}
		}
		_, err := appendEntry(tx, conversation, "pi.assistant", transcriptAssistant("done"))
		return nil, err
	})
	if err != nil {
		t.Fatal(err)
	}
	// When the user opens the chat.
	watch, err := agents.Watch(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Close()
	// Then the snapshot holds at most 300 entries and starts at a call, not at its result.
	items := watch.Snapshot.Items
	if len(items) != 151 || items[0].ID != "call-50" || items[0].Status != "done" {
		t.Fatalf("window has %d items, first = %#v", len(items), items[0])
	}
	if watch.Snapshot.HistoryBefore != items[0].EntryID {
		t.Fatalf("historyBefore = %d, want %d", watch.Snapshot.HistoryBefore, items[0].EntryID)
	}
	// And the start of the turn loads as history.
	history, err := agents.History(ctx, ref, watch.Snapshot.HistoryBefore, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Items) != 51 || history.Items[0].Kind != "user" || history.Items[50].ID != "call-49" {
		t.Fatalf("history has %d items", len(history.Items))
	}
}

func TestUserOpensLongToolOutputAndThought(t *testing.T) {
	// Given a turn with a long thought and long outputs of a done call, a failed call and a session call.
	ctx := t.Context()
	agents, ref, conversation := transcriptAgents(t)
	long := strings.Repeat("x", detailLimit+1)
	thought := "Plan the check\n" + long
	var callEntry durable.EntryId
	_, err := conversation.Commit(ctx, func(tx durable.Tx) (any, error) {
		if _, err := appendEntry(tx, conversation, "pi.user", ai.UserMessage{Content: ai.UserText("check")}); err != nil {
			return nil, err
		}
		msg := transcriptAssistant("")
		msg.Content = []ai.AssistantContentBlock{
			ai.ThinkingContent{Thinking: thought},
			ai.ToolCall{ID: "done", Name: "bash", Arguments: map[string]any{"command": "cat big"}},
			ai.ToolCall{ID: "failed", Name: "bash", Arguments: map[string]any{"command": "false"}},
			ai.ToolCall{ID: "session", Name: "session_new", Arguments: map[string]any{"title": "child"}},
			ai.ToolCall{ID: "orphan", Name: "bash", Arguments: map[string]any{"command": "sleep 9"}},
		}
		// Parallel calls put the last result past the first page of the forward scan.
		for i := range 20 {
			msg.Content = append(msg.Content, ai.ToolCall{ID: fmt.Sprintf("p%d", i), Name: "bash", Arguments: map[string]any{"command": "pwd"}})
		}
		id, err := appendEntry(tx, conversation, "pi.assistant", msg)
		if err != nil {
			return nil, err
		}
		callEntry = id
		calls := []string{"done", "failed", "session"}
		for i := range 20 {
			calls = append(calls, fmt.Sprintf("p%d", i))
		}
		for _, call := range calls {
			result := ai.ToolResultMessage{ToolCallID: call, IsError: call == "failed", Content: []ai.ToolResultMessageContent{ai.TextContent{Text: call + long}}}
			if _, err := appendEntry(tx, conversation, "pi.toolResult", result); err != nil {
				return nil, err
			}
		}
		_, err = appendEntry(tx, conversation, "pi.assistant", transcriptAssistant("checked"))
		return nil, err
	})
	if err != nil {
		t.Fatal(err)
	}
	// When the user opens the chat and loads its history.
	watch, err := agents.Watch(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Close()
	history, err := agents.History(ctx, ref, math.MaxInt64, 100)
	if err != nil {
		t.Fatal(err)
	}
	// Then both leave out the long done output and keep the first line of the thought.
	for name, items := range map[string][]Item{"snapshot": watch.Snapshot.Items, "history": history.Items} {
		byID := map[string]Item{}
		for _, item := range items {
			byID[item.ID] = item
		}
		thinkingID := fmt.Sprintf("e%d:0", callEntry)
		if got := byID[thinkingID]; got.Text != "Plan the check" || !got.Truncated {
			t.Fatalf("%s thought = %#v", name, got)
		}
		if got := byID["done"]; got.Output != "" || !got.Truncated || got.Status != "done" {
			t.Fatalf("%s done call = %#v", name, got)
		}
		// A failed call opens by itself, and a session card reads its result.
		for _, id := range []string{"failed", "session"} {
			if got := byID[id]; got.Output != id+long || got.Truncated {
				t.Fatalf("%s %s call keeps %d bytes, truncated %v", name, id, len(got.Output), got.Truncated)
			}
		}
	}
	// When the user opens the done call and the thought, they get the full text.
	done, err := agents.Detail(ctx, ref, int64(callEntry), "done")
	if err != nil {
		t.Fatal(err)
	}
	if done.Output != "done"+long || done.Truncated || done.Status != "done" {
		t.Fatalf("done detail = %d bytes, %#v", len(done.Output), done.Status)
	}
	full, err := agents.Detail(ctx, ref, int64(callEntry), fmt.Sprintf("e%d:0", callEntry))
	if err != nil {
		t.Fatal(err)
	}
	if full.Text != thought || full.Truncated {
		t.Fatalf("thought detail = %d bytes", len(full.Text))
	}
	last, err := agents.Detail(ctx, ref, int64(callEntry), "p19")
	if err != nil {
		t.Fatal(err)
	}
	if last.Output != "p19"+long || last.Status != "done" {
		t.Fatalf("last parallel detail = %d bytes, %q", len(last.Output), last.Status)
	}
	// A call without a result stops the scan at the next answer.
	orphan, err := agents.Detail(ctx, ref, int64(callEntry), "orphan")
	if err != nil {
		t.Fatal(err)
	}
	if orphan.Output != "" || orphan.Status != "error" {
		t.Fatalf("orphan detail = %#v", orphan)
	}
	if _, err := agents.Detail(ctx, ref, int64(callEntry), "missing"); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("missing item: %v", err)
	}
}

func TestUserNativeHistoryKeepsToolCallAndResultTogether(t *testing.T) {
	// Given durable assistant calls and results in a real SQLite conversation.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	dir := t.TempDir()
	agents, err := New(Config{DataDir: filepath.Join(dir, "data"), Home: filepath.Join(dir, "home"), FakeModel: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := agents.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	ref, err := agents.Create(ctx, "test", Options{Model: ModelRef{Provider: "faux", ModelID: "echo"}})
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := agents.conversation(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	var resultID int64
	_, err = conversation.Commit(ctx, func(tx durable.Tx) (any, error) {
		call := transcriptAssistant("")
		call.Content = []ai.AssistantContentBlock{ai.ToolCall{ID: "call", Name: "bash", Arguments: map[string]any{"command": "pwd"}}}
		if _, err := tx.AppendEntry(conversation.Id(), durable.EntryDraft{Kind: "pi.assistant", Model: []ai.Message{call}}); err != nil {
			return nil, err
		}
		result, err := tx.AppendEntry(conversation.Id(), durable.EntryDraft{Kind: "pi.toolResult", Model: []ai.Message{ai.ToolResultMessage{ToolCallID: "call", Content: []ai.ToolResultMessageContent{ai.TextContent{Text: "/workspace"}}}}})
		resultID = int64(result.Id)
		return nil, err
	})
	if err != nil {
		t.Fatal(err)
	}
	// A limit of one result extends through the matching assistant entry.
	history, err := agents.History(ctx, ref, resultID+1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Items) != 1 || history.Items[0].ID != "call" || history.Items[0].Status != "done" || history.Items[0].Output != "/workspace" {
		t.Fatalf("split call/result = %#v", history.Items)
	}
	// The cursor is exclusive and does not include that result in the older page.
	older, err := agents.History(ctx, ref, resultID, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range older.Items {
		if item.Kind == "tool" && strings.Contains(item.Output, "/workspace") {
			t.Fatal("exclusive history cursor included newer result")
		}
	}
}
