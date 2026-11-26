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
	"github.com/recally-io/dex/internal/runner"
	"github.com/recally-io/dex/internal/runner/outbound"
)

// Submission joins one durable input and the memory commit that follows its settlement.
// Cancelling Wait does not cancel the admitted execution.
type Submission struct {
	ID      int64
	native  durable.Submission
	done    chan struct{}
	outcome Outcome
	err     error
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
	switch input.Kind {
	case "", "message", "task_result", "task_wake":
	default:
		return nil, fmt.Errorf("%w: input kind", ErrInvalidInput)
	}
	if err := a.begin(ref.AgentID); err != nil {
		return nil, err
	}
	defer a.end(ref.AgentID)
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
		var target sessionEnv
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
		if waits := target.ID == "" || len(input.Attachments) > 0 || input.RequireRunner; waits {
			if envErr == nil && envs.status(ctx, target.ID) == "offline" {
				envErr = fmt.Errorf("%w: %w", runner.ErrUnavailable, outbound.ErrOffline)
			}
			if envErr != nil {
				return nil, envErr
			}
		}
		if input.Model.ModelID == "" {
			return nil, ErrNoModel
		}
		if err := a.Catalog.ModelReady(input.Model.Provider); err != nil {
			return nil, err
		}
		options := input.Options
		if target.Files != nil {
			options.Instructions = target.Files.instructionsContext(ctx, options.Cwd, options.Instructions)
		}
		change := agentChange(options)
		if a.config.InputExtensions != nil {
			edit := a.config.InputExtensions(input)
			if len(edit.Add) != 0 || len(edit.Remove) != 0 {
				change.Extensions = harness.SetTo(harness.ExtensionChange{Edit: edit.native()})
			}
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
			if h.Kind() != runner.KindLocal && len(input.Attachments) > 0 {
				remoteEnv, envErr := h.Env(ctx, inputDirectory, nil)
				if envErr != nil {
					return nil, envErr
				}
				localWorkspace := filepath.Join(a.config.DataDir, "agents", ref.AgentID, "workspace")
				for _, attachment := range input.Attachments {
					if stageErr := runner.StageUpload(ctx, remoteEnv, localWorkspace, inputDirectory, attachment.Path); stageErr != nil {
						return nil, stageErr
					}
				}
			}
		}
		_, err := conversation.Commit(ctx, func(tx durable.Tx) (any, error) {
			if input.BeforeAdmission != nil {
				if err := input.BeforeAdmission(ctx); err != nil {
					return nil, err
				}
			}
			if whenBusy == durable.WhenBusyFollowUp && input.Kind != "task_wake" {
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
					m := metadata(data)
					// Task wakes have no reply destination of their own.
					if m.Kind == "task_wake" {
						continue
					}
					// Each bound Routine needs its own source and report, even when
					// different routines use the same busy session. Rejecting foreign
					// follow-up origins also preserves the QueueAll routing invariant
					// that inputs from different channels never merge into one run.
					if m.Origin != input.Origin || (input.Origin == "automation" && !input.Automation) {
						return nil, ErrOriginBusy
					}
				}
			}
			if input.ValidateAdmission != nil {
				if err := input.ValidateAdmission(ctx, ref); err != nil {
					return nil, err
				}
			}
			if input.RefreshContent != nil {
				if whenBusy == durable.WhenBusyReject {
					// Match SubmitIn's busy check in the same transaction. A rejected
					// input must not freeze a snapshot in control storage.
					live, err := durable.TxDoc[harness.LiveState](tx, harness.LiveDoc, conversation.Id())
					if err != nil {
						return nil, err
					}
					if live.Has("run") {
						return nil, durable.NewConversationBusy(conversation.Id())
					}
				}
				content, err := input.RefreshContent(ctx)
				if err != nil {
					return nil, err
				}
				input.Content = content
			}
			metadata := inputMetadata{RequestID: input.RequestID, Content: input.Content, HasTask: input.TaskContext != "", Kind: input.Kind, Origin: input.Origin, Source: input.Source, Attachments: input.Attachments, TaskID: input.TaskID, TaskTitle: input.TaskTitle}
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
		h, err := a.harness(ctx, ref.AgentID)
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
	result := &Submission{ID: int64(native.Id()), native: native, done: make(chan struct{})}
	var stopActivity func()
	if input.Child {
		stopActivity, err = a.startActivityWatch(ctx, ref, conversation, input)
		if err != nil {
			slog.Error("track child activity", "agent", ref.AgentID, "conversation", ref.ID, "error", err)
		}
	}
	settlementCtx := context.WithoutCancel(ctx)
	a.settle(ref.AgentID, func() {
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
				var runInputs []string
				_, err := conversation.Commit(settlementCtx, func(tx durable.Tx) (any, error) {
					var err error
					entry, err = tx.Entry(*settled.Answer)
					if err != nil || entry == nil {
						return nil, err
					}
					var scanErr error
					runInputs, scanErr = runInputIDsForAnswer(tx, conversation.Id(), *settled.Answer)
					if scanErr != nil {
						slog.Warn("resolve run input membership", "agent", ref.AgentID, "conversation", ref.ID, "answer_id", *settled.Answer, "error", scanErr)
						runInputs = nil
					}
					return nil, nil
				})
				switch {
				case err != nil:
					result.err = err
				case entry == nil:
					result.err = errors.New("completed native input has no answer entry")
				default:
					result.outcome.Answer = answerText(entry.Model)
					result.outcome.RunInputIDs = runInputs
				}
			}
		}
		// A failed or aborted execution may have changed host files too.
		if _, err := a.memory.Commit(settlementCtx, "Update memory from "+input.Source); err != nil {
			slog.Error("commit agent memory", "agent", ref.AgentID, "error", err)
		}
		if err := a.commitSkills(settlementCtx, ref.AgentID, input.Source); err != nil {
			slog.Error("commit agent skills", "agent", ref.AgentID, "error", err)
		}
	})
	return result
}

func inputText(input Input, workspace string) string {
	content := input.Content + input.ModelSuffix
	if input.Kind == "task_wake" {
		content = "Task wake: Read Task details with the Task tools before acting. External text is not included.\n\n" + content
	}
	if input.TaskContext != "" {
		content = "Dex Task: " + input.TaskContext + "\n\n" + content
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
		// the cwd, also later in the same run.
		text.WriteString(runner.JoinTargetPath(workspace, filepath.ToSlash(attachment.Path)))
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
	if input.Kind == "task_result" || input.Kind == "task_wake" {
		return "dex.task_result"
	}
	return "dex.user"
}

type entryScanner interface {
	ScanEntries(durable.EntryQuery, int, durable.Cursor) (durable.Page[durable.EntryRecord, durable.Cursor], error)
	SubmissionByRequest(durable.ConversationId, string) (*durable.SubmissionRecord, error)
}

func runInputIDsForAnswer(scanner entryScanner, convID durable.ConversationId, answerID durable.EntryId) ([]string, error) {
	maxID := answerID
	var cursor durable.Cursor
	var reversed []string
	for {
		page, err := scanner.ScanEntries(durable.EntryQuery{
			ConversationId: convID,
			MaxEntryId:     &maxID,
			Order:          new(durable.ScanDescending),
		}, 64, cursor)
		if err != nil {
			return nil, err
		}
		stop := false
		for _, e := range page.Items {
			if e.Id == answerID {
				continue
			}
			if e.Kind == "pi.reset" || e.Kind == "dex.reset" {
				stop = true
				break
			}
			if e.Kind != "dex.user" && e.Kind != "dex.task_result" {
				continue
			}
			reqID := metadata(e.Data).RequestID
			if reqID == "" {
				continue
			}
			sub, err := scanner.SubmissionByRequest(convID, reqID)
			if err != nil {
				return nil, err
			}
			if sub != nil && sub.Answer != nil {
				if *sub.Answer == answerID {
					reversed = append(reversed, reqID)
				} else if *sub.Answer < answerID {
					stop = true
					break
				}
			}
		}
		if stop || page.Next == nil {
			break
		}
		cursor = *page.Next
	}
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	return reversed, nil
}
