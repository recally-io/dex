package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

func TestUserSteerJoinsBackgroundRun(t *testing.T) {
	// Given a background run paused in a real bash tool.
	dir := t.TempDir()
	started, release := filepath.Join(dir, "started"), filepath.Join(dir, "release")
	reports := make(chan TaskContext, 1)
	a := coreEngine(t, Config{Tasks: TaskTools{ReportTask: func(_ context.Context, source TaskContext, _ TaskReport) error { reports <- source; return nil }}})
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })
	faux := ai.NewFauxProvider(ai.FauxConfig{ProviderID: "probe", Models: []ai.FauxModelDefinition{{ID: "probe"}}, TokensPerSecond: 1000})
	seen := make(chan []string, 4)
	factory := ai.FauxResponseFactory(func(transcript ai.TranscriptContext, _ ai.StreamOptions, _ *ai.FauxProviderState, _ *ai.Model) (ai.AssistantMessage, error) {
		var users []string
		var bashDone, reportDone bool
		for _, msg := range transcript.Messages() {
			if user, ok := msg.(ai.UserMessage); ok {
				if text, ok := user.Content.(ai.UserText); ok {
					users = append(users, string(text))
				}
			}
			if result, ok := msg.(ai.ToolResultMessage); ok {
				if result.ToolName == "bash" {
					bashDone = true
				}
				if result.ToolName == "report_task" {
					reportDone = true
				}
			}
		}
		if !bashDone {
			return fakeCall("bash", map[string]any{"command": fmt.Sprintf("touch %q; while [ ! -f %q ]; do sleep 0.05; done", started, release)}), nil
		}
		if !reportDone {
			seen <- users
			return fakeCall("report_task", map[string]any{"outcome": "done", "summary": "ok", "cause": "", "reproduction": []any{}, "files": []any{}, "openItems": []any{}}), nil
		}
		return fakeText("complete"), nil
	})
	p := faux.Provider()
	stream := p.Stream
	p.Stream = func(ctx context.Context, model *ai.Model, transcript ai.TranscriptContext, options ai.StreamOptions) (*ai.AssistantMessageEventStream, error) {
		faux.AppendResponses([]ai.FauxResponseStep{factory})
		return stream(ctx, model, transcript, options)
	}
	p.StreamSimple = p.Stream
	a.Catalog.Models.SetProvider(p)
	opts := Options{Model: ModelRef{Provider: "probe", ModelID: "probe"}, Instructions: "original instructions"}
	ref := coreCreate(t, a, "alpha", opts)
	old, err := a.Submit(t.Context(), ref, Input{RequestID: "old-task", Content: "old", Options: coreOptions("echo")})
	if err != nil {
		t.Fatal(err)
	}
	coreWait(t, old)
	original, err := a.Submit(t.Context(), ref, Input{RequestID: "original", Content: "goal", Background: true, Options: opts})
	if err != nil {
		t.Fatal(err)
	}
	coreFile(t, started)
	if sealed, err := a.SealTask(t.Context(), ref, "original"); err != nil || sealed {
		t.Fatalf("active task seal: %v %v", sealed, err)
	}
	// A different, finished task cannot anchor admission into this run.
	_, err = a.Submit(t.Context(), ref, Input{RequestID: "wrong-task", Content: "wrong", Steer: &TrustedTaskSteer{OriginalRequestID: "old-task"}})
	if err == nil {
		t.Fatal("admitted steer for a different task")
	}
	lookup, err := a.LookupSubmission(t.Context(), ref, "wrong-task")
	if err != nil || lookup.State != SubmissionAbsent {
		t.Fatalf("wrong-task receipt: %+v, %v", lookup, err)
	}
	// When two steer inputs and a duplicate arrive before the tool returns.
	submit := func(id, text string) *Submission {
		in := Input{RequestID: id, Content: text, Options: coreOptions("shout"), Steer: &TrustedTaskSteer{OriginalRequestID: "original"}}
		sub, err := a.Submit(t.Context(), ref, in)
		if err != nil {
			t.Fatal(err)
		}
		return sub
	}
	one := submit("steer-1", "first")
	if sealed, err := a.SealTask(t.Context(), ref, "original"); err != nil || sealed {
		t.Fatalf("seal after steer admission: %v %v", sealed, err)
	}
	for _, in := range []Input{
		{RequestID: "steer-1", Kind: "task_result", Steer: &TrustedTaskSteer{OriginalRequestID: "original"}},
		{RequestID: "steer-1", Steer: &TrustedTaskSteer{OriginalRequestID: "old-task"}},
		{RequestID: "original", Steer: &TrustedTaskSteer{OriginalRequestID: "original"}},
		{RequestID: "old-task", Steer: &TrustedTaskSteer{OriginalRequestID: "original"}},
	} {
		if _, err := a.Submit(t.Context(), ref, in); err == nil {
			t.Fatalf("conflicting request accepted: %+v", in)
		}
	}
	two := submit("steer-2", "second")
	duplicate := submit("steer-1", "first")
	if duplicate.ID != one.ID {
		t.Fatalf("duplicate admitted twice: %d != %d", duplicate.ID, one.ID)
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	var users []string
	select {
	case users = <-seen:
	case <-ctx.Done():
		t.Fatal("model never reached post-tool generation")
	}
	if strings.Join(users, "|") != "old|goal|first|second" {
		t.Fatalf("post-tool inputs: %q", users)
	}
	for _, sub := range []*Submission{original, one, two, duplicate} {
		out, err := sub.Wait(ctx)
		if err != nil || out.Status != "done" {
			t.Fatalf("outcome: %+v, %v", out, err)
		}
	}
	// A retry after settlement reacquires the same submission and starts no run.
	replay := submit("steer-1", "first")
	if replay.ID != one.ID {
		t.Fatalf("replay ID: %d != %d", replay.ID, one.ID)
	}
	if out, err := replay.Wait(ctx); err != nil || out.Status != "done" {
		t.Fatalf("replay outcome: %+v, %v", out, err)
	}
	conversation, err := a.conversation(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conversation.Commit(ctx, func(tx durable.Tx) (any, error) {
		doc, err := tx.Doc(harness.LiveDoc, conversation.Id())
		if err != nil {
			return nil, err
		}
		live, err := durable.FromJsonValue[harness.LiveState](doc.Snapshot())
		if err == nil && live.Run != nil {
			t.Error("replay started a new run")
		}
		return nil, err
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case source := <-reports:
		if source.SourceRequestID != "original" {
			t.Fatalf("report source: %+v", source)
		}
	case <-ctx.Done():
		t.Fatal("report_task did not reach handler")
	}
}
