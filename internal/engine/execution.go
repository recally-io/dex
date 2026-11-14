package engine

import (
	"context"

	"github.com/MichaelKinsy/PiG/chord/delta"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

// Execution is a content-free projection of native run and submission state.
// A queued submission is already admitted. It can coexist with an active run.
type Execution struct {
	Phase  string          `json:"phase"`
	Queued int             `json:"queued"`
	Turns  []ExecutionTurn `json:"turns"`
}

type ExecutionTurn struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

func executionPhase(live harness.LiveState) string {
	if live.Run == nil {
		return "idle"
	}
	if g := live.Generation; g != nil {
		if g.Retry != nil {
			return "retry"
		}
		if g.Deferred != nil {
			return "deferred"
		}
		// Generation exists from the request phase, even before the first content event.
		return "model"
	}
	for _, tool := range live.Tools {
		if tool.Status == harness.ToolSlotRunning {
			return "tool"
		}
	}
	for _, c := range live.Compactions {
		if c.Blocking {
			return "compaction"
		}
	}
	if live.Run.AfterRun != nil {
		return "finishing"
	}
	// Run ownership can precede scheduling. Do not infer a model request from missing tools.
	return "running"
}

// executionMetadata reads only run control. Decoding the full live document here would serialize
// partial model content on the Session line, delaying the execution this view observes.
func executionMetadata(live *delta.Object) harness.LiveState {
	state := harness.LiveState{}
	run := live.Object("run")
	if run == nil {
		return state
	}
	state.Run = &harness.LiveRun{}
	if run.Object("afterRun") != nil {
		state.Run.AfterRun = &harness.LiveRunAfterRun{}
	}
	if generation := live.Object("generation"); generation != nil {
		state.Generation = &harness.LiveGeneration{}
		if generation.Object("retry") != nil {
			state.Generation.Retry = &harness.RetryStatus{}
		}
		if generation.Object("deferred") != nil {
			state.Generation.Deferred = &harness.DeferredStatus{}
		}
	}
	if tools := live.Array("tools"); tools != nil {
		for i := range tools.Len() {
			if tools.Object(i).Get("status") == string(harness.ToolSlotRunning) {
				state.Tools = []harness.ToolSlot{{Status: harness.ToolSlotRunning}}
				break
			}
		}
	}
	if compactions := live.Array("compactions"); compactions != nil {
		for i := range compactions.Len() {
			if compactions.Object(i).Get("blocking") == true {
				state.Compactions = []harness.CompactionStatus{{Blocking: true}}
				break
			}
		}
	}
	return state
}

// Execution reads native records in one commit. It does not wait for a model or read answer text.
func (a *Agents) Execution(ctx context.Context, ref Ref, requests []string) (Execution, error) {
	out := Execution{Phase: "idle", Turns: []ExecutionTurn{}}
	if err := a.begin(); err != nil {
		return out, err
	}
	defer a.end()
	a.mu.Lock()
	resource := a.resources[ref.AgentID]
	a.mu.Unlock()
	// Status reads must not open a cold agent or wait for its MCP connections.
	if resource == nil {
		out.Phase = "unavailable"
		for _, id := range requests {
			out.Turns = append(out.Turns, ExecutionTurn{ID: id, Status: "unavailable"})
		}
		return out, nil
	}
	conversation, err := resource.harness.Conversation(ctx, durable.ConversationId(ref.ID))
	if err != nil {
		return out, err
	}
	if conversation == nil {
		return out, ErrNotFound
	}
	_, err = conversation.Commit(ctx, func(tx durable.Tx) (any, error) {
		liveDoc, err := durable.TxDoc(tx, harness.LiveDoc, conversation.Id())
		if err != nil {
			return nil, err
		}
		inboxDoc, err := durable.TxDoc(tx, harness.InboxDoc, conversation.Id())
		if err != nil {
			return nil, err
		}
		out.Phase = executionPhase(executionMetadata(liveDoc))
		if items := inboxDoc.Array("items"); items != nil {
			out.Queued = items.Len()
		}
		for _, id := range requests {
			record, err := tx.SubmissionByRequest(conversation.Id(), id)
			if err != nil {
				return nil, err
			}
			turn := ExecutionTurn{ID: id, Status: "waiting"}
			if record != nil {
				switch record.Status {
				case durable.SubmissionQueued:
					turn.Status = "queued"
				case durable.SubmissionPlaced:
					turn.Status = "running"
				case durable.SubmissionDone:
					turn.Status = "done"
				case durable.SubmissionUnanswered:
					turn.Status = "failed"
					if record.Reason != nil {
						turn.Reason = *record.Reason
					}
				}
			}
			out.Turns = append(out.Turns, turn)
		}
		return nil, nil
	})
	return out, err
}
