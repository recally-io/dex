package tools

import (
	"context"
	"database/sql"
	"errors"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	tasksvc "github.com/recally-io/dex/internal/task"
)

func (s *Service) Split(ctx context.Context, c engine.TaskContext, a TaskSplitArgs) (TaskInfo, error) {
	return mutate(ctx, s, c, "split", func(q *sqlc.Queries, l *tasksvc.Ledger, actor tasksvc.Actor, origin tasksvc.Origin, now int64) (TaskInfo, error) {
		parent, err := readableTask(ctx, q, actor, a.ParentID)
		if err != nil {
			return TaskInfo{}, err
		}
		child, err := l.SplitTask(ctx, actor, origin, parent, tasksvc.SplitTaskParams{
			Title: a.Title, AssigneeBotID: a.AssigneeBotID, Note: a.Note, Status: a.Status, Reviewer: a.Reviewer, DependsOn: a.DependsOn,
		}, now)
		if err != nil {
			return TaskInfo{}, err
		}
		return taskInfo(ctx, q, child)
	})
}

func (s *Service) Assign(ctx context.Context, c engine.TaskContext, a TaskAssignArgs) (TaskInfo, error) {
	return mutate(ctx, s, c, "assign", func(q *sqlc.Queries, l *tasksvc.Ledger, actor tasksvc.Actor, origin tasksvc.Origin, now int64) (TaskInfo, error) {
		task, err := l.AssignTask(ctx, actor, origin, a.ID, a.AssigneeBotID, a.Reviewer, a.Note, now)
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, tasksvc.ErrForbidden) {
			return TaskInfo{}, invalid("cannot assign this Task to the requested Bot")
		}
		if err != nil {
			return TaskInfo{}, err
		}
		// A handoff can remove the caller's read access. Return only the outcome of the write.
		info := summary(task)
		info.Title, err = tasksvc.ModelTitle(ctx, q, task)
		if err != nil {
			return TaskInfo{}, err
		}
		blockers, err := tasksvc.ComputeBlockers(ctx, q, task)
		info.Blocked = len(blockers) > 0
		return info, err
	})
}

type WorkloadItem struct {
	TaskInfo
	InReview      bool   `json:"inReview"`
	PendingAction string `json:"pendingAction"`
}

func (s *Service) MyWorkload(ctx context.Context, c engine.TaskContext, _ struct{}) ([]WorkloadItem, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	conv, _, err := caller(ctx, q, c)
	if err != nil {
		return nil, err
	}
	if conv.ParentID.Valid {
		return nil, tasksvc.ErrForbidden
	}
	tasks, err := q.ListBotWorkload(ctx, conv.BotID)
	if err != nil {
		return nil, err
	}
	items := make([]WorkloadItem, 0, len(tasks))
	for _, task := range tasks {
		item := WorkloadItem{TaskInfo: summary(task), InReview: task.Status == tasksvc.StatusInReview}
		item.Title, err = tasksvc.ModelTitle(ctx, q, task)
		if err != nil {
			return nil, err
		}
		blockers, err := tasksvc.ComputeBlockers(ctx, q, task)
		if err != nil {
			return nil, err
		}
		item.Blocked = len(blockers) > 0
		switch {
		case item.InReview && task.ReviewerKind == tasksvc.ActorBot && task.ReviewerID == conv.BotID:
			item.PendingAction = "review"
		case item.InReview:
			item.PendingAction = "wait_for_review"
		case task.AssigneeBotID != conv.BotID:
			item.PendingAction = "wait_for_submission"
		case item.Blocked:
			item.PendingAction = "check_blockers"
		default:
			item.PendingAction = "work"
		}
		items = append(items, item)
	}
	return items, tx.Commit()
}
