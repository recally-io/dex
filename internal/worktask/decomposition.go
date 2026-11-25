package worktask

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"fmt"
	"strings"

	"github.com/recally-io/dex/internal/db/sqlc"
)

type SplitTaskParams struct {
	Title, AssigneeBotID, Note, Status, Reviewer string
	DependsOn                                    []string
}

func delegationReviewer(actor Actor, assignee, mode string) (kind, id string, err error) {
	switch mode {
	case "":
		if actor.isBot() && actor.botID != assignee {
			return ActorBot, actor.botID, nil
		}
	case "none":
	case "owner":
		return ActorOwner, "", nil
	case "creator":
		if !actor.isBot() {
			return ActorOwner, "", nil
		}
		return ActorBot, actor.botID, nil
	default:
		return "", "", ErrInvalid
	}
	return "", "", nil
}

func (l *Ledger) SplitTask(ctx context.Context, actor Actor, origin Origin, parent sqlc.WorkTask, p SplitTaskParams, now int64) (sqlc.WorkTask, error) {
	if err := authorize(ctx, l.q, actor, origin, parent, ActionSplit); err != nil {
		return sqlc.WorkTask{}, err
	}
	if !IsOpenStatus(parent.Status) {
		return sqlc.WorkTask{}, ErrInvalid
	}
	assignee := cmp.Or(p.AssigneeBotID, parent.AssigneeBotID)
	kind, reviewerID, err := delegationReviewer(actor, assignee, p.Reviewer)
	if err != nil {
		return sqlc.WorkTask{}, err
	}
	child, err := l.createTask(ctx, actor, CreateTaskParams{
		Title: p.Title, AssigneeBotID: assignee, Note: strings.TrimSpace(p.Note), Status: p.Status,
		ReviewerKind: kind, ReviewerID: reviewerID,
	}, parent, now)
	if err != nil {
		return sqlc.WorkTask{}, err
	}
	for _, key := range p.DependsOn {
		child, err = l.changeDependency(ctx, actor, child, key, true, now)
		if err != nil {
			return sqlc.WorkTask{}, err
		}
	}
	if _, err := l.record(ctx, actor, parent, sqlc.InsertWorkTaskEntryParams{
		Kind: KindNote, Content: "Created subtask " + FormatKey(child.Number), CreatedAt: now,
	}); err != nil {
		return sqlc.WorkTask{}, err
	}
	if assignee != cmp.Or(actor.botID, parent.AssigneeBotID) {
		_, err = l.queueWake(ctx, assignee, child, WakeAssigned, now)
	}
	return child, err
}

func (l *Ledger) AssignTask(ctx context.Context, actor Actor, origin Origin, key, assignee, reviewer, note string, now int64) (sqlc.WorkTask, error) {
	task, err := boardTask(ctx, l.q, key)
	if err != nil {
		return sqlc.WorkTask{}, err
	}
	if err := authorize(ctx, l.q, actor, origin, task, ActionAssign); err != nil {
		return sqlc.WorkTask{}, err
	}
	if !IsOpenStatus(task.Status) || assignee == "" {
		return sqlc.WorkTask{}, ErrInvalid
	}
	if reviewer == "" && assignee == task.AssigneeBotID {
		return task, nil
	}
	kind, id := task.ReviewerKind, task.ReviewerID
	if reviewer != "" || kind == "" {
		kind, id, err = delegationReviewer(actor, assignee, reviewer)
		if err != nil {
			return sqlc.WorkTask{}, err
		}
	}
	if actor.isBot() && task.ReviewerKind != "" && (kind != task.ReviewerKind || id != task.ReviewerID) {
		return sqlc.WorkTask{}, fmt.Errorf("%w: only the Owner can replace an existing Reviewer", ErrForbidden)
	}
	task, assigned, err := l.changeRoles(ctx, actor, task, sqlc.UpdateBoardTaskRolesParams{
		ID: task.ID, AssigneeBotID: assignee, ReviewerKind: kind, ReviewerID: id,
	}, strings.TrimSpace(note), now)
	if err == nil && assigned {
		_, err = l.queueWake(ctx, assignee, task, WakeAssigned, now)
	}
	return task, err
}

// changeDependency runs under the writer lock. The reachability read and the
// insertion must see the same graph, including edges added during a split.
func (l *Ledger) changeDependency(ctx context.Context, actor Actor, task sqlc.WorkTask, upstreamKey string, add bool, now int64) (sqlc.WorkTask, error) {
	// Archived Tasks still participate in the dependency graph.
	upstream, err := relatedBoardTask(ctx, l.q, upstreamKey)
	if err != nil {
		return sqlc.WorkTask{}, err
	}
	if !task.ParentTaskID.Valid || task.ParentTaskID != upstream.ParentTaskID || task.ID == upstream.ID {
		return sqlc.WorkTask{}, fmt.Errorf("%w: dependencies must connect two different Tasks with the same parent", ErrInvalid)
	}
	var changed int64
	verb := "Removed dependency on "
	if add {
		var cycle bool
		cycle, err = l.q.WorkTaskDependencyWouldCycle(ctx, sqlc.WorkTaskDependencyWouldCycleParams{TaskID: task.ID, DependsOnTaskID: upstream.ID})
		if err != nil {
			return sqlc.WorkTask{}, err
		}
		if cycle {
			return sqlc.WorkTask{}, fmt.Errorf("%w: this dependency would create a cycle", ErrInvalid)
		}
		verb = "Added dependency on "
		changed, err = l.q.InsertWorkTaskDependency(ctx, sqlc.InsertWorkTaskDependencyParams{TaskID: task.ID, DependsOnTaskID: upstream.ID, CreatedAt: now})
	} else {
		changed, err = l.q.DeleteWorkTaskDependency(ctx, sqlc.DeleteWorkTaskDependencyParams{TaskID: task.ID, DependsOnTaskID: upstream.ID})
	}
	if err != nil || changed == 0 {
		return task, err
	}
	updated, err := l.recordDependency(ctx, actor, task, upstream, verb+FormatKey(upstream.Number), now)
	if err == nil && !add && upstream.Status != StatusDone {
		err = l.wakeIfUnblocked(ctx, updated, now)
	}
	return updated, err
}

func relatedBoardTask(ctx context.Context, q *sqlc.Queries, key string) (sqlc.WorkTask, error) {
	if n, ok := ParseKey(key); ok {
		return q.GetRelatedWorkTaskByNumber(ctx, n)
	}
	return q.GetRelatedWorkTask(ctx, key)
}

func (l *Ledger) recordDependency(ctx context.Context, actor Actor, task, upstream sqlc.WorkTask, text string, now int64) (sqlc.WorkTask, error) {
	meta, err := json.Marshal(struct {
		DependsOn string `json:"dependsOn"`
	}{upstream.ID})
	if err != nil {
		return sqlc.WorkTask{}, err
	}
	updated, _, err := l.append(ctx, actor, task, task.Status, task.Title, task.ReviewSeq, sqlc.InsertWorkTaskEntryParams{
		Kind: KindStatus, Content: text, MetaJson: string(meta), CreatedAt: now,
	})
	return updated, err
}

func (l *Ledger) wakeIfUnblocked(ctx context.Context, task sqlc.WorkTask, now int64) error {
	if !IsOpenStatus(task.Status) {
		return nil
	}
	blockers, err := ComputeBlockers(ctx, l.q, task)
	if err == nil && len(blockers) == 0 {
		_, err = l.queueWake(ctx, task.AssigneeBotID, task, WakeGateUnblocked, now)
	}
	return err
}

func (l *Ledger) notifyDependents(ctx context.Context, before, after sqlc.WorkTask, now int64) error {
	if !after.ParentTaskID.Valid {
		return nil
	}
	var notice, reason string
	switch {
	case after.Status == StatusDone:
		notice = " completed"
	case after.Status == StatusCancelled:
		notice, reason = " was cancelled", WakeDependencyChanged
	case before.Status == StatusDone:
		notice, reason = " reopened", WakeDependencyChanged
	default:
		return nil
	}
	dependents, err := l.q.ListWorkTaskDependents(ctx, after.ID)
	if err != nil {
		return err
	}
	for _, dependent := range dependents {
		text := "Dependency " + FormatKey(after.Number) + notice
		updated, err := l.recordDependency(ctx, System, dependent, after, text, now)
		if err != nil {
			return err
		}
		if reason == "" {
			err = l.wakeIfUnblocked(ctx, updated, now)
		} else {
			_, err = l.queueWake(ctx, updated.AssigneeBotID, updated, reason, now)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) SplitTask(ctx context.Context, actor Actor, origin Origin, key string, p SplitTaskParams, revision *int64) (out BoardSummary, err error) {
	err = s.write(ctx, func(l *Ledger, now int64) error {
		parent, err := boardTask(ctx, l.q, key)
		if err != nil {
			return err
		}
		if err := checkRevision(parent, revision); err != nil {
			return err
		}
		child, err := l.SplitTask(ctx, actor, origin, parent, p, now)
		if err != nil {
			return err
		}
		out, err = readBoardSummary(ctx, l.q, child)
		return err
	})
	return out, err
}

func (s *Service) SetBoardDependency(ctx context.Context, key, upstreamKey string, add bool, revision *int64) error {
	return s.write(ctx, func(l *Ledger, now int64) error {
		task, err := boardTask(ctx, l.q, key)
		if err != nil {
			return err
		}
		if err := checkRevision(task, revision); err != nil {
			return err
		}
		_, err = l.changeDependency(ctx, Owner, task, upstreamKey, add, now)
		return err
	})
}

func (s *Service) ListOwnerReviews(ctx context.Context) ([]sqlc.WorkTask, error) {
	return s.q.ListOwnerWorkTaskReviews(ctx)
}

// UpstreamTasks returns only the direct parent and direct dependency Tasks.
func UpstreamTasks(ctx context.Context, q *sqlc.Queries, task sqlc.WorkTask) (*sqlc.WorkTask, []sqlc.WorkTask, error) {
	if !task.ParentTaskID.Valid {
		return nil, nil, nil
	}
	parent, err := q.GetRelatedWorkTask(ctx, task.ParentTaskID.String)
	if err != nil {
		return nil, nil, err
	}
	dependencies, err := q.ListWorkTaskDependencies(ctx, task.ID)
	return new(parent), dependencies, err
}
