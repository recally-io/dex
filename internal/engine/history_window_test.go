package engine

import (
	"context"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
)

func TestUserKeepsToolResultsAcrossNativeHistoryWindow(t *testing.T) {
	// Given a call and its result separated by more entries than the native window holds.
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	a := coreEngine(t, Config{})
	ref := coreCreate(t, a, "test", coreOptions("echo"))
	conversation, err := a.conversation(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conversation.Commit(ctx, func(tx durable.Tx) (any, error) {
		call := transcriptAssistant("")
		call.Content = []ai.AssistantContentBlock{ai.ToolCall{ID: "window-tool", Name: "bash", Arguments: map[string]any{"command": "pwd"}}}
		if _, err := tx.AppendEntry(conversation.Id(), durable.EntryDraft{Kind: "pi.assistant", Model: []ai.Message{call}}); err != nil {
			return nil, err
		}
		for range 120 {
			if _, err := tx.AppendEntry(conversation.Id(), durable.EntryDraft{Kind: "test.progress"}); err != nil {
				return nil, err
			}
		}
		_, err := tx.AppendEntry(conversation.Id(), durable.EntryDraft{Kind: "pi.tool-result", Model: []ai.Message{ai.ToolResultMessage{
			ToolCallID: "window-tool", Content: []ai.ToolResultMessageContent{ai.TextContent{Text: "/workspace"}},
		}}})
		return nil, err
	})
	if err != nil {
		t.Fatal(err)
	}

	// When the user subscribes, the complete tool item includes its saved result.
	watch, err := a.Watch(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Close()
	if len(watch.Snapshot.Items) != 1 || watch.Snapshot.Items[0].ID != "window-tool" || watch.Snapshot.Items[0].Status != "done" || watch.Snapshot.Items[0].Output != "/workspace" {
		t.Fatalf("tool result was split by the window: %+v", watch.Snapshot)
	}
	for _, id := range []string{"reset-1", "reset-2", "reset-3"} {
		if err := a.Reset(ctx, ref, id, ""); err != nil {
			t.Fatal(err)
		}
	}

	// Then the live stream and a new subscription reach the same reset without old tool items.
	fresh, err := a.Watch(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if len(fresh.Snapshot.Items) != 1 || fresh.Snapshot.Items[0].Boundary != "reset" {
		t.Fatalf("old context survived reset: %+v", fresh.Snapshot)
	}
	lastReset := fresh.Snapshot.Items[0].ID
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("live stream did not reach %s: %v", lastReset, ctx.Err())
		case event, ok := <-watch.Events:
			if !ok {
				t.Fatal("live stream stopped during reset")
			}
			if event.Type == "snapshot" && len(event.Items) == 1 && event.Items[0].ID == lastReset {
				return
			}
		}
	}
}
