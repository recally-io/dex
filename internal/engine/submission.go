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
	"github.com/recally-io/dex/internal/execenv/runner"
	"github.com/recally-io/dex/internal/vault"
)

// Submission joins one durable input and the memory commit that follows its settlement.
// Cancelling Wait does not cancel the admitted execution.
type Submission struct {
	ID           int64
	ActivationID int64
	native       durable.Submission
	done         chan struct{}
	outcome      Outcome
	err          error
}

// Running reports whether native execution has placed this input in an active run.
func (s *Submission) Running(ctx context.Context) (bool, error) {
	record, err := s.native.Status(ctx)
	return record.Status == durable.SubmissionPlaced, err
}

// Queued reports whether native execution still holds this input in its inbox.
func (s *Submission) Queued(ctx context.Context) (bool, error) {
	record, err := s.native.Status(ctx)
	return record.Status == durable.SubmissionQueued, err
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
	if err := a.begin(resourceKey(ref)); err != nil {
		return nil, err
	}
	defer a.end(resourceKey(ref))
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
		var target sessionEnv
		if ref.Scope == "" {
			envs := a.environments(ref.AgentID)
			if envs == nil {
				return nil, ErrClosed
			}
			var envErr error
			target, envErr = envs.session(ctx, ref.ID)
			// Preserve the default's admission order: wait for it before checking
			// the model. This keeps pending Main alerts across restarts too.
			// An explicit selection may report unavailability through its tools,
			// but attachments must wait until they can be staged there.
			// A lease stays valid while its machine is offline, so check the machine here
			// without waiting: the input stays in business storage until the machine is back.
			if waits := target.ID == "" || len(input.Attachments) > 0 || (input.Kind == "task_result" && IsMonitorEvent(input.RequestID)); waits {
				if envErr == nil && envs.status(ctx, target.ID) == "offline" {
					envErr = fmt.Errorf("%w: %w", execenv.ErrUnavailable, runner.ErrOffline)
				}
				if envErr != nil {
					return nil, envErr
				}
			}
		}
		if input.Model.ModelID == "" {
			return nil, ErrNoModel
		}
		if err := a.Catalog.ModelReady(input.Model.Provider); err != nil {
			return nil, err
		}
		var change harness.AgentChange
		if ref.Scope != "" {
			change = a.changeFor(ctx, ref, input.Options)
		} else {
			options := input.Options
			if target.Files != nil {
				options.Instructions = target.Files.instructionsContext(ctx, options.Cwd, options.Instructions)
			}
			change = agentChange(options)
		}
		if ref.Scope == "" {
			if input.Automation {
				change.Extensions = harness.SetTo(harness.ExtensionChange{Edit: &harness.ExtensionEdit{Remove: []*durable.Extension{routineExtension(ref.AgentID, a.config.Routines)}, Add: []*durable.Extension{routineCheckInstructions(), botReadExtension(ref.AgentID, a.config.Bots)}}})
			}
			// Only Main and Chat sessions can ask the user. Extension edits persist on the conversation,
			// so a child, an automation or a dream never gets the tool and needs no removal.
			dream := input.Origin == "dream" || strings.Contains(input.Source, "/dream/")
			asksUser := !input.Automation && !input.Child && !dream
			switch {
			case input.Main:
				add := []*durable.Extension{frontDeskExtension()}
				if asksUser {
					add = append(add, channelExtensions(ref.AgentID, a.config.Channels)...)
					add = append(add, askUserExtension(ref.AgentID, a.config.Tasks.AskUser), outboxExtension(ref.AgentID, a.config.Tasks.Outbox), botReadExtension(ref.AgentID, a.config.Bots), botManageExtension(ref.AgentID, a.config.Bots), workTaskExtension(ref.AgentID, a.config.WorkTasks))
				}
				change.Extensions = harness.SetTo(harness.ExtensionChange{Edit: &harness.ExtensionEdit{Remove: []*durable.Extension{workLoopExtension()}, Add: add}})
			case asksUser:
				add := channelExtensions(ref.AgentID, a.config.Channels)
				add = append(add, askUserExtension(ref.AgentID, a.config.Tasks.AskUser), outboxExtension(ref.AgentID, a.config.Tasks.Outbox), botReadExtension(ref.AgentID, a.config.Bots), botManageExtension(ref.AgentID, a.config.Bots), workTaskExtension(ref.AgentID, a.config.WorkTasks))
				change.Extensions = harness.SetTo(harness.ExtensionChange{Edit: &harness.ExtensionEdit{Add: add}})
			}
			switch {
			case input.Child:
				// Every input of a child repeats this edit, so a user message in a child that was a task
				// before the upgrade loses the same extensions.
				change.Extensions = harness.SetTo(harness.ExtensionChange{Edit: &harness.ExtensionEdit{Remove: []*durable.Extension{sessionExtension(ref.AgentID, a.config.Tasks), workLoopExtension(), frontDeskExtension(), routineExtension(ref.AgentID, a.config.Routines), outboxExtension(ref.AgentID, a.config.Tasks.Outbox)}, Add: []*durable.Extension{childExtension(), childOutboxExtension(ref.AgentID, a.config.Tasks.Outbox)}}})
			case dream:
				change.Extensions = harness.SetTo(harness.ExtensionChange{Edit: &harness.ExtensionEdit{Remove: []*durable.Extension{sessionExtension(ref.AgentID, a.config.Tasks), sessionReadExtension(ref.AgentID, a.config.Tasks), monitorExtension(ref.AgentID, a.config.Tasks.Monitors, nil, vault.Snapshot{}), workLoopExtension(), routineExtension(ref.AgentID, a.config.Routines), artifactExtension(ref.AgentID, nil, a.config.Tasks.DeliverArtifact)}, Add: []*durable.Extension{dreamMessageExtension(ref.AgentID, a.config.Tasks.ReadDreamMessage)}}})
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
		inputDirectory := ""
		if h := target.Handle; h != nil {
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
			if input.Authorize != nil {
				if err := input.Authorize(ctx); err != nil {
					return nil, err
				}
			}
			mode, err := isolatedAdmission(tx, conversation.Id(), input, whenBusy)
			if err != nil {
				return nil, err
			}
			if mode == durable.WhenBusyFollowUp {
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
					// Each bound Routine needs its own source and report, even when
					// different routines use the same busy session.
					if m := metadata(data); m.Origin != input.Origin || (input.Origin == "automation" && !input.Automation) {
						return nil, ErrOriginBusy
					}
				}
			}
			if input.Kind == "task_result" && IsMonitorEvent(input.RequestID) && a.config.Tasks.AuthorizeMonitor != nil {
				c := TaskContext{AgentID: ref.AgentID, Scope: ref.Scope, ConversationID: ref.ID, SourceRequestID: input.RequestID}
				if err := a.config.Tasks.AuthorizeMonitor(ctx, c); err != nil {
					return nil, fmt.Errorf("%w: monitor target or authority is no longer valid", ErrInvalidInput)
				}
			}
			metadata := inputMetadata{Scope: ref.Scope, RequestID: input.RequestID, Content: input.Content, HasWorkTask: input.WorkTaskContext != "", Kind: input.Kind, Origin: input.Origin, Source: input.Source, Attachments: input.Attachments, TaskID: input.TaskID, TaskTitle: input.TaskTitle, Isolation: input.Isolation, ReviewBypass: input.ReviewBypass}
			data, err := durable.ToJsonValue(metadata)
			if err != nil {
				return nil, err
			}
			draft := durable.SubmissionDraft{Type: durable.SubmissionTypeInput, RequestId: &input.RequestID, Content: ai.UserText(inputText(input, inputDirectory)), WhenBusy: mode, InputEntry: &durable.InputEntry{Kind: kind, Data: data}}
			if input.ReviewBypass {
				draft.WhenIdle = durable.WhenIdleReject
			}
			id, err = conversation.SubmitIn(tx, draft)
			return nil, err
		})
		if err != nil {
			if _, ok := errors.AsType[*durable.ConversationIdle](err); ok {
				return nil, ErrReviewBarrier
			}
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
	record := existing
	if record == nil && input.Isolation != "" {
		placed, err := native.Status(ctx)
		if err != nil {
			return nil, err
		}
		record = &placed
	}
	var activationID int64
	if record != nil && record.Entry != nil {
		_, err := conversation.Commit(ctx, func(tx durable.Tx) (any, error) {
			entry, err := tx.Entry(*record.Entry)
			activationID = activationOf(*record, entry)
			return nil, err
		})
		if err != nil {
			return nil, err
		}
	}
	return a.settleSubmission(ctx, ref, conversation, input, native, activationID), nil
}

func (a *Agents) settleSubmission(ctx context.Context, ref Ref, conversation harness.Conversation, input Input, native durable.Submission, activationID int64) *Submission {
	var err error
	result := &Submission{ID: int64(native.Id()), ActivationID: activationID, native: native, done: make(chan struct{})}
	var stopActivity func()
	if input.Child {
		stopActivity, err = a.startActivityWatch(ctx, ref, conversation, input)
		if err != nil {
			slog.Error("track child activity", "agent", ref.AgentID, "conversation", ref.ID, "error", err)
		}
	}
	settlementCtx := context.WithoutCancel(ctx)
	a.settle(resourceKey(ref), func() {
		defer close(result.done)
		if stopActivity != nil {
			defer stopActivity()
		}
		settled, err := native.Wait(settlementCtx)
		if err != nil {
			result.err = err
		} else {
			result.outcome = Outcome{Status: string(settled.Status), ActivationID: activationID}
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
		// A failed or aborted execution may have changed host files too.
		if ref.Scope == "" {
			if _, err := a.memory.Commit(settlementCtx, "Update memory from "+input.Source); err != nil {
				slog.Error("commit agent memory", "agent", ref.AgentID, "error", err)
			}
			if err := a.commitSkills(settlementCtx, ref.AgentID, input.Source); err != nil {
				slog.Error("commit agent skills", "agent", ref.AgentID, "error", err)
			}
		}
	})
	return result
}

func inputText(input Input, workspace string) string {
	content := input.Content
	// Keep the owner transcript and durable source identity separate from model guidance.
	if input.Scope == "" && input.Origin == "automation" && input.Kind == "message" && !input.Automation {
		content += boundRoutineCheckSuffix
	}
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
