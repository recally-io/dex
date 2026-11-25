package worktask

import (
	"context"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// Blocker describes an unresolved external wait or open Task question. A queue
// position starts at 1 on the Assignee's Main and is 0 outside that queue.
type Blocker struct {
	Kind          string
	WaitID        int64
	Namespace     string
	ObjectType    string
	ObjectKey     string
	EventType     string
	QuestionID    string
	Question      string
	QueuePosition int64
}

// ComputeBlockers derives the current blockers without changing the Task.
// Pass snapshot queries when reading the Task and blockers together.
func ComputeBlockers(ctx context.Context, q *sqlc.Queries, task sqlc.WorkTask) ([]Blocker, error) {
	waits, err := q.ListUnresolvedWorkTaskWaits(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	questions, err := q.ListWorkTaskQuestionBlockers(ctx, sqlc.ListWorkTaskQuestionBlockersParams{TaskID: task.ID, AssigneeBotID: task.AssigneeBotID})
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
