package tools

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"html"
	"slices"
	"strconv"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	tasksvc "github.com/recally-io/dex/internal/task"
)

const bootstrapBudget = 16 * 1024

func (s *Service) List(ctx context.Context, c engine.TaskContext, a TaskListArgs) ([]TaskInfo, error) {
	status := cmp.Or(a.Status, "open")
	if status != "open" && status != "all" && status != tasksvc.StatusDone && status != tasksvc.StatusCancelled && !tasksvc.IsOpenStatus(status) {
		return nil, invalid("unknown Task status %q", status)
	}
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
	params := sqlc.ListBotTasksParams{BotID: conv.AgentID, StatusFilter: status}
	if conv.ParentID.Valid {
		params.ChildConversationID = conv.ID
	}
	rows, err := q.ListBotTasks(ctx, params)
	if err != nil {
		return nil, err
	}
	out := make([]TaskInfo, 0, len(rows))
	for _, task := range rows {
		info, err := taskInfo(ctx, q, task)
		if err != nil {
			return nil, err
		}
		info.Sessions = nil
		out = append(out, info)
	}
	return out, tx.Commit()
}

func (s *Service) Read(ctx context.Context, c engine.TaskContext, a TaskReadArgs) (TaskDetail, error) {
	var afterSeq int64
	if a.AfterSeq != nil {
		afterSeq = *a.AfterSeq
	}
	if afterSeq < 0 || a.Limit < 0 {
		return TaskDetail{}, invalid("afterSeq and limit cannot be negative")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return TaskDetail{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	conv, _, err := caller(ctx, q, c)
	if err != nil {
		return TaskDetail{}, err
	}
	task, err := readableTask(ctx, q, tasksvc.BotInConversation(conv), a.ID)
	if err != nil {
		return TaskDetail{}, err
	}
	info, blockers, err := taskInfoAndBlockers(ctx, q, task)
	if err != nil {
		return TaskDetail{}, err
	}
	if info.Sessions == nil {
		info.Sessions = []TaskSession{}
	}
	detail := TaskDetail{TaskInfo: info, Blockers: blockers, NextCursor: afterSeq}
	if err := readTimeline(ctx, q, task.ID, a, &detail, false, new(bootstrapBudget-2)); err != nil {
		return TaskDetail{}, err
	}
	questions, err := q.ListTaskQuestions(ctx, task.ID)
	if err != nil {
		return TaskDetail{}, err
	}
	for _, question := range questions {
		detail.Questions = append(detail.Questions, TaskQuestionInfo{ID: question.ID, Text: question.Text, Queued: question.Queued != 0})
	}
	objects, err := q.ListTaskObjects(ctx, task.ID)
	if err != nil {
		return TaskDetail{}, err
	}
	waits, err := q.ListTaskWaits(ctx, task.ID)
	if err != nil {
		return TaskDetail{}, err
	}
	for _, object := range objects {
		link := TaskLinkInfo{Namespace: object.Namespace, Type: object.ObjectType, Key: object.ObjectKey}
		for _, w := range waits {
			if w.Namespace == object.Namespace && w.ObjectType == object.ObjectType && w.ObjectKey == object.ObjectKey {
				link.Waits = append(link.Waits, TaskWaitInfo{Event: w.EventType, Revision: w.Revision, Resolved: w.ResolvedBy.Valid, AlertMain: w.Alert == 1})
			}
		}
		detail.Links = append(detail.Links, link)
	}
	if a.IncludeUpstream {
		if err := readUpstream(ctx, q, task, &detail); err != nil {
			return TaskDetail{}, err
		}
	}
	return detail, tx.Commit()
}

func readUpstream(ctx context.Context, q *sqlc.Queries, task sqlc.Task, detail *TaskDetail) error {
	parent, dependencies, err := tasksvc.UpstreamTasks(ctx, q, task)
	if err != nil || parent == nil {
		return err
	}
	remaining := bootstrapBudget - 2
	add := func(related sqlc.Task, relation string) error {
		title, err := tasksvc.ModelTitle(ctx, q, related)
		if err != nil {
			return err
		}
		item := UpstreamContext{Key: tasksvc.FormatKey(related.Number), Title: title, Status: related.Status, Relation: relation}
		header, err := json.Marshal(item)
		if err != nil {
			return err
		}
		remaining -= len(header) + len(",\"entries\":[],")
		if remaining < 0 {
			detail.UpstreamHasMore = true
			return nil
		}
		var timeline TaskDetail
		if err := readTimeline(ctx, q, related.ID, TaskReadArgs{}, &timeline, true, &remaining); err != nil {
			return err
		}
		item.Entries = timeline.Entries
		detail.Upstream = append(detail.Upstream, item)
		detail.UpstreamHasMore = detail.UpstreamHasMore || timeline.HasMore
		return nil
	}
	if err := add(*parent, "parent"); err != nil {
		return err
	}
	for _, dependency := range dependencies {
		if remaining <= 0 {
			detail.UpstreamHasMore = true
			break
		}
		if err := add(dependency, "dependency"); err != nil {
			return err
		}
	}
	return nil
}

func readTimeline(ctx context.Context, q *sqlc.Queries, taskID string, a TaskReadArgs, detail *TaskDetail, trustedOnly bool, remaining *int) error {
	limit := min(cmp.Or(a.Limit, 20), 100)
	bootstrap := a.AfterSeq == nil && len(a.Tags) == 0
	tags, err := json.Marshal(a.Tags)
	if err != nil {
		return err
	}
	params := sqlc.ListTaskTimelineParams{TaskID: taskID, AfterSeq: detail.NextCursor, Tags: string(tags), MaxRows: limit + 1}
	if trustedOnly {
		params.TrustedOnly = 1
	}
	if bootstrap {
		params.Bootstrap = 1
	}
	entries, err := q.ListTaskTimeline(ctx, params)
	if err != nil {
		return err
	}
	for _, row := range entries[:min(int(limit), len(entries))] {
		entry := entryInfo(row)
		if bootstrap {
			encoded, err := json.Marshal(entry)
			if err != nil {
				return err
			}
			size := len(encoded) + 1 // Include space for a separator.
			if size > *remaining {
				continue
			}
			*remaining -= size
		}
		detail.NextCursor = max(detail.NextCursor, entry.Seq)
		detail.Entries = append(detail.Entries, entry)
	}
	if bootstrap {
		slices.SortFunc(detail.Entries, func(a, b TaskEntryInfo) int { return cmp.Compare(a.Seq, b.Seq) })
	}
	detail.HasMore = len(entries) > len(detail.Entries)
	return nil
}

func entryInfo(e sqlc.ListTaskTimelineRow) TaskEntryInfo {
	info := TaskEntryInfo{Seq: e.Seq, Trust: e.Trust, CreatedAt: e.CreatedAt}
	if e.Trust == tasksvc.TrustExternal {
		info.Kind = tasksvc.KindExternal
		info.EventType, info.SourceURL, info.AuthorClass = e.EventType, e.SourceUrl, e.AuthorAssociation
		return info
	}
	info.ID, info.Kind, info.Tag = e.ID, e.Kind, e.Tag
	if e.Trust == tasksvc.TrustOwner {
		info.Content = new(e.Content)
		return info
	}
	actor := e.ActorKind
	if id := cmp.Or(e.ActorBotID, e.ActorExecutionID); id != "" {
		actor += ":" + id
	}
	info.Content = new(fmt.Sprintf(`<task-entry seq="%d" actor="%s" trust="agent">%s</task-entry>`, e.Seq, html.EscapeString(actor), html.EscapeString(e.Content)))
	return info
}

func summary(task sqlc.Task) TaskInfo {
	return TaskInfo{ID: task.ID, Key: tasksvc.FormatKey(task.Number), Title: task.Title, Status: task.Status, UpdatedAt: task.UpdatedAt,
		AssigneeBotID: task.AssigneeBotID, ReviewerKind: task.ReviewerKind, ReviewerID: task.ReviewerID, ReviewSeq: task.ReviewSeq}
}

func taskInfo(ctx context.Context, q *sqlc.Queries, task sqlc.Task) (TaskInfo, error) {
	info, _, err := taskInfoAndBlockers(ctx, q, task)
	return info, err
}

func taskInfoAndBlockers(ctx context.Context, q *sqlc.Queries, task sqlc.Task) (TaskInfo, []tasksvc.Blocker, error) {
	info := summary(task)
	title, err := tasksvc.ModelTitle(ctx, q, task)
	if err != nil {
		return info, nil, err
	}
	info.Title = title
	sessions, err := q.ListTaskSessions(ctx, task.ID)
	if err != nil {
		return info, nil, err
	}
	blockers, err := tasksvc.ComputeBlockers(ctx, q, task)
	if err != nil {
		return info, nil, err
	}
	info.Blocked = len(blockers) > 0
	for _, s := range sessions {
		link := TaskSession{ID: strconv.FormatInt(s.ConversationID, 10), Title: s.Title, BotID: s.AgentID, BotName: s.AgentName, IsPrimary: s.IsPrimary == 1, LinkedAt: s.LinkedAt}
		info.Sessions = append(info.Sessions, link)
		if link.IsPrimary {
			info.Session = new(link)
		}
	}
	return info, blockers, nil
}
