package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

func isolationGate(t *testing.T, dir, name string) (string, string, func()) {
	t.Helper()
	started := filepath.Join(dir, name+".started")
	gate := filepath.Join(dir, name+".release")
	release := func() {
		t.Helper()
		if err := os.WriteFile(gate, nil, 0o600); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(release)
	return fmt.Sprintf(`touch '%s'; until test -f '%s'; do sleep .02; done; printf '%s'`, started, gate, name), started, release
}

func nativeRun(t *testing.T, a *Agents, ref Ref) *harness.LiveRun {
	t.Helper()
	c, err := a.conversation(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	var run *harness.LiveRun
	_, err = c.Commit(t.Context(), func(tx durable.Tx) (any, error) {
		var err error
		run, err = liveRun(tx, c.Id())
		return nil, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func isolatedNativeRun(t *testing.T, a *Agents, ref Ref, s *Submission) durable.TaskId {
	t.Helper()
	run := nativeRun(t, a, ref)
	if run == nil || run.First == nil || int64(*run.First) != s.ActivationID || !slices.Equal(run.Inputs, []durable.SubmissionId{durable.SubmissionId(s.ID)}) {
		t.Fatalf("submission %d is not the sole starting input (activation %d): %+v", s.ID, s.ActivationID, run)
	}
	if int64(run.TaskId) == s.ActivationID || int64(run.TaskId) == s.ID || s.ActivationID == s.ID {
		t.Fatalf("test requires distinct native generation, submission and first entry IDs: %d, %d, %d", run.TaskId, s.ID, s.ActivationID)
	}
	return run.TaskId
}

func TestUserPendingSteerCannotJoinProtectedChildAtRunnerBoundary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := coreEngine(t, Config{DataDir: dir})
	ref := coreCreate(t, a, "alpha", coreOptions("bash"))
	submit := func(id, origin, mode string, isolation InputIsolation, content string) (*Submission, error) {
		return a.Submit(t.Context(), ref, Input{RequestID: id, Kind: "message", Child: true, Origin: origin, WhenBusy: mode, Isolation: isolation, Content: content, Options: coreOptions("bash")})
	}
	// Given an older ordinary activation and an ordinary steer in its native Inbox.
	command, running, stopOrdinary := isolationGate(t, dir, "ordinary-active")
	ordinary, err := submit("ordinary-first", "task", "followUp", "", command)
	if err != nil {
		t.Fatal(err)
	}
	coreFile(t, running)
	command, steerRunning, stopSteer := isolationGate(t, dir, "ordinary-steer")
	steer, err := submit("ordinary-steer", "web", "steer", "", command)
	if err != nil {
		t.Fatal(err)
	}
	if record, err := steer.native.Status(t.Context()); err != nil || record.Status != durable.SubmissionQueued {
		t.Fatalf("native steer is not pending: %+v, %v", record, err)
	}
	protectedCommand, protectedRunning, stopProtected := isolationGate(t, dir, "protected")
	tryProtected := func() (*Submission, error) {
		return submit("opted-in", "task", "followUp", IsolatedDelegation, protectedCommand)
	}
	if _, err := tryProtected(); !errors.Is(err, ErrBusy) {
		t.Fatalf("protected input shared pending ordinary work: %v", err)
	}
	// When PiG reaches its actual post-tool boundary, it consumes the pending
	// steer into the ordinary run, never into a new protected activation.
	stopOrdinary()
	coreFile(t, steerRunning)
	run := nativeRun(t, a, ref)
	if run == nil || !slices.Equal(run.Inputs, []durable.SubmissionId{durable.SubmissionId(ordinary.ID), durable.SubmissionId(steer.ID)}) {
		t.Fatalf("pending ordinary steer did not join its native boundary: %+v", run)
	}
	if _, err := tryProtected(); !errors.Is(err, ErrBusy) {
		t.Fatalf("protected input attached after ordinary steering: %v", err)
	}
	stopSteer()
	if got, steerOutcome := coreWait(t, ordinary), coreWait(t, steer); got.AnswerID != steerOutcome.AnswerID || got.Answer != "bash: ordinary-steer" {
		t.Fatalf("ordinary shared answer changed: %+v, %+v", got, steerOutcome)
	}
	// Then the retry starts a sole-input activation. A later parent steer is
	// deferred as an ordinary follow-up, even through an admission replay.
	protected, err := tryProtected()
	if err != nil {
		t.Fatal(err)
	}
	coreFile(t, protectedRunning)
	isolatedNativeRun(t, a, ref, protected)
	replay, err := submit("opted-in", "task", "steer", "", "printf replacement")
	if err != nil || replay.ID != protected.ID || replay.ActivationID != protected.ActivationID {
		t.Fatalf("native replay changed request/activation binding: %+v, %v", replay, err)
	}
	late, err := submit("late-parent-steer", "task", "steer", "", "printf later")
	if err != nil {
		t.Fatal(err)
	}
	isolatedNativeRun(t, a, ref, protected)
	stopProtected()
	if got, independent := coreWait(t, replay), coreWait(t, late); got.Answer != "bash: protected" || independent.Answer != "bash: later" || got.AnswerID == independent.AnswerID || independent.ActivationID != 0 {
		t.Fatalf("regular steer acquired a protected result: %+v, %+v", got, independent)
	}
}

func TestUserCanonicalReviewsDoNotShareCurrentOrQueuedMainWork(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := coreEngine(t, Config{DataDir: dir})
	ref := coreCreate(t, a, "alpha", coreOptions("bash"))
	submit := func(input Input) *Submission {
		t.Helper()
		input.Main, input.Origin, input.Options = true, "telegram:42", coreOptions("bash")
		s, err := a.Submit(t.Context(), ref, input)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	busy := func(input Input) {
		t.Helper()
		input.Main, input.Origin, input.Options = true, "telegram:42", coreOptions("bash")
		if _, err := a.Submit(t.Context(), ref, input); !errors.Is(err, ErrBusy) {
			t.Fatalf("%s must remain pending rather than share a native activation: %v", input.RequestID, err)
		}
	}

	// Given an ordinary foreground run and an ordinary steer already in the native Inbox.
	command, started, _ := isolationGate(t, dir, "foreground")
	foreground := submit(Input{RequestID: "foreground", Kind: "message", Content: command})
	coreFile(t, started)
	command, steerStarted, releaseSteer := isolationGate(t, dir, "pending-steer")
	steer := submit(Input{RequestID: "steer-before-review", Kind: "message", Content: command, WhenBusy: "steer"})
	reviewCommand, reviewStarted, releaseReview := isolationGate(t, dir, "first-review")
	review := Input{RequestID: "result:one", Kind: "task_result", Isolation: IsolatedReview, Content: reviewCommand}
	busy(review)

	// When precise Stop has aborted its foreground task but not yet applied its Inbox boundary,
	// a protected review must *still* wait. Native SubmitIn on this idle Inbox would otherwise
	// start a generation containing the pending steer and the review together.
	h, err := a.harness(t.Context(), ref.AgentID, ref.Scope)
	if err != nil {
		t.Fatal(err)
	}
	current := nativeRun(t, a, ref)
	if _, err := h.AbortTask(t.Context(), current.TaskId); err != nil {
		t.Fatal(err)
	}
	if got := coreWait(t, foreground); got.Reason != "aborted" {
		t.Fatalf("foreground was not stopped: %+v", got)
	}
	busy(review)
	if err := a.StopRun(t.Context(), ref, runTargetPrefix(ref)+fmt.Sprint(current.TaskId)); err != nil {
		t.Fatal(err)
	}
	coreFile(t, steerStarted)
	busy(review)
	releaseSteer()
	if got := coreWait(t, steer); got.Answer != "bash: pending-steer" {
		t.Fatalf("the existing native steer was not serviced at the boundary: %+v", got)
	}

	// Then each same-origin review owns its own activation, independent of ordinary queued
	// and steering input. Neither pending result has been dropped or executed early.
	first := submit(review)
	coreFile(t, reviewStarted)
	firstGeneration := isolatedNativeRun(t, a, ref, first)
	command, secondStarted, releaseSecond := isolationGate(t, dir, "second-review")
	secondReview := Input{RequestID: "result:two", Kind: "task_result", Isolation: IsolatedReview, Content: command}
	ordinary := Input{RequestID: "queued-during-review", Kind: "message", Content: "printf ordinary"}
	lateSteer := Input{RequestID: "steer-during-review", Kind: "message", Content: "printf late-steer", WhenBusy: "steer"}
	for _, input := range []Input{secondReview, ordinary, lateSteer} {
		busy(input)
	}
	// Another session on the same agent is not serialized behind Main.
	other := coreCreate(t, a, "alpha", coreOptions("echo"))
	if got := coreWait(t, coreSubmit(t, a, other, "unrelated", "parallel", "echo")); got.Answer != "echo: parallel" {
		t.Fatalf("unrelated input: %+v", got)
	}
	isolatedNativeRun(t, a, ref, first)
	releaseReview()
	if got := coreWait(t, first); got.Answer != "bash: first-review" || got.ActivationID != first.ActivationID {
		t.Fatalf("first review: %+v", got)
	}
	second := submit(secondReview)
	coreFile(t, secondStarted)
	if isolatedNativeRun(t, a, ref, second) == firstGeneration || second.ActivationID == first.ActivationID {
		t.Fatal("reviews shared an activation")
	}
	busy(ordinary)
	releaseSecond()
	if got := coreWait(t, second); got.Answer != "bash: second-review" {
		t.Fatalf("second review: %+v", got)
	}
	for _, input := range []Input{ordinary, lateSteer} {
		if got := coreWait(t, submit(input)); got.Answer != "bash: "+input.Content[len("printf "):] {
			t.Fatalf("ordinary deferred input %s: %+v", input.RequestID, got)
		}
	}
}
