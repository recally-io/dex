package engine

import (
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
)

func TestUserTaskFromBeforeBindingsReplaysAfterUpgrade(t *testing.T) {
	// Given a task admitted by the old native path, without a task binding.
	config := Config{DataDir: t.TempDir(), Home: t.TempDir()}
	a := coreEngine(t, config)
	opts := coreOptions("echo")
	ref := coreCreate(t, a, "alpha", opts)
	conv, err := a.conversation(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	request := "legacy-root"
	root, err := conv.Submit(t.Context(), durable.SubmissionDraft{
		Type: durable.SubmissionTypeInput, RequestId: &request,
		Content: ai.UserText("goal"), WhenBusy: durable.WhenBusyFollowUp,
		InputEntry: &durable.InputEntry{Kind: "dex.user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	settled, err := root.Wait(t.Context())
	if err != nil || settled.Status != durable.SubmissionDone {
		t.Fatalf("legacy outcome: %+v, %v", settled, err)
	}
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	// When the caller replays the original task input after the upgrade.
	a = coreEngine(t, config)
	replay, err := a.Submit(t.Context(), ref, Input{RequestID: request, Background: true, Options: coreOptions("shout")})
	if err != nil {
		t.Fatalf("replay after upgrade: %v", err)
	}

	// Then replay returns the same admission and answer, and ignores changed settings.
	if replay.ID != int64(root.Id()) {
		t.Fatalf("replay ID: %d != %d", replay.ID, root.Id())
	}
	if got := coreWait(t, replay); got.Status != "done" || got.Answer != "echo: goal" {
		t.Fatalf("replayed outcome: %+v", got)
	}
	conv, err = a.conversation(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := conv.Agent(t.Context())
	if err != nil || agent.Model == nil || agent.Model.ModelId != "echo" {
		t.Fatalf("replay changed the model: %+v, %v", agent, err)
	}
	// Replay must not grant an unbound legacy input authority over a task.
	if _, err := a.SealTask(t.Context(), ref, request); err == nil {
		t.Fatal("legacy replay created a sealable task binding")
	}
	if _, err := a.Submit(t.Context(), ref, Input{
		RequestID: "legacy-steer", Content: "change goal", Steer: &TrustedTaskSteer{OriginalRequestID: request},
	}); err == nil {
		t.Fatal("legacy replay created steering authority")
	}
}

func TestUserContinuesSealedTaskWithoutReportAuthority(t *testing.T) {
	// Given a finished task whose delegated input has been sealed.
	a := coreEngine(t, Config{})
	opts := coreOptions("tool")
	ref := coreCreate(t, a, "alpha", opts)
	root, err := a.Submit(t.Context(), ref, Input{RequestID: "root", Content: "goal", Background: true, Options: opts})
	if err != nil {
		t.Fatal(err)
	}
	coreWait(t, root)
	sealed, err := a.SealTask(t.Context(), ref, "root")
	if err != nil || !sealed {
		t.Fatalf("seal: %v %v", sealed, err)
	}

	// When the user continues in the same session after task completion.
	text := `report_task {"outcome":"done","summary":"replace report","cause":"","reproduction":[],"files":[],"openItems":[]}`
	continued, err := a.Submit(t.Context(), ref, Input{
		RequestID: "continuation", Content: text, TaskContinuation: true, Options: opts,
	})
	if err != nil {
		t.Fatalf("continue sealed task: %v", err)
	}

	// Then the session answers, but report_task is no longer available.
	if got := coreWait(t, continued); got.Status != "done" || !strings.HasPrefix(got.Answer, "tool error: ") {
		t.Fatalf("continued outcome: %+v", got)
	}
	if _, err := a.Submit(t.Context(), ref, Input{
		RequestID: "late-steer", Content: "change goal", Steer: &TrustedTaskSteer{OriginalRequestID: "root"},
	}); err == nil {
		t.Fatal("continuation reopened the task to steering")
	}
}
