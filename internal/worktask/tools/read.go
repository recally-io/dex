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
	"github.com/recally-io/dex/internal/worktask"
)

const bootstrapBudget = 16 * 1024

func (s *Service) List(ctx context.Context, c engine.TaskContext, a WorkTaskListArgs) ([]WorkTaskInfo, error) {
	status := cmp.Or(a.Status, "open")
	if status != "open" && status != "all" && status != worktask.StatusDone && status != worktask.StatusCancelled && !worktask.IsOpenStatus(status) {
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
	params := sqlc.ListBotWorkTasksParams{BotID: conv.AgentID, StatusFilter: status}
	if conv.ParentID.Valid {
		params.ChildConversationID = conv.ID
	}
	rows, err := q.ListBotWorkTasks(ctx, params)
	if err != nil {
		return nil, err
	}
	out := make([]WorkTaskInfo, 0, len(rows))
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

func (s *Service) Read(ctx context.Context, c engine.TaskContext, a WorkTaskReadArgs) (WorkTaskDetail, error) {
	var afterSeq int64
	if a.AfterSeq != nil {
		afterSeq = *a.AfterSeq
	}
	if afterSeq < 0 || a.Limit < 0 {
		return WorkTaskDetail{}, invalid("afterSeq and limit cannot be negative")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return WorkTaskDetail{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	conv, _, err := caller(ctx, q, c)
	if err != nil {
		return WorkTaskDetail{}, err
	}
	task, err := readableTask(ctx, q, worktask.BotInConversation(conv), a.ID)
	if err != nil {
		return WorkTaskDetail{}, err
	}
	info, blockers, err := taskInfoAndBlockers(ctx, q, task)
	if err != nil {
		return WorkTaskDetail{}, err
	}
	if info.Sessions == nil {
		info.Sessions = []WorkTaskSession{}
	}
	detail := WorkTaskDetail{WorkTaskInfo: info, Blockers: blockers, NextCursor: afterSeq}
	if err := readTimeline(ctx, q, task.ID, a, &detail); err != nil {
		return WorkTaskDetail{}, err
	}
	questions, err := q.ListWorkTaskQuestions(ctx, task.ID)
	if err != nil {
		return WorkTaskDetail{}, err
	}
	for _, question := range questions {
		detail.Questions = append(detail.Questions, WorkTaskQuestionInfo{ID: question.ID, Text: question.Text, Queued: question.Queued != 0})
	}
	objects, err := q.ListWorkTaskObjects(ctx, task.ID)
	if err != nil {
		return WorkTaskDetail{}, err
	}
	waits, err := q.ListBoardTaskWaits(ctx, task.ID)
	if err != nil {
		return WorkTaskDetail{}, err
	}
	for _, object := range objects {
		link := WorkTaskLinkInfo{Namespace: object.Namespace, Type: object.ObjectType, Key: object.ObjectKey}
		for _, w := range waits {
			if w.Namespace == object.Namespace && w.ObjectType == object.ObjectType && w.ObjectKey == object.ObjectKey {
				link.Waits = append(link.Waits, WorkTaskWaitInfo{Event: w.EventType, Revision: w.Revision, Resolved: w.ResolvedBy.Valid, AlertMain: w.Alert == 1})
			}
		}
		detail.Links = append(detail.Links, link)
	}
	return detail, tx.Commit()
}

func readTimeline(ctx context.Context, q *sqlc.Queries, taskID string, a WorkTaskReadArgs, detail *WorkTaskDetail) error {
	limit := min(cmp.Or(a.Limit, 20), 100)
	bootstrap := a.AfterSeq == nil && len(a.Tags) == 0
	tags, err := json.Marshal(a.Tags)
	if err != nil {
		return err
	}
	params := sqlc.ListWorkTaskTimelineParams{TaskID: taskID, AfterSeq: detail.NextCursor, Tags: string(tags), MaxRows: limit + 1}
	if bootstrap {
		params.Bootstrap = 1
	}
	entries, err := q.ListWorkTaskTimeline(ctx, params)
	if err != nil {
		return err
	}
	remaining := bootstrapBudget - 2 // JSON array brackets
	for _, row := range entries[:min(int(limit), len(entries))] {
		entry := entryInfo(row)
		if bootstrap {
			encoded, err := json.Marshal(entry)
			if err != nil {
				return err
			}
			size := len(encoded) + 1 // Include space for a separator.
			if size > remaining {
				continue
			}
			remaining -= size
		}
		detail.NextCursor = max(detail.NextCursor, entry.Seq)
		detail.Entries = append(detail.Entries, entry)
	}
	if bootstrap {
		slices.SortFunc(detail.Entries, func(a, b WorkTaskEntryInfo) int { return cmp.Compare(a.Seq, b.Seq) })
	}
	detail.HasMore = len(entries) > len(detail.Entries)
	return nil
}

func entryInfo(e sqlc.ListWorkTaskTimelineRow) WorkTaskEntryInfo {
	info := WorkTaskEntryInfo{Seq: e.Seq, Trust: e.Trust, CreatedAt: e.CreatedAt}
	if e.Trust == worktask.TrustExternal {
		info.Kind = worktask.KindExternal
		info.EventType, info.SourceURL, info.AuthorClass = e.EventType, e.SourceUrl, e.AuthorAssociation
		return info
	}
	info.ID, info.Kind, info.Tag = e.ID, e.Kind, e.Tag
	if e.Trust == worktask.TrustOwner {
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

func summary(task sqlc.WorkTask) WorkTaskInfo {
	return WorkTaskInfo{ID: task.ID, Key: worktask.FormatKey(task.Number), Title: task.Title, Status: task.Status, UpdatedAt: task.UpdatedAt,
		AssigneeBotID: task.AssigneeBotID, ReviewerKind: task.ReviewerKind, ReviewerID: task.ReviewerID, ReviewSeq: task.ReviewSeq}
}

func taskInfo(ctx context.Context, q *sqlc.Queries, task sqlc.WorkTask) (WorkTaskInfo, error) {
	info, _, err := taskInfoAndBlockers(ctx, q, task)
	return info, err
}

func taskInfoAndBlockers(ctx context.Context, q *sqlc.Queries, task sqlc.WorkTask) (WorkTaskInfo, []worktask.Blocker, error) {
	info := summary(task)
	title, err := worktask.ModelTitle(ctx, q, task)
	if err != nil {
		return info, nil, err
	}
	info.Title = title
	sessions, err := q.ListWorkTaskSessions(ctx, task.ID)
	if err != nil {
		return info, nil, err
	}
	blockers, err := worktask.ComputeBlockers(ctx, q, task)
	if err != nil {
		return info, nil, err
	}
	info.Blocked = len(blockers) > 0
	for _, s := range sessions {
		link := WorkTaskSession{ID: strconv.FormatInt(s.ConversationID, 10), Title: s.Title, BotID: s.AgentID, BotName: s.AgentName, IsPrimary: s.IsPrimary == 1, LinkedAt: s.LinkedAt}
		info.Sessions = append(info.Sessions, link)
		if link.IsPrimary {
			info.Session = new(link)
		}
	}
	return info, blockers, nil
}
