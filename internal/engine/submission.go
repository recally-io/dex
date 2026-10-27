package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

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
	// Replay must not change settings or reclassify an existing execution.
	var existing *durable.SubmissionRecord
	_, err = conversation.Commit(ctx, func(tx durable.Tx) (any, error) {
		var err error
		existing, err = tx.SubmissionByRequest(conversation.Id(), input.RequestID)
		return nil, err
	})
	if err != nil {
		return nil, err
	}
	var native durable.Submission
	if record := existing; record != nil {
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
		if input.Background {
			change.Extensions = harness.SetTo(harness.ExtensionChange{Edit: &harness.ExtensionEdit{Remove: []*durable.Extension{taskExtension(ref.AgentID, a.config.Tasks), routineExtension(ref.AgentID, a.config.Routines)}, Add: []*durable.Extension{taskReportExtension(ref.AgentID, a.config.Tasks.ReportTask)}}})
		} else if input.Origin == "dream" || strings.Contains(input.Source, "/dream/") {
			change.Extensions = harness.SetTo(harness.ExtensionChange{Edit: &harness.ExtensionEdit{Remove: []*durable.Extension{taskExtension(ref.AgentID, a.config.Tasks), routineExtension(ref.AgentID, a.config.Routines)}}})
		}
		if err := conversation.Configure(ctx, change); err != nil {
			return nil, err
		}
		kind := "dex.user"
		if input.Kind == "task_result" {
			kind = "dex.task_result"
		}
		metadata := InputMetadata{RequestID: input.RequestID, Content: input.Content, Kind: input.Kind, Origin: input.Origin, Source: input.Source, Attachments: input.Attachments, TaskID: input.TaskID, TaskTitle: input.TaskTitle}
		data, err := durable.ToJsonValue(metadata)
		if err != nil {
			return nil, err
		}
		native, err = conversation.Submit(ctx, durable.SubmissionDraft{Type: durable.SubmissionTypeInput, RequestId: &input.RequestID, Content: ai.UserText(inputText(input, filepath.Join(a.config.DataDir, "agents", ref.AgentID, "workspace"))), WhenBusy: durable.WhenBusyFollowUp, InputEntry: &durable.InputEntry{Kind: kind, Data: data}})
		if err != nil {
			if _, ok := errors.AsType[*durable.ConversationBusy](err); ok {
				return nil, errors.Join(ErrBusy, err)
			}
			return nil, err
		}
	}
	result := &Submission{ID: int64(native.Id()), done: make(chan struct{})}
	a.mu.Lock()
	a.settling++
	a.mu.Unlock()
	settlementCtx := context.WithoutCancel(ctx)
	a.settlements.Go(func() {
		defer func() { a.mu.Lock(); a.settling--; a.mu.Unlock() }()
		defer close(result.done)
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
	return result, nil
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
