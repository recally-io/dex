package engine

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
)

func TestUserReadingExecutionDoesNotStartAColdAgent(t *testing.T) {
	// Given an engine that has not opened the referenced bot.
	a := coreEngine(t, Config{})
	// When a parent reads its execution metadata, no cold initialization runs.
	state, err := a.Execution(t.Context(), Ref{AgentID: "cold", ID: 1}, []string{"saved"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Phase != "unavailable" {
		t.Fatalf("cold execution status: %+v", state)
	}
	// Then observation has created neither an agent directory nor a native database.
	if _, err := os.Stat(filepath.Join(a.config.DataDir, "agents", "cold")); !os.IsNotExist(err) {
		t.Fatalf("status read initialized a cold agent: %v", err)
	}
}

func TestUserSeesModelWorkAndRetryBeforeContent(t *testing.T) {
	// Given a real Harness with a provider boundary held before its first content event.
	a := coreEngine(t, Config{})
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	provider := ai.NewFauxProvider(ai.FauxConfig{ProviderID: "status", Models: []ai.FauxModelDefinition{{ID: "held"}}, TokensPerSecond: 10000})
	provider.SetResponses([]ai.FauxResponseStep{
		ai.FauxResponseFactory(func(ai.TranscriptContext, ai.StreamOptions, *ai.FauxProviderState, *ai.Model) (ai.AssistantMessage, error) {
			close(entered)
			<-release
			return ai.FauxAssistantMessage(ai.FauxContentString(""), ai.FauxAssistantMessageOptions{StopReason: "error", ErrorMessage: "overloaded"}), nil
		}),
		ai.FauxAssistantMessage(ai.FauxContentString("recovered"), ai.FauxAssistantMessageOptions{}),
	})
	a.Catalog.Models.SetProvider(provider.Provider())
	options := Options{Model: ModelRef{Provider: "status", ModelID: "held"}, ThinkingLevel: "off"}
	ref := coreCreate(t, a, "alpha", options)
	submission, err := a.Submit(t.Context(), ref, Input{RequestID: "held", Content: "work", Child: true, Options: options})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("provider did not start")
	}
	// When the UI subscribes without any assistant content, the native phase is already model.
	watch, err := a.Watch(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Close()
	if watch.Snapshot.Phase != "model" || !watch.Snapshot.Running {
		t.Fatalf("held provider status: %+v", watch.Snapshot)
	}
	for _, item := range watch.Snapshot.Items {
		if item.Kind == "assistant" {
			t.Fatal("the phase must not depend on assistant text")
		}
	}
	once.Do(func() { close(release) })
	// Then a retry is a phase of the same submission, not a new child or a successful outcome.
	seenRetry := false
	for !seenRetry {
		select {
		case <-ctx.Done():
			t.Fatal("retry was not observable")
		case event, ok := <-watch.Events:
			if !ok {
				t.Fatal("execution stream closed")
			}
			seenRetry = event.Phase == "retry" && event.Running && event.Retry != nil
		}
	}
	if result := coreWait(t, submission); result.Status != "done" {
		t.Fatalf("retry outcome: %+v", result)
	}
	state, err := a.Execution(ctx, ref, []string{"held"})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Turns) != 1 || state.Turns[0].Status != "done" {
		t.Fatalf("submission outcome: %+v", state)
	}
}
