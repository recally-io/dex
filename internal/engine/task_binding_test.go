package engine

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"

	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

func TestUserTaskBindingSurvivesSuccessorAndRestart(t *testing.T) {
	reports := make(chan TaskContext, 4)
	config := Config{DataDir: t.TempDir(), Home: t.TempDir(), Tasks: TaskTools{ReportTask: func(_ context.Context, source TaskContext, _ TaskReport) error { reports <- source; return nil }}}
	a := coreEngine(t, config)
	opts := coreOptions("tool")
	ref := coreCreate(t, a, "alpha", opts)
	text := `report_task {"outcome":"done","summary":"ok","cause":"","reproduction":[],"files":[],"openItems":[]}`
	root, err := a.Submit(t.Context(), ref, Input{RequestID: "root", Content: text, Background: true, Options: opts})
	if err != nil {
		t.Fatal(err)
	}
	if out := coreWait(t, root); out.Status != "done" {
		t.Fatalf("root: %+v", out)
	}
	if source := <-reports; source.SourceRequestID != "root" {
		t.Fatalf("root source: %+v", source)
	}
	steer := Input{RequestID: "next", Content: text, Steer: &TrustedTaskSteer{OriginalRequestID: "root"}}
	next, err := a.Submit(t.Context(), ref, steer)
	if err != nil {
		t.Fatal(err)
	}
	if out := coreWait(t, next); out.Status != "done" {
		t.Fatalf("successor: %+v", out)
	}
	if source := <-reports; source.SourceRequestID != "root" {
		t.Fatalf("successor source: %+v", source)
	}
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	a = coreEngine(t, config)
	replay, err := a.Submit(t.Context(), ref, steer)
	if err != nil {
		t.Fatal(err)
	}
	if replay.ID != next.ID {
		t.Fatalf("replay: %d != %d", replay.ID, next.ID)
	}
	sealed, err := a.SealTask(t.Context(), ref, "root")
	if err != nil || !sealed {
		t.Fatalf("seal: %v %v", sealed, err)
	}
	steer.RequestID = "after-seal"
	if _, err = a.Submit(t.Context(), ref, steer); err == nil {
		t.Fatal("sealed task admitted new input")
	}
	got, err := a.LookupSubmission(t.Context(), ref, steer.RequestID)
	if err != nil || got.State != SubmissionAbsent {
		t.Fatalf("receipt after seal: %+v %v", got, err)
	}
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	a = coreEngine(t, config)
	if _, err = a.Submit(t.Context(), ref, steer); err == nil {
		t.Fatal("reopened sealed task admitted input")
	}
}

func TestUserTaskBindingRejectsConflictsWithoutReceipt(t *testing.T) {
	a := coreEngine(t, Config{})
	opts := coreOptions("echo")
	ref := coreCreate(t, a, "alpha", opts)
	old := coreSubmit(t, a, ref, "taken", "old", "echo")
	coreWait(t, old)
	// Before a root exists, the engine cannot tell an ordinary input from a task admitted before
	// bindings existed, so the guard starts with the root.
	root, err := a.Submit(t.Context(), ref, Input{RequestID: "root", Content: "goal", Background: true, Options: opts})
	if err != nil {
		t.Fatal(err)
	}
	coreWait(t, root)
	if _, err := a.Submit(t.Context(), ref, Input{RequestID: "taken", Background: true, Options: opts}); err == nil {
		t.Fatal("existing ordinary input became a root")
	}
	if _, err := a.Submit(t.Context(), ref, Input{RequestID: "root", Kind: "task_result", Background: true}); err == nil {
		t.Fatal("root replay changed input kind")
	}
	if _, err := a.Submit(t.Context(), ref, Input{RequestID: "other-root", Background: true, Options: opts}); err == nil {
		t.Fatal("second root accepted")
	}
	got, err := a.LookupSubmission(t.Context(), ref, "other-root")
	if err != nil || got.State != SubmissionAbsent {
		t.Fatalf("failed root created receipt: %+v %v", got, err)
	}
}

func TestUserTaskBindingSealRejectsOrdinaryNewInput(t *testing.T) {
	a := coreEngine(t, Config{})
	opts := coreOptions("echo")
	ref := coreCreate(t, a, "alpha", opts)
	old := coreSubmit(t, a, ref, "old", "old", "echo")
	coreWait(t, old)
	root, err := a.Submit(t.Context(), ref, Input{RequestID: "root", Content: "goal", Background: true, Options: opts})
	if err != nil {
		t.Fatal(err)
	}
	coreWait(t, root)
	sealed, err := a.SealTask(t.Context(), ref, "root")
	if err != nil || !sealed {
		t.Fatalf("seal: %v %v", sealed, err)
	}
	h, err := a.harness(t.Context(), ref.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := a.conversation(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	before, err := durable.Snapshot[harness.AgentState](t.Context(), h, harness.AgentDoc, conv.Id())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Submit(t.Context(), ref, Input{RequestID: "ordinary", Content: "restart", Options: coreOptions("shout")}); err == nil {
		t.Fatal("sealed task admitted ordinary input")
	}
	for _, input := range []Input{{RequestID: "old", Options: coreOptions("shout")}, {RequestID: "root", Background: true, Options: coreOptions("shout")}} {
		replay, err := a.Submit(t.Context(), ref, input)
		if err != nil {
			t.Fatal(err)
		}
		if (input.Background && replay.ID != root.ID) || (!input.Background && replay.ID != old.ID) {
			t.Fatalf("wrong replay ID: %d", replay.ID)
		}
		coreWait(t, replay)
	}
	after, err := durable.Snapshot[harness.AgentState](t.Context(), h, harness.AgentDoc, conv.Id())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("agent configuration changed: before=%+v after=%+v err=%v", before, after, err)
	}
	got, err := a.LookupSubmission(t.Context(), ref, "ordinary")
	if err != nil || got.State != SubmissionAbsent {
		t.Fatalf("rejected input receipt: %+v %v", got, err)
	}
}

func TestUserTaskBindingSealWithNativeCompaction(t *testing.T) {
	a := coreEngine(t, Config{})
	opts := coreOptions("echo")
	ref := coreCreate(t, a, "alpha", opts)
	root, err := a.Submit(t.Context(), ref, Input{RequestID: "root", Content: "goal", Background: true, Options: opts})
	if err != nil {
		t.Fatal(err)
	}
	coreWait(t, root)
	conv, err := a.conversation(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conv.Commit(t.Context(), func(tx durable.Tx) (any, error) {
		_, err := harness.CreateCompaction(tx, conv.Id(), harness.CompactionInput{Reason: durable.CompactionThreshold}, nil)
		return nil, err
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := a.SealTask(t.Context(), ref, "root")
	if err != nil {
		t.Fatal(err)
	}
	if sealed {
		t.Fatal("sealed while compaction is live")
	}
}

func TestUserTaskBindingRollback(t *testing.T) {
	a := coreEngine(t, Config{})
	opts := coreOptions("echo")
	ref := coreCreate(t, a, "alpha", opts)
	root, err := a.Submit(t.Context(), ref, Input{RequestID: "root", Content: "goal", Background: true, Options: opts})
	if err != nil {
		t.Fatal(err)
	}
	coreWait(t, root)
	conv, err := a.conversation(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("rollback")
	_, err = conv.Commit(t.Context(), func(tx durable.Tx) (any, error) {
		doc, b, err := bindingDraft(tx, conv.Id())
		if err != nil {
			return nil, err
		}
		b.Sealed = true
		if err := saveBinding(doc, b); err != nil {
			return nil, err
		}
		return nil, rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("commit error: %v", err)
	}
	next, err := a.Submit(t.Context(), ref, Input{RequestID: "next", Content: "successor", Steer: &TrustedTaskSteer{OriginalRequestID: "root"}})
	if err != nil {
		t.Fatalf("rollback left sealed binding: %v", err)
	}
	coreWait(t, next)
	sealed, err := a.SealTask(t.Context(), ref, "root")
	if err != nil || !sealed {
		t.Fatalf("seal: %v %v", sealed, err)
	}
	replay, err := a.Submit(t.Context(), ref, Input{RequestID: "next", Steer: &TrustedTaskSteer{OriginalRequestID: "root"}})
	if err != nil || replay.ID != next.ID {
		t.Fatalf("sealed replay: %v %v", replay, err)
	}
	coreWait(t, replay)
	if _, err := a.Submit(t.Context(), ref, Input{RequestID: "after", Steer: &TrustedTaskSteer{OriginalRequestID: "root"}}); err == nil {
		t.Fatal("seal did not block later submission")
	}
}

func TestUserTaskBindingAdmissionRollback(t *testing.T) {
	a := coreEngine(t, Config{})
	opts := coreOptions("echo")
	ref := coreCreate(t, a, "alpha", opts)
	root, err := a.Submit(t.Context(), ref, Input{RequestID: "root", Content: "goal", Background: true, Options: opts})
	if err != nil {
		t.Fatal(err)
	}
	coreWait(t, root)
	conv, err := a.conversation(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("rollback admission")
	request := "rolled-back"
	_, err = conv.Commit(t.Context(), func(tx durable.Tx) (any, error) {
		doc, binding, err := bindingDraft(tx, conv.Id())
		if err != nil {
			return nil, err
		}
		draft := durable.SubmissionDraft{Type: durable.SubmissionTypeInput, RequestId: &request, Content: ai.UserText("ignored"), WhenBusy: durable.WhenBusySteer, InputEntry: &durable.InputEntry{Kind: "dex.user"}}
		admitted, err := harness.AdmitSubmission(tx, conv.Id(), draft, float64(time.Now().UnixMilli()), taskQueueModes, nil)
		if err != nil {
			return nil, err
		}
		binding.Inputs[request] = taskBoundInput{ID: admitted.Id, Kind: "dex.user"}
		if err := saveBinding(doc, binding); err != nil {
			return nil, err
		}
		return nil, rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("commit error: %v", err)
	}
	got, err := a.LookupSubmission(t.Context(), ref, request)
	if err != nil || got.State != SubmissionAbsent {
		t.Fatalf("rolled back receipt: %+v %v", got, err)
	}
	_, err = conv.Commit(t.Context(), func(tx durable.Tx) (any, error) {
		_, b, err := bindingDraft(tx, conv.Id())
		if err != nil {
			return nil, err
		}
		if _, ok := b.Inputs[request]; ok {
			t.Error("rolled back binding persisted")
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := a.SealTask(t.Context(), ref, "root")
	if err != nil || !sealed {
		t.Fatalf("rollback left work: %v %v", sealed, err)
	}
}
