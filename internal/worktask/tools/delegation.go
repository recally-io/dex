package tools

import (
	"context"
	"database/sql"
	"errors"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/worktask"
)

func (s *Service) Split(ctx context.Context, c engine.TaskContext, a WorkTaskSplitArgs) (WorkTaskInfo, error) {
	return mutate(ctx, s, c, "split", func(q *sqlc.Queries, l *worktask.Ledger, actor worktask.Actor, origin worktask.Origin, now int64) (WorkTaskInfo, error) {
		parent, err := readableTask(ctx, q, actor, a.ParentID)
		if err != nil {
			return WorkTaskInfo{}, err
		}
		child, err := l.SplitTask(ctx, actor, origin, parent, worktask.SplitTaskParams{
			Title: a.Title, AssigneeBotID: a.AssigneeBotID, Note: a.Note, Status: a.Status, Reviewer: a.Reviewer, DependsOn: a.DependsOn,
		}, now)
		if err != nil {
			return WorkTaskInfo{}, err
		}
		return taskInfo(ctx, q, child)
	})
}

func (s *Service) Assign(ctx context.Context, c engine.TaskContext, a WorkTaskAssignArgs) (WorkTaskInfo, error) {
	return mutate(ctx, s, c, "assign", func(q *sqlc.Queries, l *worktask.Ledger, actor worktask.Actor, origin worktask.Origin, now int64) (WorkTaskInfo, error) {
		task, err := l.AssignTask(ctx, actor, origin, a.ID, a.AssigneeBotID, a.Reviewer, a.Note, now)
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, worktask.ErrForbidden) {
			return WorkTaskInfo{}, invalid("cannot assign this Task to the requested Bot")
		}
		if err != nil {
			return WorkTaskInfo{}, err
		}
		// A handoff can remove the caller's read access. Return only the outcome of the write.
		info := summary(task)
		info.Title, err = worktask.ModelTitle(ctx, q, task)
		if err != nil {
			return WorkTaskInfo{}, err
		}
		blockers, err := worktask.ComputeBlockers(ctx, q, task)
		info.Blocked = len(blockers) > 0
		return info, err
	})
}

type WorkloadItem struct {
	WorkTaskInfo
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
		return nil, worktask.ErrForbidden
	}
	tasks, err := q.ListBotWorkload(ctx, conv.AgentID)
	if err != nil {
		return nil, err
	}
	items := make([]WorkloadItem, 0, len(tasks))
	for _, task := range tasks {
		item := WorkloadItem{WorkTaskInfo: summary(task), InReview: task.Status == worktask.StatusInReview}
		item.Title, err = worktask.ModelTitle(ctx, q, task)
		if err != nil {
			return nil, err
		}
		blockers, err := worktask.ComputeBlockers(ctx, q, task)
		if err != nil {
			return nil, err
		}
		item.Blocked = len(blockers) > 0
		switch {
		case item.InReview && task.ReviewerKind == worktask.ActorBot && task.ReviewerID == conv.AgentID:
			item.PendingAction = "review"
		case item.InReview:
			item.PendingAction = "wait_for_review"
		case task.AssigneeBotID != conv.AgentID:
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
