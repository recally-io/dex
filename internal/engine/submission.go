package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
	"github.com/recally-io/dex/internal/execenv"
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
		h, err := a.harness(ctx, ref.AgentID, ref.Scope)
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
		change := a.changeFor(ctx, ref, input.Options)
		if ref.Scope == "" {
			if input.Automation {
				change.Extensions = harness.SetTo(harness.ExtensionChange{Edit: &harness.ExtensionEdit{Remove: []*durable.Extension{routineExtension(ref.AgentID, a.config.Routines), backgroundExtension(ref.AgentID, a.config.Tasks.Background, nil)}, Add: []*durable.Extension{checkExtension(ref.AgentID, a.config.Tasks.ReportCheck), botReadExtension(ref.AgentID, a.config.Bots)}}})
			}
			// Only Main and Chat sessions can ask the user. Extension edits persist on the conversation,
			// so a child, an automation or a dream never gets the tool and needs no removal.
			dream := input.Origin == "dream" || strings.Contains(input.Source, "/dream/")
			asksUser := !input.Automation && !input.Child && !dream
			switch {
			case input.Main:
				add := []*durable.Extension{frontDeskExtension()}
				if asksUser {
					add = append(add, askUserExtension(ref.AgentID, a.config.Tasks.AskUser), outboxExtension(ref.AgentID, a.config.Tasks.Outbox), timerExtension(ref.AgentID, a.config.Tasks.Timers), botReadExtension(ref.AgentID, a.config.Bots), botManageExtension(ref.AgentID, a.config.Bots))
				}
				change.Extensions = harness.SetTo(harness.ExtensionChange{Edit: &harness.ExtensionEdit{Remove: []*durable.Extension{workLoopExtension()}, Add: add}})
			case asksUser:
				change.Extensions = harness.SetTo(harness.ExtensionChange{Edit: &harness.ExtensionEdit{Add: []*durable.Extension{askUserExtension(ref.AgentID, a.config.Tasks.AskUser), outboxExtension(ref.AgentID, a.config.Tasks.Outbox), timerExtension(ref.AgentID, a.config.Tasks.Timers), botReadExtension(ref.AgentID, a.config.Bots), botManageExtension(ref.AgentID, a.config.Bots)}}})
			}
			switch {
			case input.Child:
				// Every input of a child repeats this edit, so a user message in a child that was a task
				// before the upgrade loses the same extensions.
				change.Extensions = harness.SetTo(harness.ExtensionChange{Edit: &harness.ExtensionEdit{Remove: []*durable.Extension{sessionExtension(ref.AgentID, a.config.Tasks), workLoopExtension(), frontDeskExtension(), routineExtension(ref.AgentID, a.config.Routines), outboxExtension(ref.AgentID, a.config.Tasks.Outbox)}, Add: []*durable.Extension{childExtension(), childOutboxExtension(ref.AgentID, a.config.Tasks.Outbox)}}})
			case dream:
				change.Extensions = harness.SetTo(harness.ExtensionChange{Edit: &harness.ExtensionEdit{Remove: []*durable.Extension{sessionExtension(ref.AgentID, a.config.Tasks), sessionReadExtension(ref.AgentID, a.config.Tasks), backgroundExtension(ref.AgentID, a.config.Tasks.Background, nil), workLoopExtension(), routineExtension(ref.AgentID, a.config.Routines), artifactExtension(ref.AgentID, nil, a.config.Tasks.DeliverArtifact)}, Add: []*durable.Extension{dreamMessageExtension(ref.AgentID, a.config.Tasks.ReadDreamMessage)}}})
			}
		}
		if err := a.groupAttachments(ref, input.Attachments); err != nil {
			return nil, err
		}
		if err := conversation.Configure(ctx, change); err != nil {
			return nil, err
		}
		kind := inputEntryKind(input)
		whenBusy := durable.WhenBusy(cmp.Or(input.WhenBusy, string(durable.WhenBusyFollowUp)))
		var id durable.SubmissionId
		inputDirectory := a.inputDirectory(ref)
		if ref.Scope != "" {
			inputDirectory = ""
		} else if envs := a.environments(ref.AgentID); envs != nil {
			// An unavailable environment fails only an input with attachments: its tool calls report
			// the reason, and the chat stays usable.
			s, envErr := envs.session(ctx, ref.ID)
			if envErr != nil && len(input.Attachments) > 0 {
				return nil, envErr
			}
			h := s.Handle
			if h == nil {
				h = a.execHandle(ref.AgentID)
			}
			inputDirectory = h.Workspace()
			if h.Kind() != execenv.KindLocal && len(input.Attachments) > 0 {
				remoteEnv, envErr := h.Env(ctx, inputDirectory, nil)
				if envErr != nil {
					return nil, envErr
				}
				localWorkspace := filepath.Join(a.config.DataDir, "agents", ref.AgentID, "workspace")
				for _, attachment := range input.Attachments {
					if stageErr := execenv.StageUpload(ctx, remoteEnv, localWorkspace, inputDirectory, attachment.Path); stageErr != nil {
						return nil, stageErr
					}
				}
			}
		}
		_, err := conversation.Commit(ctx, func(tx durable.Tx) (any, error) {
			if whenBusy == durable.WhenBusyFollowUp {
				inbox, err := durable.TxDoc[harness.InboxState](tx, harness.InboxDoc, conversation.Id())
				if err != nil {
					return nil, err
				}
				queue, err := durable.DecodeDoc[harness.InboxState](inbox.Snapshot())
				if err != nil {
					return nil, err
				}
				for _, item := range queue.Items {
					if item.Mode != harness.InboxFollowUp || item.Entry == nil {
						continue
					}
					data, _ := item.Entry.Get("data")
					if m := metadata(data); m.Origin != input.Origin {
						return nil, ErrOriginBusy
					}
				}
			}
			metadata := inputMetadata{Scope: ref.Scope, RequestID: input.RequestID, Content: input.Content, HasWorkTask: input.WorkTaskContext != "", Kind: input.Kind, Origin: input.Origin, Source: input.Source, Attachments: input.Attachments, TaskID: input.TaskID, TaskTitle: input.TaskTitle}
			data, err := durable.ToJsonValue(metadata)
			if err != nil {
				return nil, err
			}
			draft := durable.SubmissionDraft{Type: durable.SubmissionTypeInput, RequestId: &input.RequestID, Content: ai.UserText(inputText(input, inputDirectory)), WhenBusy: whenBusy, InputEntry: &durable.InputEntry{Kind: kind, Data: data}}
			id, err = conversation.SubmitIn(tx, draft)
			return nil, err
		})
		if err != nil {
			if _, ok := errors.AsType[*durable.ConversationBusy](err); ok {
				return nil, errors.Join(ErrBusy, err)
			}
			return nil, err
		}
		h, err := a.harness(ctx, ref.AgentID, ref.Scope)
		if err != nil {
			return nil, err
		}
		if native, err = h.Submission(ctx, id); err != nil {
			return nil, err
		}
	}
	return a.settleSubmission(ctx, ref, conversation, input, native), nil
}

func (a *Agents) settleSubmission(ctx context.Context, ref Ref, conversation harness.Conversation, input Input, native durable.Submission) *Submission {
	var err error
	result := &Submission{ID: int64(native.Id()), done: make(chan struct{})}
	var stopActivity func()
	if input.Child {
		stopActivity, err = a.startActivityWatch(ctx, ref, conversation, input)
		if err != nil {
			slog.Error("track child activity", "agent", ref.AgentID, "conversation", ref.ID, "error", err)
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
			if settled.Status == durable.SubmissionDone && settled.Answer == nil {
				result.err = errors.New("completed native input has no answer")
			} else if settled.Status == durable.SubmissionDone {
				result.outcome.AnswerID = int64(*settled.Answer)
				var entry *durable.EntryRecord
				_, err := conversation.Commit(settlementCtx, func(tx durable.Tx) (any, error) {
					var err error
					entry, err = tx.Entry(*settled.Answer)
					return nil, err
				})
				switch {
				case err != nil:
					result.err = err
				case entry == nil:
					result.err = errors.New("completed native input has no answer entry")
				default:
					result.outcome.Answer = answerText(entry.Model)
				}
			}
		}
		// A failed or aborted execution may have changed memory too.
		if ref.Scope == "" {
			if _, err := a.memory.Commit(settlementCtx, "Update memory from "+input.Source); err != nil {
				slog.Error("commit agent memory", "agent", ref.AgentID, "error", err)
			}
		}
	})
	return result
}

func inputText(input Input, workspace string) string {
	content := input.Content
	if input.WorkTaskContext != "" {
		content = "Dex Task: " + input.WorkTaskContext + "\n\n" + content
	}
	if len(input.Attachments) == 0 {
		return content
	}
	var text strings.Builder
	if content != "" {
		text.WriteString(content)
		text.WriteString("\n\n")
	}
	text.WriteString("Attached files:")
	for _, attachment := range input.Attachments {
		text.WriteString("\n- ")
		// Uploads stay in the workspace. An absolute path keeps working after set_project changes
		// the cwd, also later in the same run. Group inputs use relative resource paths instead.
		text.WriteString(execenv.JoinTargetPath(workspace, filepath.ToSlash(attachment.Path)))
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
