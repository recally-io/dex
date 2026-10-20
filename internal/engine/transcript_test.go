package engine

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
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
