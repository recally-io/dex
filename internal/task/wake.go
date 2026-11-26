package task

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"slices"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
)

const (
	WakeAssigned          = "assigned"
	WakeReviewRequested   = "review_requested"
	WakeChangesRequested  = "changes_requested"
	WakeGateUnblocked     = "gate_unblocked"
	WakeDependencyChanged = "dependency_changed"
	WakeExecutionFailed   = "execution_failed"
	WakeExecutionStale    = "execution_stale"
	WakeQuestionAnswered  = "question_answered"
)

type WakeItem struct {
	TaskKey string `json:"taskKey"`
	Reason  string `json:"reason"`
}

type WakePayload struct {
	HighWaterEntryID int64      `json:"highWaterEntryId"`
	Items            []WakeItem `json:"items"`
}

// queueWake uses the entry just appended by this mutation. It returns false if the Bot has no active Main.
func (l *Ledger) queueWake(ctx context.Context, botID string, task sqlc.Task, reason string, now int64) (bool, error) {
	q := l.q
	if err := q.EnsureTaskMain(ctx, sqlc.EnsureTaskMainParams{AgentID: botID, CreatedAt: now}); err != nil {
		return false, err
	}
	main, err := q.GetTaskMain(ctx, botID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	item := WakeItem{TaskKey: FormatKey(task.Number), Reason: reason}
	entryID := l.entryIDs[len(l.entryIDs)-1]
	pending, err := q.GetUnadmittedTaskWake(ctx, main)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		payload, err := json.Marshal(WakePayload{HighWaterEntryID: entryID, Items: []WakeItem{item}})
		if err != nil {
			return false, err
		}
		id, err := insertTaskWake(ctx, q, main, string(payload), now)
		if err != nil {
			return false, err
		}
		// The input link lets the Assignee's Main attach answers and children to the Task.
		if botID == task.AssigneeBotID {
			if err := q.AssociateTaskInput(ctx, sqlc.AssociateTaskInputParams{SubmissionID: id, TaskID: task.ID}); err != nil {
				return false, err
			}
		}
	case err != nil:
		return false, err
	default:
		var payload WakePayload
		if err := json.Unmarshal([]byte(pending.Content), &payload); err != nil {
			return false, err
		}
		payload.HighWaterEntryID = max(payload.HighWaterEntryID, entryID)
		if !slices.Contains(payload.Items, item) {
			payload.Items = append(payload.Items, item)
		}
		content, err := json.Marshal(payload)
		if err != nil {
			return false, err
		}
		if err := q.UpdateUnadmittedTaskWake(ctx, sqlc.UpdateUnadmittedTaskWakeParams{ID: pending.ID, Content: string(content)}); err != nil {
			return false, err
		}
		// Without one Task key, replies and questions must not attach to any Task.
		if slices.ContainsFunc(payload.Items, func(other WakeItem) bool { return other.TaskKey != item.TaskKey }) {
			if err := q.ClearTaskWakeAssociation(ctx, pending.ID); err != nil {
				return false, err
			}
		}
	}
	if !slices.Contains(l.wakeConversations, main) {
		l.wakeConversations = append(l.wakeConversations, main)
	}
	return true, nil
}

func insertTaskWake(ctx context.Context, q *sqlc.Queries, main int64, content string, now int64) (string, error) {
	id := "task-wake:" + uuid.NewV7().String()
	return id, q.CreateTaskWake(ctx, sqlc.CreateTaskWakeParams{ID: id, ConversationID: main, Content: content, CreatedAt: now})
}

// RequeueTaskWake copies the latest stored payload after admission frees the slot.
// The caller must pass the latest source row and use the same write transaction.
func RequeueTaskWake(ctx context.Context, q *sqlc.Queries, source sqlc.Submission, now int64) error {
	id, err := insertTaskWake(ctx, q, source.ConversationID, source.Content, now)
	if err != nil {
		return err
	}
	return q.CopyTaskWakeAssociation(ctx, sqlc.CopyTaskWakeAssociationParams{NewSubmissionID: id, SourceSubmissionID: source.ID})
}
