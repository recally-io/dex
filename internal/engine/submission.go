package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

// Submission joins one durable input and the memory commit that follows its settlement.
// Cancelling Wait does not cancel the admitted execution.
type Submission struct {
	ID      int64
	done    chan struct{}
	outcome Outcome
	err     error
}

func (s *Submission) Wait(ctx context.Context) (Outcome, error) {
	select {
	case <-ctx.Done():
		return Outcome{}, ctx.Err()
	case <-s.done:
		return s.outcome, s.err
	}
}

func (a *Agents) Submit(ctx context.Context, ref Ref, input Input) (*Submission, error) {
	if input.RequestID == "" {
		return nil, fmt.Errorf("%w: request ID is required", ErrInvalidInput)
	}
	if input.Steer != nil && input.Steer.OriginalRequestID == "" {
		return nil, fmt.Errorf("%w: original task request ID is required", ErrInvalidInput)
	}
	if input.Kind != "" && input.Kind != "message" && input.Kind != "task_result" {
		return nil, fmt.Errorf("%w: input kind", ErrInvalidInput)
	}
	if err := a.begin(); err != nil {
		return nil, err
	}
	defer a.end()
	unlock, err := a.lock(ctx, ref)
	if err != nil {
		return nil, err
	}
	defer unlock()
	conversation, err := a.conversation(ctx, ref)
	if err != nil {
		return nil, err
	}
	if input.Steer != nil {
		native, err := a.admitTaskSteer(ctx, ref, conversation, input)
		if err != nil {
			return nil, err
		}
		return a.settleSubmission(ctx, ref, conversation, input, native), nil
	}
	// Replay must not change settings or reclassify an existing execution.
	var existing *durable.SubmissionRecord
	_, err = conversation.Commit(ctx, func(tx durable.Tx) (any, error) {
		var err error
		existing, err = tx.SubmissionByRequest(conversation.Id(), input.RequestID)
		if err != nil {
			return nil, err
		}
		if existing == nil {
			_, binding, err := bindingDraft(tx, conversation.Id())
			if err != nil {
				return nil, err
			}
			if binding.Sealed && (input.Background || !input.TaskContinuation) {
				return nil, fmt.Errorf("%w: task sealed", ErrNotFound)
			}
			if input.Background && binding.Root != "" {
				return nil, fmt.Errorf("%w: task already bound", ErrInvalidInput)
			}
		}
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	var native durable.Submission
	if record := existing; record != nil {
		_, err := conversation.Commit(ctx, func(tx durable.Tx) (any, error) {
			_, b, err := bindingDraft(tx, conversation.Id())
			if err != nil {
				return nil, err
			}
			mapped, bound := b.Inputs[input.RequestID]
			// Legacy inputs have no binding. Control's task record supplies their background role.
			if input.Background && (record.Type != durable.SubmissionTypeInput || (b.Root != "" && (b.Root != input.RequestID || !bound || mapped.ID != record.Id || mapped.Kind != inputEntryKind(input)))) {
				return nil, fmt.Errorf("%w: request is not task root", ErrInvalidInput)
			}
			if !input.Background && bound {
				return nil, fmt.Errorf("%w: request belongs to task", ErrInvalidInput)
			}
			return nil, nil
		})
		if err != nil {
			return nil, err
		}
		h, err := a.harness(ctx, ref.AgentID)
		if err != nil {
			return nil, err
		}
		native, err = h.Submission(ctx, record.Id)
		if err != nil {
			return nil, err
		}
	} else {
		if input.Model.ModelID == "" {
			return nil, ErrNoModel
		}
		change := agentChange(input.Options)
		if input.Automation {
			change.Extensions = harness.SetTo(harness.ExtensionChange{Edit: &harness.ExtensionEdit{Remove: []*durable.Extension{routineExtension(ref.AgentID, a.config.Routines)}, Add: []*durable.Extension{checkExtension(ref.AgentID, a.config.Tasks.ReportCheck)}}})
		}
		// Only Main and Chat sessions can ask the user. Extension edits persist on the conversation,
		// so a task, an automation or a dream never gets the tool and needs no removal.
		dream := input.Origin == "dream" || strings.Contains(input.Source, "/dream/")
		asksUser := !input.Automation && !input.Background && !input.TaskContinuation && !dream
		switch {
		case input.Main:
			add := []*durable.Extension{frontDeskExtension()}
			if asksUser {
				add = append(add, askUserExtension(ref.AgentID, a.config.Tasks.AskUser))
			}
			change.Extensions = harness.SetTo(harness.ExtensionChange{Edit: &harness.ExtensionEdit{Remove: []*durable.Extension{workLoopExtension()}, Add: add}})
		case asksUser:
			change.Extensions = harness.SetTo(harness.ExtensionChange{Edit: &harness.ExtensionEdit{Add: []*durable.Extension{askUserExtension(ref.AgentID, a.config.Tasks.AskUser)}}})
		}
		if input.TaskContinuation {
			change.Extensions = continuationExtensions(ref.AgentID, a.config)
		}
		if input.Background {
			change.Extensions = harness.SetTo(harness.ExtensionChange{Edit: &harness.ExtensionEdit{Remove: []*durable.Extension{taskExtension(ref.AgentID, a.config.Tasks), workLoopExtension(), routineExtension(ref.AgentID, a.config.Routines)}, Add: []*durable.Extension{taskReportExtension(ref.AgentID, a.config.Tasks.ReportTask)}}})
		} else if dream {
			change.Extensions = harness.SetTo(harness.ExtensionChange{Edit: &harness.ExtensionEdit{Remove: []*durable.Extension{taskExtension(ref.AgentID, a.config.Tasks), workLoopExtension(), routineExtension(ref.AgentID, a.config.Routines), artifactExtension(ref.AgentID, filepath.Join(a.config.DataDir, "agents", ref.AgentID, "workspace"), a.config.Tasks.DeliverArtifact)}, Add: []*durable.Extension{dreamMessageExtension(ref.AgentID, a.config.Tasks.ReadDreamMessage)}}})
		}
		if err := conversation.Configure(ctx, change); err != nil {
			return nil, err
		}
		kind := inputEntryKind(input)
		metadata := InputMetadata{RequestID: input.RequestID, Content: input.Content, Kind: input.Kind, Origin: input.Origin, Source: input.Source, Attachments: input.Attachments, TaskID: input.TaskID, TaskTitle: input.TaskTitle}
		data, err := durable.ToJsonValue(metadata)
		if err != nil {
			return nil, err
		}
		draft := durable.SubmissionDraft{Type: durable.SubmissionTypeInput, RequestId: &input.RequestID, Content: ai.UserText(inputText(input, filepath.Join(a.config.DataDir, "agents", ref.AgentID, "workspace"))), WhenBusy: durable.WhenBusy(cmp.Or(input.WhenBusy, string(durable.WhenBusyFollowUp))), InputEntry: &durable.InputEntry{Kind: kind, Data: data}}
		if input.Background {
			var id durable.SubmissionId
			_, err = conversation.Commit(ctx, func(tx durable.Tx) (any, error) {
				doc, binding, err := bindingDraft(tx, conversation.Id())
				if err != nil {
					return nil, err
				}
				if binding.Root != "" {
					return nil, fmt.Errorf("%w: task already bound", ErrInvalidInput)
				}
				existing, err := tx.SubmissionByRequest(conversation.Id(), input.RequestID)
				if err != nil {
					return nil, err
				}
				if existing != nil {
					return nil, fmt.Errorf("%w: request already exists", ErrInvalidInput)
				}
				admitted, err := harness.AdmitSubmission(tx, conversation.Id(), draft, float64(time.Now().UnixMilli()), taskQueueModes, nil)
				if err != nil {
					return nil, err
				}
				binding.Root = input.RequestID
				binding.Inputs[input.RequestID] = taskBoundInput{ID: admitted.Id, Kind: kind}
				if err := saveBinding(doc, binding); err != nil {
					return nil, err
				}
				id = admitted.Id
				return nil, nil
			})
			if err == nil {
				var h harness.Harness
				h, err = a.harness(ctx, ref.AgentID)
				if err == nil {
					native, err = h.Submission(ctx, id)
				}
			}
		} else {
			native, err = conversation.Submit(ctx, draft)
		}
		if err != nil {
			if _, ok := errors.AsType[*durable.ConversationBusy](err); ok {
				return nil, errors.Join(ErrBusy, err)
			}
			return nil, err
		}
	}
	return a.settleSubmission(ctx, ref, conversation, input, native), nil
}

func (a *Agents) settleSubmission(ctx context.Context, ref Ref, conversation harness.Conversation, input Input, native durable.Submission) *Submission {
	var err error
	result := &Submission{ID: int64(native.Id()), done: make(chan struct{})}
	var stopActivity func()
	if input.Background && input.Steer == nil {
		stopActivity, err = a.startActivityWatch(ctx, ref, conversation)
		if err != nil {
			slog.Error("track background activity", "agent", ref.AgentID, "conversation", ref.ID, "error", err)
		}
	}
	a.mu.Lock()
	a.settling++
	a.mu.Unlock()
	settlementCtx := context.WithoutCancel(ctx)
	a.settlements.Go(func() {
		defer func() { a.mu.Lock(); a.settling--; a.mu.Unlock() }()
		defer close(result.done)
		if stopActivity != nil {
			defer stopActivity()
		}
		settled, err := native.Wait(settlementCtx)
		if err != nil {
			result.err = err
		} else {
			result.outcome = Outcome{Status: string(settled.Status)}
			if settled.Reason != nil {
				result.outcome.Reason = *settled.Reason
			}
			if settled.Status == durable.SubmissionDone && settled.Answer != nil {
				var entry *durable.EntryRecord
				_, err := conversation.Commit(settlementCtx, func(tx durable.Tx) (any, error) {
					var err error
					entry, err = tx.Entry(*settled.Answer)
					return nil, err
				})
				if err != nil {
					result.err = err
				} else if entry != nil {
					result.outcome.Answer = answerText(entry.Model)
				}
			}
		}
		// A failed or aborted execution may have changed memory too.
		if _, err := a.memory.Commit(settlementCtx, "Update memory from "+input.Source); err != nil {
			slog.Error("commit agent memory", "agent", ref.AgentID, "error", err)
		}
	})
	return result
}

var taskQueueModes = harness.QueueModes{SteeringMode: durable.QueueAll, FollowUpMode: durable.QueueOneAtATime}

// The control receipt is responsible for request ID/content uniqueness.
func (a *Agents) admitTaskSteer(ctx context.Context, ref Ref, conversation harness.Conversation, input Input) (durable.Submission, error) {
	metadata := InputMetadata{RequestID: input.RequestID, Content: input.Content, Kind: input.Kind, Origin: input.Origin, Source: input.Source, Attachments: input.Attachments, TaskID: input.TaskID, TaskTitle: input.TaskTitle}
	data, err := durable.ToJsonValue(metadata)
	if err != nil {
		return nil, err
	}
	kind := inputEntryKind(input)
	draft := durable.SubmissionDraft{Type: durable.SubmissionTypeInput, RequestId: &input.RequestID, Content: ai.UserText(inputText(input, filepath.Join(a.config.DataDir, "agents", ref.AgentID, "workspace"))), WhenBusy: durable.WhenBusySteer, InputEntry: &durable.InputEntry{Kind: kind, Data: data}}
	var id durable.SubmissionId
	_, err = conversation.Commit(ctx, func(tx durable.Tx) (any, error) {
		doc, binding, err := bindingDraft(tx, conversation.Id())
		if err != nil {
			return nil, err
		}
		if binding.Root != input.Steer.OriginalRequestID {
			return nil, fmt.Errorf("%w: task binding", ErrNotFound)
		}
		existing, err := tx.SubmissionByRequest(conversation.Id(), input.RequestID)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			mapped, ok := binding.Inputs[input.RequestID]
			if !ok || (mapped.ID != existing.Id || mapped.Kind != kind) || input.RequestID == binding.Root || existing.Type != durable.SubmissionTypeInput {
				return nil, fmt.Errorf("%w: request belongs to another input", ErrInvalidInput)
			}
			id = existing.Id
			return nil, nil
		}
		if binding.Sealed {
			return nil, fmt.Errorf("%w: task sealed", ErrNotFound)
		}
		liveDoc, err := tx.Doc(harness.LiveDoc, conversation.Id())
		if err != nil {
			return nil, err
		}
		live, err := durable.FromJsonValue[harness.LiveState](liveDoc.Snapshot())
		if err != nil {
			return nil, err
		}
		if live.Run != nil {
			belongs := false
			for _, id := range live.Run.Inputs {
				for _, bound := range binding.Inputs {
					if id == bound.ID {
						belongs = true
						break
					}
				}
			}
			if !belongs {
				return nil, fmt.Errorf("%w: task run mismatch", ErrNotFound)
			}
			task, err := tx.Task(live.Run.TaskId)
			if err != nil {
				return nil, err
			}
			if task == nil || task.AbortRequested {
				return nil, fmt.Errorf("%w: task run aborting", ErrNotFound)
			}
		}
		admission, err := harness.AdmitSubmission(tx, conversation.Id(), draft, float64(time.Now().UnixMilli()), taskQueueModes, nil)
		if err != nil {
			return nil, err
		}
		binding.Inputs[input.RequestID] = taskBoundInput{ID: admission.Id, Kind: kind}
		if err := saveBinding(doc, binding); err != nil {
			return nil, err
		}
		id = admission.Id
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	h, err := a.harness(ctx, ref.AgentID)
	if err != nil {
		return nil, err
	}
	native, err := h.Submission(ctx, id)
	if err != nil {
		return nil, err
	}
	if native == nil {
		return nil, fmt.Errorf("native submission %d missing after admission", id)
	}
	return native, nil
}

func inputText(input Input, workspace string) string {
	if len(input.Attachments) == 0 {
		return input.Content
	}
	var text strings.Builder
	if input.Content != "" {
		text.WriteString(input.Content)
		text.WriteString("\n\n")
	}
	text.WriteString("Attached files:")
	for _, attachment := range input.Attachments {
		text.WriteString("\n- ")
		// Uploads stay in the workspace. An absolute path keeps working after set_project changes
		// the cwd, also later in the same run.
		text.WriteString(filepath.Join(workspace, attachment.Path))
	}
	return text.String()
}

func answerText(messages []ai.Message) string {
	var text strings.Builder
	for _, message := range messages {
		if assistant, ok := message.(ai.AssistantMessage); ok {
			for _, block := range assistant.Content {
				if block, ok := block.(ai.TextContent); ok {
					text.WriteString(block.Text)
				}
			}
		}
	}
	return text.String()
}

func inputEntryKind(input Input) string {
	if input.Kind == "task_result" {
		return "dex.task_result"
	}
	return "dex.user"
}
