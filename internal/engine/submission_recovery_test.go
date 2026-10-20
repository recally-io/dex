package engine

import (
	"encoding/json/v2"
	"errors"
	"testing"

	"github.com/MichaelKinsy/PiG/durable"
)

func TestUserSubmissionReplayNeedsNoCurrentModel(t *testing.T) {
	// Given a completed native input with durable source metadata.
	a := coreEngine(t, Config{})
	ref := coreCreate(t, a, "alpha", coreOptions("echo"))
	input := Input{RequestID: "stable", Content: "original", Origin: "telegram:7", Source: "dex:alpha/chat/12", Options: coreOptions("echo")}
	first, err := a.Submit(t.Context(), ref, input)
	if err != nil {
		t.Fatal(err)
	}
	want := coreWait(t, first)
	if want.Status != "done" || want.Answer != "echo: original" {
		t.Fatalf("original outcome: %+v", want)
	}
	// When the caller replays with no model or a different model and content.
	for _, model := range []string{"", "shout"} {
		replay := input
		replay.Content, replay.Source, replay.Options = "replacement", "replacement", coreOptions(model)
		sub, err := a.Submit(t.Context(), ref, replay)
		if err != nil {
			t.Fatal(err)
		}
		if got := coreWait(t, sub); sub.ID != first.ID || got != want {
			t.Fatalf("replayed id=%d outcome=%+v; want id=%d outcome=%+v", sub.ID, got, first.ID, want)
		}
	}
	// Then replay retains the input metadata and original execution model.
	conversation, err := a.conversation(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := conversation.Entries(t.Context(), durable.EntryQuery{}, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	users := 0
	for _, entry := range entries.Items {
		if entry.Kind != "dex.user" {
			continue
		}
		users++
		raw, err := json.Marshal(entry.Data)
		if err != nil {
			t.Fatal(err)
		}
		var metadata InputMetadata
		if err := json.Unmarshal(raw, &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata.RequestID != input.RequestID || metadata.Content != input.Content || metadata.Origin != input.Origin || metadata.Source != input.Source {
			t.Fatalf("replay changed input metadata: %+v", metadata)
		}
	}
	if users != 1 {
		t.Fatalf("native input entries=%d", users)
	}
	history, err := a.History(t.Context(), ref, 1<<62, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range history.Items {
		if item.Kind == "assistant" && item.Model != "faux/echo" {
			t.Fatalf("replay changed execution model: %+v", item)
		}
	}
	// A genuinely new input still requires a model, even in a configured conversation.
	if _, err := a.Submit(t.Context(), ref, Input{RequestID: "new", Content: "new"}); !errors.Is(err, ErrNoModel) {
		t.Fatalf("new admission without model: %v", err)
	}
}
