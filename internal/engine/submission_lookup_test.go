package engine

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
)

func TestSubmissionLookupLiveAndCold(t *testing.T) {
	dir := t.TempDir()
	started, release := filepath.Join(dir, "started"), filepath.Join(dir, "release")
	a := coreEngine(t, Config{})
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })
	faux := ai.NewFauxProvider(ai.FauxConfig{ProviderID: "lookup", Models: []ai.FauxModelDefinition{{ID: "lookup"}}, TokensPerSecond: 1000})
	factory := ai.FauxResponseFactory(func(transcript ai.TranscriptContext, _ ai.StreamOptions, _ *ai.FauxProviderState, _ *ai.Model) (ai.AssistantMessage, error) {
		for _, msg := range transcript.Messages() {
			if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolName == "bash" {
				return fakeText("done"), nil
			}
		}
		return fakeCall("bash", map[string]any{"command": fmt.Sprintf("touch %q; while [ ! -f %q ]; do sleep 0.05; done", started, release)}), nil
	})
	p := faux.Provider()
	stream := p.Stream
	p.Stream = func(ctx context.Context, m *ai.Model, tr ai.TranscriptContext, opts ai.StreamOptions) (*ai.AssistantMessageEventStream, error) {
		faux.AppendResponses([]ai.FauxResponseStep{factory})
		return stream(ctx, m, tr, opts)
	}
	p.StreamSimple = p.Stream
	a.Catalog.Models.SetProvider(p)
	opts := Options{Model: ModelRef{Provider: "lookup", ModelID: "lookup"}}
	ref := coreCreate(t, a, "alpha", opts)
	get := func(id string) SubmissionLookup {
		t.Helper()
		got, err := a.LookupSubmission(t.Context(), ref, id)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := get("missing"); got.State != SubmissionAbsent {
		t.Fatalf("missing: %+v", got)
	}
	first, err := a.Submit(t.Context(), ref, Input{RequestID: "first", Content: "goal", Background: true, Options: opts})
	if err != nil {
		t.Fatal(err)
	}
	coreFile(t, started)
	if got := get("first"); got.State != SubmissionPlaced || got.Entry == nil {
		t.Fatalf("placed: %+v", got)
	}
	queued, err := a.Submit(t.Context(), ref, Input{RequestID: "queued", Content: "雪 \"quoted\"", Steer: &TrustedTaskSteer{OriginalRequestID: "first"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := get("queued"); got.State != SubmissionQueued || got.Entry != nil {
		t.Fatalf("queued: %+v", got)
	}
	withdrawn, err := a.Submit(t.Context(), ref, Input{RequestID: "withdrawn", Content: "cancel", Steer: &TrustedTaskSteer{OriginalRequestID: "first"}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := a.harness(t.Context(), ref.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.AbortSubmission(t.Context(), durable.SubmissionId(withdrawn.ID), nil); err != nil {
		t.Fatal(err)
	}
	if got := get("withdrawn"); got.State != SubmissionUnanswered || got.Entry != nil || got.Reason == nil {
		t.Fatalf("unanswered: %+v", got)
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	coreWait(t, first)
	coreWait(t, queued)
	got := get("queued")
	if got.State != SubmissionDone || got.Entry == nil || got.Entry.Kind != "dex.user" {
		t.Fatalf("done: %+v", got)
	}
	raw, err := json.Marshal(got.Entry.Data)
	if err != nil {
		t.Fatal(err)
	}
	var meta InputMetadata
	if err = json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.RequestID != "queued" || meta.Content != "雪 \"quoted\"" {
		t.Fatalf("entry: %s", raw)
	}
	other := ref
	other.ID++
	if miss, err := a.LookupSubmission(t.Context(), other, "queued"); err != nil || miss.State != SubmissionAbsent {
		t.Fatalf("scope: %+v %v", miss, err)
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	cold := coreEngine(t, Config{DataDir: a.config.DataDir, Home: a.config.Home})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			result, err := cold.LookupSubmission(t.Context(), ref, "queued")
			if err != nil || result.State != SubmissionDone {
				t.Errorf("concurrent cold lookup: %+v, %v", result, err)
			}
		})
	}
	wg.Wait()
	cold.mu.Lock()
	opened := len(cold.resources)
	cold.mu.Unlock()
	if opened != 0 {
		t.Fatalf("cold lookup opened %d harnesses", opened)
	}
	recovered, err := cold.LookupSubmission(t.Context(), ref, "queued")
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != got.State || recovered.Entry == nil || recovered.Entry.ID != got.Entry.ID {
		t.Fatalf("cold: %+v; live: %+v", recovered, got)
	}
	recoveredJSON, err := json.Marshal(recovered.Entry.Data)
	if err != nil || string(recoveredJSON) != string(raw) {
		t.Fatalf("cold JSON: %s %v; want %s", recoveredJSON, err, raw)
	}
}
