package worktask

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// ErrRevision reports a failed HTTP write precondition separately from a stale review.
var ErrRevision = errors.New("task changed since you opened it")

// BoardSummary contains the projection used in Board cards and mutation responses.
type BoardSummary struct {
	Task           sqlc.WorkTask
	BlockerCount   int
	PrimarySession *sqlc.ListWorkTaskSessionsRow
}

type BoardFilter struct {
	BotID, Status, Source string
	IncludeClosed         bool
	Blocked, InReview     *bool
}

func (s *Service) ListBoard(ctx context.Context, f BoardFilter) ([]BoardSummary, error) {
	if f.Status != "" && !IsOpenStatus(f.Status) && f.Status != StatusDone && f.Status != StatusCancelled {
		return nil, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	tasks, err := q.ListBoardTasks(ctx, sqlc.ListBoardTasksParams{
		BotID: f.BotID, StatusFilter: f.Status, Source: f.Source, IncludeClosed: f.IncludeClosed, InReview: f.InReview,
	})
	if err != nil {
		return nil, err
	}
	out := make([]BoardSummary, 0, len(tasks))
	for _, task := range tasks {
		view, err := readBoardSummary(ctx, q, task)
		if err != nil {
			return nil, err
		}
		if f.Blocked == nil || *f.Blocked == (view.BlockerCount > 0) {
			out = append(out, view)
		}
	}
	return out, tx.Commit()
}

func boardTask(ctx context.Context, q *sqlc.Queries, key string) (sqlc.WorkTask, error) {
	if n, ok := ParseKey(key); ok {
		return q.GetBoardTaskByNumber(ctx, n)
	}
	return q.GetBoardTask(ctx, key)
}

func NewBoardSummary(task sqlc.WorkTask, blockerCount int, sessions []sqlc.ListWorkTaskSessionsRow) BoardSummary {
	out := BoardSummary{Task: task, BlockerCount: blockerCount}
	if len(sessions) > 0 && sessions[0].IsPrimary == 1 {
		out.PrimarySession = new(sessions[0])
	}
	return out
}

func readBoardSummary(ctx context.Context, q *sqlc.Queries, task sqlc.WorkTask) (BoardSummary, error) {
	blockers, err := ComputeBlockers(ctx, q, task)
	if err != nil {
		return BoardSummary{}, err
	}
	sessions, err := q.ListWorkTaskSessions(ctx, task.ID)
	return NewBoardSummary(task, len(blockers), sessions), err
}

func (s *Service) CreateBoardTask(ctx context.Context, p CreateTaskParams) (out BoardSummary, err error) {
	err = s.write(ctx, func(l *Ledger, now int64) error {
		task, err := l.CreateTask(ctx, Owner, OriginUser, p, now)
		out.Task = task // A new Task has no blockers or linked Sessions.
		return err
	})
	return out, err
}

func checkBoardReviewer(ctx context.Context, q *sqlc.Queries, kind, id string) error {
	if !validReviewer(kind, id) {
		return ErrInvalid
	}
	if kind == ActorBot {
		_, err := q.GetActiveWorkTaskAssignee(ctx, id)
		return err
	}
	return nil
}

func checkRevision(task sqlc.WorkTask, expected *int64) error {
	if expected != nil && *expected != task.Revision {
		return ErrRevision
	}
	return nil
}

type BoardUpdate struct {
	Title, Status, AssigneeBotID, ReviewerKind, ReviewerID *string
	Revision                                               *int64
}

func (s *Service) UpdateBoardTask(ctx context.Context, key string, p BoardUpdate) (out BoardSummary, err error) {
	err = s.write(ctx, func(l *Ledger, now int64) error {
		task, err := boardTask(ctx, l.q, key)
		if err != nil {
			return err
		}
		if err := checkRevision(task, p.Revision); err != nil {
			return err
		}
		task, err = l.updateBoardTask(ctx, task, p, now)
		if err != nil {
			return err
		}
		out, err = readBoardSummary(ctx, l.q, task)
		return err
	})
	return out, err
}

func (l *Ledger) updateBoardTask(ctx context.Context, task sqlc.WorkTask, p BoardUpdate, now int64) (sqlc.WorkTask, error) {
	roles := sqlc.UpdateBoardTaskRolesParams{ID: task.ID, AssigneeBotID: task.AssigneeBotID, ReviewerKind: task.ReviewerKind, ReviewerID: task.ReviewerID}
	if p.AssigneeBotID != nil {
		roles.AssigneeBotID = *p.AssigneeBotID
	}
	if p.ReviewerKind != nil {
		roles.ReviewerKind = *p.ReviewerKind
		if roles.ReviewerKind != ActorBot {
			roles.ReviewerID = ""
		}
	}
	if p.ReviewerID != nil {
		roles.ReviewerID = *p.ReviewerID
	}
	task, assigned, err := l.changeRoles(ctx, Owner, task, roles, "", now)
	if err != nil {
		return sqlc.WorkTask{}, err
	}
	if p.Title != nil {
		title, err := Title(*p.Title)
		if err != nil {
			return sqlc.WorkTask{}, err
		}
		if title != task.Title {
			task, err = l.Rename(ctx, Owner, OriginUser, task, title, now)
			if err != nil {
				return sqlc.WorkTask{}, err
			}
		}
	}
	if p.Status != nil && *p.Status != task.Status {
		task, err = l.TransitionStatus(ctx, Owner, OriginUser, task, *p.Status, nil, now)
		if err != nil {
			return sqlc.WorkTask{}, err
		}
	}
	if assigned && IsOpenStatus(task.Status) {
		_, err = l.queueWake(ctx, task.AssigneeBotID, task, WakeAssigned, now)
	}
	return task, err
}

func (l *Ledger) changeRoles(ctx context.Context, actor Actor, task sqlc.WorkTask, roles sqlc.UpdateBoardTaskRolesParams, note string, now int64) (sqlc.WorkTask, bool, error) {
	var err error
	assigned := roles.AssigneeBotID != task.AssigneeBotID
	changedReviewer := roles.ReviewerKind != task.ReviewerKind || roles.ReviewerID != task.ReviewerID
	if assigned || changedReviewer {
		if assigned {
			if _, err := l.q.GetActiveWorkTaskAssignee(ctx, roles.AssigneeBotID); err != nil {
				return sqlc.WorkTask{}, false, err
			}
		}
		if changedReviewer {
			if err := checkBoardReviewer(ctx, l.q, roles.ReviewerKind, roles.ReviewerID); err != nil {
				return sqlc.WorkTask{}, false, err
			}
		}
		meta, err := json.Marshal(struct {
			AssigneeBotID string `json:"assigneeBotId"`
			ReviewerKind  string `json:"reviewerKind"`
			ReviewerID    string `json:"reviewerId"`
		}{roles.AssigneeBotID, roles.ReviewerKind, roles.ReviewerID})
		if err != nil {
			return sqlc.WorkTask{}, false, err
		}
		task, err = l.q.UpdateBoardTaskRoles(ctx, roles)
		if err != nil {
			return sqlc.WorkTask{}, false, err
		}
		content := "Reviewer changed"
		if assigned {
			content = "Assigned to " + roles.AssigneeBotID
		}
		if note != "" {
			if note, err = Content(note); err != nil {
				return sqlc.WorkTask{}, false, err
			}
			content += "\n" + note
		}
		task, _, err = l.append(ctx, actor, task, task.Status, task.Title, task.ReviewSeq, sqlc.InsertWorkTaskEntryParams{
			Kind: KindAssign, MetaJson: string(meta), Content: content, CreatedAt: now,
		})
		if err != nil {
			return sqlc.WorkTask{}, false, err
		}
	}

	if changedReviewer && task.Status == StatusInReview && task.ReviewerKind == ActorBot {
		_, err = l.queueWake(ctx, task.ReviewerID, task, WakeReviewRequested, now)
	}
	return task, assigned, err
}

func (s *Service) CreateBoardEntry(ctx context.Context, key string, revision *int64, p PostEntryParams) (entry sqlc.WorkTaskEntry, err error) {
	err = s.write(ctx, func(l *Ledger, now int64) error {
		task, err := boardTask(ctx, l.q, key)
		if err != nil {
			return err
		}
		if err := checkRevision(task, revision); err != nil {
			return err
		}
		entry, err = l.PostEntry(ctx, Owner, OriginUser, task, p, now)
		return err
	})
	return entry, err
}

func (s *Service) ReviewBoardTask(ctx context.Context, key, decision string, seq int64, note string) (out BoardSummary, err error) {
	err = s.write(ctx, func(l *Ledger, now int64) error {
		task, err := boardTask(ctx, l.q, key)
		if err != nil {
			return err
		}
		task, err = l.Review(ctx, Owner, OriginUser, task, decision, seq, note, now)
		if err != nil {
			return err
		}
		out, err = readBoardSummary(ctx, l.q, task)
		return err
	})
	return out, err
}
