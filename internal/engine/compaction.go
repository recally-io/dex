package engine

import (
	"context"
	"fmt"

	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

type compactOperations struct {
	Tasks map[string]durable.TaskId `json:"tasks"`
}

var compactOperationsDoc = durable.DefineDoc(durable.DocDefinition[compactOperations]{
	Kind: "dex.compact_operations", Version: 1,
	Initial:           func() compactOperations { return compactOperations{Tasks: map[string]durable.TaskId{}} },
	CheckpointWhen:    func(compactOperations, []durable.Op, durable.CheckpointInfo) bool { return true },
	DocumentSemantics: durable.NewLatestConversationSemantics(durable.ForkInitial).DocumentSemantics,
})

// Compaction describes the task and the eventual summary write separately.
type Compaction struct {
	TaskID int64
	Status string
	Reason string
}

// Compact admits one manual compaction and returns its task ID without waiting for a boundary.
func (a *Agents) Compact(ctx context.Context, ref Ref, operationID string) (int64, error) {
	if operationID == "" {
		return 0, fmt.Errorf("%w: compact operation", ErrInvalidInput)
	}
	if err := a.begin(); err != nil {
		return 0, err
	}
	defer a.end()
	unlock, err := a.lock(ctx, ref)
	if err != nil {
		return 0, err
	}
	defer unlock()
	conv, err := a.conversation(ctx, ref)
	if err != nil {
		return 0, err
	}
	h, err := a.harness(ctx, ref.AgentID)
	if err != nil {
		return 0, err
	}
	h.Resume()
	result, err := conv.Commit(ctx, func(tx durable.Tx) (any, error) {
		doc, err := tx.Doc(compactOperationsDoc, conv.Id())
		if err != nil {
			return nil, err
		}
		state, err := durable.FromJsonValue[compactOperations](doc.Snapshot())
		if err != nil {
			return nil, err
		}
		if id, ok := state.Tasks[operationID]; ok {
			return id, nil
		}
		id, err := harness.CreateCompaction(tx, conv.Id(), harness.CompactionInput{Reason: durable.CompactionManual}, nil)
		if err != nil {
			return nil, err
		}
		state.Tasks[operationID] = id
		v, err := durable.ToJsonValue(state.Tasks)
		if err != nil {
			return nil, err
		}
		if err = doc.Set("tasks", v); err != nil {
			return nil, err
		}
		return id, nil
	})
	if err != nil {
		return 0, err
	}
	id, ok := result.(durable.TaskId)
	if !ok {
		return 0, fmt.Errorf("unexpected compaction result %T", result)
	}
	return int64(id), nil
}

// LookupCompact reads an admitted task ID without opening a cold Harness.
func (a *Agents) LookupCompact(ctx context.Context, ref Ref, operationID string) (int64, bool, error) {
	if operationID == "" {
		return 0, false, ErrInvalidInput
	}
	raw, err := a.readOperationDocument(ctx, ref, compactOperationsDoc, "dex.compact_operations")
	if err != nil {
		return 0, false, err
	}
	if raw == nil {
		return 0, false, nil
	}
	state, err := durable.FromJsonValue[compactOperations](raw)
	if err != nil {
		return 0, false, err
	}
	id, ok := state.Tasks[operationID]
	return int64(id), ok, nil
}

// CompactionStatus reads a saved task and its summary write without waiting.
func (a *Agents) CompactionStatus(ctx context.Context, ref Ref, taskID int64) (Compaction, error) {
	if taskID <= 0 {
		return Compaction{}, ErrInvalidInput
	}
	var result Compaction
	err := a.readAgentStorage(ctx, ref, func(ctx context.Context, h harness.Harness) error {
		var err error
		result, err = readCompactionState(ctx, ref, taskID, h.GetTask, func(ctx context.Context, id durable.SubmissionId) (*durable.SubmissionRecord, error) {
			sub, err := h.Submission(ctx, id)
			if err != nil {
				return nil, err
			}
			record, err := sub.Status(ctx)
			return &record, err
		})
		return err
	}, func(ctx context.Context, store durable.Storage) error {
		var err error
		result, err = readCompactionState(ctx, ref, taskID, store.Task, store.Submission)
		return err
	})
	return result, err
}

func readCompactionState(ctx context.Context, ref Ref, taskID int64, taskRead func(context.Context, durable.TaskId) (*durable.TaskRecord[durable.JsonValue, durable.JsonValue, durable.JsonValue], error), submissionRead func(context.Context, durable.SubmissionId) (*durable.SubmissionRecord, error)) (Compaction, error) {
	task, err := taskRead(ctx, durable.TaskId(taskID))
	if err != nil {
		return Compaction{}, err
	}
	if task == nil || int64(task.ConversationId) != ref.ID || task.Kind != "pi.compaction" {
		return Compaction{}, ErrNotFound
	}
	result := Compaction{TaskID: taskID, Status: "generating"}
	if task.State.Status != durable.TaskTerminal {
		return result, nil
	}
	outcome := task.State.Outcome
	if outcome == nil {
		return Compaction{}, fmt.Errorf("compaction %d has no outcome", taskID)
	}
	if outcome.Status != durable.OutcomeCompleted {
		result.Status = "failed"
		result.Reason = string(outcome.Status)
		return result, nil
	}
	if outcome.Result == nil {
		return Compaction{}, fmt.Errorf("compaction %d has no result", taskID)
	}
	summary, err := durable.FromJsonValue[harness.CompactionResult](*outcome.Result)
	if err != nil {
		return Compaction{}, err
	}
	if summary.EntryId != nil {
		return Compaction{}, fmt.Errorf("conversation compaction %d returned a direct entry", taskID)
	}
	if summary.SubmissionId == nil {
		result.Status = "no_op"
		return result, nil
	}
	record, err := submissionRead(ctx, *summary.SubmissionId)
	if err != nil {
		return Compaction{}, err
	}
	if record == nil {
		return Compaction{}, fmt.Errorf("compaction %d summary submission missing", taskID)
	}
	switch record.Status {
	case durable.SubmissionDone:
		result.Status = "applied"
	case durable.SubmissionUnanswered:
		result.Status = "failed"
		if record.Reason != nil {
			result.Reason = *record.Reason
			if *record.Reason == "stale" {
				result.Status = "stale"
			}
		}
	case durable.SubmissionQueued, durable.SubmissionPlaced:
		result.Status = "pending_write"
	default:
		return Compaction{}, fmt.Errorf("unexpected summary submission status %q", record.Status)
	}
	return result, nil
}
