package task

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// ErrRevision reports a failed HTTP write precondition separately from a stale review.
var ErrRevision = errors.New("task changed since you opened it")

// Summary contains the projection used in Board cards and mutation responses.
type Summary struct {
	Task           sqlc.Task
	BlockerCount   int
	PrimarySession *sqlc.ListTaskSessionsRow
}

type Filter struct {
	BotID, Status, Source string
	IncludeClosed         bool
	Blocked, InReview     *bool
}

func (s *Service) ListTasks(ctx context.Context, f Filter) ([]Summary, error) {
	if f.Status != "" && !IsOpenStatus(f.Status) && f.Status != StatusDone && f.Status != StatusCancelled {
		return nil, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	tasks, err := q.ListTasks(ctx, sqlc.ListTasksParams{
		BotID: f.BotID, StatusFilter: f.Status, Source: f.Source, IncludeClosed: f.IncludeClosed, InReview: f.InReview,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Summary, 0, len(tasks))
	for _, task := range tasks {
		view, err := readTaskSummary(ctx, q, task)
		if err != nil {
			return nil, err
		}
		if f.Blocked == nil || *f.Blocked == (view.BlockerCount > 0) {
			out = append(out, view)
		}
	}
	return out, tx.Commit()
}

// Lookup reads an active Bot's Task by key or UUID.
func Lookup(ctx context.Context, q *sqlc.Queries, key string) (sqlc.Task, error) {
	if n, ok := ParseKey(key); ok {
		return q.GetTaskByNumber(ctx, n)
	}
	return q.GetTask(ctx, key)
}

func NewSummary(task sqlc.Task, blockerCount int, sessions []sqlc.ListTaskSessionsRow) Summary {
	out := Summary{Task: task, BlockerCount: blockerCount}
	if len(sessions) > 0 && sessions[0].IsPrimary == 1 {
		out.PrimarySession = new(sessions[0])
	}
	return out
}

func readTaskSummary(ctx context.Context, q *sqlc.Queries, task sqlc.Task) (Summary, error) {
	blockers, err := ComputeBlockers(ctx, q, task)
	if err != nil {
		return Summary{}, err
	}
	sessions, err := q.ListTaskSessions(ctx, task.ID)
	return NewSummary(task, len(blockers), sessions), err
}

func checkTaskReviewer(ctx context.Context, q *sqlc.Queries, kind, id string) error {
	if !validReviewer(kind, id) {
		return ErrInvalid
	}
	if kind == ActorBot {
		_, err := q.GetActiveTaskAssignee(ctx, id)
		return err
	}
	return nil
}

func checkRevision(task sqlc.Task, expected *int64) error {
	if expected != nil && *expected != task.Revision {
		return ErrRevision
	}
	return nil
}

type UpdateParams struct {
	Title, Status, AssigneeBotID, ReviewerKind, ReviewerID *string
	Revision                                               *int64
}

func (s *Service) UpdateTask(ctx context.Context, key string, p UpdateParams) (out Summary, err error) {
	err = s.write(ctx, func(l *Ledger, now int64) error {
		task, err := Lookup(ctx, l.q, key)
		if err != nil {
			return err
		}
		if err := checkRevision(task, p.Revision); err != nil {
			return err
		}
		task, err = l.updateTask(ctx, task, p, now)
		if err != nil {
			return err
		}
		out, err = readTaskSummary(ctx, l.q, task)
		return err
	})
	return out, err
}

func (l *Ledger) updateTask(ctx context.Context, task sqlc.Task, p UpdateParams, now int64) (sqlc.Task, error) {
	roles := sqlc.UpdateTaskRolesParams{ID: task.ID, AssigneeBotID: task.AssigneeBotID, ReviewerKind: task.ReviewerKind, ReviewerID: task.ReviewerID}
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
		return sqlc.Task{}, err
	}
	if p.Title != nil {
		title, err := Title(*p.Title)
		if err != nil {
			return sqlc.Task{}, err
		}
		if title != task.Title {
			task, err = l.Rename(ctx, Owner, OriginUser, task, title, now)
			if err != nil {
				return sqlc.Task{}, err
			}
		}
	}
	if p.Status != nil && *p.Status != task.Status {
		task, err = l.TransitionStatus(ctx, Owner, OriginUser, task, *p.Status, nil, now)
		if err != nil {
			return sqlc.Task{}, err
		}
	}
	if assigned && IsOpenStatus(task.Status) {
		_, err = l.queueWake(ctx, task.AssigneeBotID, task, WakeAssigned, now)
	}
	return task, err
}

func (l *Ledger) changeRoles(ctx context.Context, actor Actor, task sqlc.Task, roles sqlc.UpdateTaskRolesParams, note string, now int64) (sqlc.Task, bool, error) {
	var err error
	assigned := roles.AssigneeBotID != task.AssigneeBotID
	changedReviewer := roles.ReviewerKind != task.ReviewerKind || roles.ReviewerID != task.ReviewerID
	if assigned || changedReviewer {
		if assigned {
			if _, err := l.q.GetActiveTaskAssignee(ctx, roles.AssigneeBotID); err != nil {
				return sqlc.Task{}, false, err
			}
		}
		if changedReviewer {
			if err := checkTaskReviewer(ctx, l.q, roles.ReviewerKind, roles.ReviewerID); err != nil {
				return sqlc.Task{}, false, err
			}
		}
		meta, err := json.Marshal(struct {
			AssigneeBotID string `json:"assigneeBotId"`
			ReviewerKind  string `json:"reviewerKind"`
			ReviewerID    string `json:"reviewerId"`
		}{roles.AssigneeBotID, roles.ReviewerKind, roles.ReviewerID})
		if err != nil {
			return sqlc.Task{}, false, err
		}
		task, err = l.q.UpdateTaskRoles(ctx, roles)
		if err != nil {
			return sqlc.Task{}, false, err
		}
		content := "Reviewer changed"
		if assigned {
			content = "Assigned to " + roles.AssigneeBotID
		}
		if note != "" {
			if note, err = Content(note); err != nil {
				return sqlc.Task{}, false, err
			}
			content += "\n" + note
		}
		task, _, err = l.append(ctx, actor, task, task.Status, task.Title, task.ReviewSeq, sqlc.InsertTaskEntryParams{
			Kind: KindAssign, MetaJson: string(meta), Content: content, CreatedAt: now,
		})
		if err != nil {
			return sqlc.Task{}, false, err
		}
	}

	if changedReviewer && task.Status == StatusInReview && task.ReviewerKind == ActorBot {
		_, err = l.queueWake(ctx, task.ReviewerID, task, WakeReviewRequested, now)
	}
	return task, assigned, err
}

func (s *Service) Review(ctx context.Context, actor Actor, origin Origin, key, decision string, seq int64, note string) (out Summary, err error) {
	err = s.write(ctx, func(l *Ledger, now int64) error {
		task, err := Lookup(ctx, l.q, key)
		if err != nil {
			return err
		}
		task, err = l.Review(ctx, actor, origin, task, decision, seq, note, now)
		if err != nil {
			return err
		}
		out, err = readTaskSummary(ctx, l.q, task)
		return err
	})
	return out, err
}
