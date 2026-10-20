package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
)

func TestUserFauxTaskResultKeepsStableMetadata(t *testing.T) {
	// Given a task result with the native follow-up metadata.
	agents := coreEngine(t, Config{})
	ref := coreCreate(t, agents, "alpha", coreOptions("spawn"))
	input := `{"task-result":{"id":"task-1","title":"Report title","status":"done","report":"echo: report"}}`
	// When the faux model receives the result as an ordinary queued input.
	result := coreWait(t, coreSubmit(t, agents, ref, "result", input, "spawn"))
	// Then the answer keeps all fields and has stable JSON for transcript assertions.
	want := `task-result: {"id":"task-1","report":"echo: report","status":"done","title":"Report title"}`
	if result.Status != "done" || result.Answer != want {
		t.Fatalf("task result: %+v; want %q", result, want)
	}
}

func TestUserFauxAnswersConcurrentRequests(t *testing.T) {
	// Given a first request paused before the faux response factory runs.
	provider := fakeProvider()
	models, err := provider.GetModels()
	if err != nil {
		t.Fatal(err)
	}
	var model *ai.Model
	for _, candidate := range models {
		if candidate.ID == "echo" {
			model = candidate
			break
		}
	}
	if model == nil {
		t.Fatal("faux echo model missing")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	transcript := func(text string) ai.TranscriptContext {
		return ai.NormalizeContext(ai.Context{Messages: []ai.Message{ai.UserMessage{Content: ai.UserText(text)}}})
	}
	first, err := provider.StreamSimple(ctx, model, transcript("first"), ai.StreamOptions{OnResponse: func(ctx context.Context, _ ai.ProviderResponse, _ *ai.Model) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// When another request reaches the provider while the first factory is paused.
	second, err := provider.StreamSimple(ctx, model, transcript("second"), ai.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result := second.Result()
	// Then both requests have their own response instead of exhausting a shared script queue.
	if result.StopReason != ai.StopReasonStop || ai.ContentText(result.Content) != "echo: second" {
		t.Fatalf("second response: stop=%s text=%q error=%q", result.StopReason, ai.ContentText(result.Content), result.ErrorMessage)
	}
	releaseOnce.Do(func() { close(release) })
	if result := first.Result(); result.StopReason != ai.StopReasonStop || ai.ContentText(result.Content) != "echo: first" {
		t.Fatalf("first response: %+v", result)
	}
}
