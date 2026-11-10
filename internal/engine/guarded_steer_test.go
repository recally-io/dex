package engine

import (
	"testing"

	"github.com/MichaelKinsy/PiG/durable"
)

func TestUserSteerDoesNotStartIdleOrFinishedTask(t *testing.T) {
	for _, finished := range []bool{false, true} {
		name := "idle"
		if finished {
			name = "finished"
		}
		t.Run(name, func(t *testing.T) {
			// Given an idle conversation, optionally after a task has settled.
			a := coreEngine(t, Config{})
			opts := coreOptions("echo")
			ref := coreCreate(t, a, "alpha", opts)
			if finished {
				original, err := a.Submit(t.Context(), ref, Input{RequestID: "original", Content: "goal", Background: true, Options: opts})
				if err != nil {
					t.Fatal(err)
				}
				if outcome := coreWait(t, original); outcome.Status != "done" {
					t.Fatalf("original task did not finish: %+v", outcome)
				}
			}
			// Sealing wins before admission, so neither case may create a receipt.
			if finished {
				sealed, err := a.SealTask(t.Context(), ref, "original")
				if err != nil || !sealed {
					t.Fatalf("seal: %v, %v", sealed, err)
				}
			}
			// When a steer arrives, it must not create a native input or run.
			_, err := a.Submit(t.Context(), ref, Input{RequestID: "idle-steer", Content: "do not run", Steer: &TrustedTaskSteer{OriginalRequestID: "original"}})
			if err == nil {
				t.Error("idle steer was admitted")
			}
			conversation, err := a.conversation(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			_, err = conversation.Commit(t.Context(), func(tx durable.Tx) (any, error) {
				record, err := tx.SubmissionByRequest(conversation.Id(), "idle-steer")
				if err != nil {
					return nil, err
				}
				if record != nil {
					t.Errorf("idle steer created native submission: id=%d type=%s status=%s", record.Id, record.Type, record.Status)
				}
				return nil, nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
