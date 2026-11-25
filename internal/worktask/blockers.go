package worktask

import (
	"context"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// Blocker describes an unresolved external wait or open Task question. A queue
// position starts at 1 in the question's Main and is 0 outside that queue.
type Blocker struct {
	Kind          string `json:"kind"`
	WaitID        int64  `json:"waitId,omitzero"`
	Namespace     string `json:"namespace,omitempty"`
	ObjectType    string `json:"objectType,omitempty"`
	ObjectKey     string `json:"objectKey,omitempty"`
	EventType     string `json:"eventType,omitempty"`
	QuestionID    string `json:"questionId,omitempty"`
	Question      string `json:"question,omitempty"`
	QueuePosition int64  `json:"queuePosition,omitzero"`
}

// ComputeBlockers derives the current blockers without changing the Task.
// Pass snapshot queries when reading the Task and blockers together.
func ComputeBlockers(ctx context.Context, q *sqlc.Queries, task sqlc.WorkTask) ([]Blocker, error) {
	if !IsOpenStatus(task.Status) {
		return nil, nil
	}
	waits, err := q.ListUnresolvedWorkTaskWaits(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	questions, err := q.ListWorkTaskQuestionBlockers(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	blockers := make([]Blocker, 0, len(waits)+len(questions))
	for _, w := range waits {
		blockers = append(blockers, Blocker{Kind: "wait", WaitID: w.ID, Namespace: w.Namespace, ObjectType: w.ObjectType, ObjectKey: w.ObjectKey, EventType: w.EventType})
	}
	for _, q := range questions {
		blockers = append(blockers, Blocker{Kind: "question", QuestionID: q.ID, Question: q.Text, QueuePosition: q.QueuePosition})
	}
	return blockers, nil
}
