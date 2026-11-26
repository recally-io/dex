package task

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// Ledger applies Task changes inside an existing write transaction. The caller
// must hold the writer before it reads a Task and abort the transaction on error.
// Call Service.Notify only after the transaction commits.
type Ledger struct {
	q            *sqlc.Queries
	entryIDs     []int64
	wakeSessions []int64
}

func NewLedger(q *sqlc.Queries) *Ledger { return &Ledger{q: q} }

func (l *Ledger) append(ctx context.Context, actor Actor, task sqlc.Task, status, title string, reviewSeq int64, entry sqlc.InsertTaskEntryParams) (sqlc.Task, sqlc.TaskEntry, error) {
	updated, err := l.q.AdvanceTask(ctx, sqlc.AdvanceTaskParams{
		ID: task.ID, Revision: task.Revision, At: entry.CreatedAt, Status: status, Title: title, ReviewSeq: reviewSeq,
	})
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrConflict
	}
	if err != nil {
		return sqlc.Task{}, sqlc.TaskEntry{}, err
	}
	entry.TaskID = task.ID
	entry.Seq = task.NextEntrySeq
	entry.ActorKind = actor.kind
	entry.ActorBotID = actor.botID
	entry.ActorExecutionID = actor.executionID
	switch actor.kind {
	case ActorBot, ActorExternalAgent:
		entry.Trust = TrustAgent
	case ActorExternal:
		entry.Trust = TrustExternal
	default:
		entry.Trust = TrustOwner
	}
	if entry.MetaJson == "" {
		entry.MetaJson = "{}"
	}
	row, err := l.q.InsertTaskEntry(ctx, entry)
	if err != nil {
		return sqlc.Task{}, sqlc.TaskEntry{}, err
	}
	l.entryIDs = append(l.entryIDs, row.ID)
	return updated, row, nil
}

func (l *Ledger) record(ctx context.Context, actor Actor, task sqlc.Task, entry sqlc.InsertTaskEntryParams) (sqlc.TaskEntry, error) {
	_, row, err := l.append(ctx, actor, task, task.Status, task.Title, task.ReviewSeq, entry)
	return row, err
}

type CreateTaskParams struct {
	AssigneeBotID string
	Title         string
	Note          string
	Status        string
	ReviewerKind  string
	ReviewerID    string
}

func (l *Ledger) CreateTask(ctx context.Context, actor Actor, origin Origin, p CreateTaskParams, now int64) (sqlc.Task, error) {
	if err := authorize(ctx, l.q, actor, origin, sqlc.Task{}, ActionCreateTopLevel); err != nil {
		return sqlc.Task{}, err
	}
	if p.ReviewerKind == "" && p.ReviewerID == "" && actor.isBot() && actor.botID != p.AssigneeBotID {
		p.ReviewerKind, p.ReviewerID = ActorBot, actor.botID
	}
	return l.createTask(ctx, actor, p, sqlc.Task{}, now)
}

func (l *Ledger) createTask(ctx context.Context, actor Actor, p CreateTaskParams, parent sqlc.Task, now int64) (sqlc.Task, error) {
	var err error
	if p.Title, err = Title(p.Title); err != nil {
		return sqlc.Task{}, err
	}
	if p.Note != "" {
		if p.Note, err = Content(p.Note); err != nil {
			return sqlc.Task{}, err
		}
	}
	switch p.Status {
	case "":
		p.Status = StatusReady
	case StatusBacklog, StatusReady, StatusInProgress:
	default:
		return sqlc.Task{}, ErrInvalid
	}
	if err := checkTaskReviewer(ctx, l.q, p.ReviewerKind, p.ReviewerID); err != nil {
		return sqlc.Task{}, err
	}
	id := uuid.NewV7().String()
	root := sql.NullString{String: id, Valid: true}
	if parent.ID != "" {
		root = parent.AuthorizedByRootID
	}
	task, err := l.createProjection(ctx, sqlc.CreateTaskParams{
		ID: id, BotID: p.AssigneeBotID, Title: p.Title, At: now, Status: p.Status,
		CreatorKind: actor.kind, CreatorID: actor.botID, AuthorizedByRootID: root,
		ReviewerKind: p.ReviewerKind, ReviewerID: p.ReviewerID,
		ParentTaskID: sql.NullString{String: parent.ID, Valid: parent.ID != ""},
	})
	if err != nil {
		return sqlc.Task{}, err
	}
	entry := sqlc.InsertTaskEntryParams{Kind: KindStatus, MetaJson: statusMeta(task.Status), CreatedAt: now}
	if p.Note != "" {
		entry.Kind, entry.Content, entry.MetaJson = KindNote, p.Note, "{}"
	}
	updated, _, err := l.append(ctx, actor, task, task.Status, task.Title, task.ReviewSeq, entry)
	return updated, err
}

func validReviewer(kind, id string) bool {
	return (kind == "" || kind == ActorOwner) && id == "" || kind == ActorBot && id != ""
}

func (l *Ledger) createProjection(ctx context.Context, p sqlc.CreateTaskParams) (sqlc.Task, error) {
	// Check the Assignee before the allocation; Source rules can fall back to an unmatched event.
	if _, err := l.q.GetActiveTaskAssignee(ctx, p.BotID); err != nil {
		return sqlc.Task{}, err
	}
	n, err := l.q.AllocateTaskNumber(ctx)
	if err != nil {
		return sqlc.Task{}, err
	}
	p.Number = n
	return l.q.CreateTask(ctx, p)
}

// statusMeta takes only canonical status constants.
func statusMeta(status string) string { return `{"status":"` + status + `"}` }

type PostEntryParams struct {
	Kind    string
	Tag     string
	Content string
}

func (l *Ledger) PostEntry(ctx context.Context, actor Actor, origin Origin, task sqlc.Task, p PostEntryParams, now int64) (sqlc.TaskEntry, error) {
	if err := authorize(ctx, l.q, actor, origin, task, ActionPost); err != nil {
		return sqlc.TaskEntry{}, err
	}
	if p.Kind == "" {
		p.Kind = KindNote
	}
	if p.Kind != KindNote && p.Kind != KindArtifact {
		return sqlc.TaskEntry{}, ErrInvalid
	}
	switch p.Tag {
	case "", TagBrief, TagDecision, TagHandoff, TagUpdate:
	default:
		return sqlc.TaskEntry{}, ErrInvalid
	}
	content, err := Content(p.Content)
	if err != nil {
		return sqlc.TaskEntry{}, err
	}
	return l.record(ctx, actor, task, sqlc.InsertTaskEntryParams{Kind: p.Kind, Tag: p.Tag, Content: content, CreatedAt: now})
}

func (l *Ledger) changeStatus(ctx context.Context, actor Actor, task sqlc.Task, status string, now int64) (sqlc.Task, error) {
	reviewSeq := int64(0)
	if status == StatusInReview {
		reviewSeq = task.NextEntrySeq
	}
	updated, _, err := l.append(ctx, actor, task, status, task.Title, reviewSeq,
		sqlc.InsertTaskEntryParams{Kind: KindStatus, MetaJson: statusMeta(status), CreatedAt: now})
	if err == nil {
		err = l.notifyDependents(ctx, task, updated, now)
	}
	return updated, err
}

// Rename changes the title without changing the Task status or review version.
func (l *Ledger) Rename(ctx context.Context, actor Actor, origin Origin, task sqlc.Task, title string, now int64) (sqlc.Task, error) {
	if err := authorize(ctx, l.q, actor, origin, task, ActionRename); err != nil {
		return sqlc.Task{}, err
	}
	updated, _, err := l.append(ctx, actor, task, task.Status, title, task.ReviewSeq,
		sqlc.InsertTaskEntryParams{Kind: KindStatus, MetaJson: statusMeta(task.Status), Content: title, CreatedAt: now})
	return updated, err
}

// RecordAssociation records a link already authorized by the caller.
func (l *Ledger) RecordAssociation(ctx context.Context, actor Actor, task sqlc.Task, content string, now int64) error {
	_, err := l.record(ctx, actor, task, sqlc.InsertTaskEntryParams{
		Kind: KindArtifact, Content: content, CreatedAt: now,
	})
	return err
}

// RecordOwnerMessage retains the exact text of a Main submission in the timeline.
func (l *Ledger) RecordOwnerMessage(ctx context.Context, task sqlc.Task, content string, now int64) error {
	if content == "" {
		return l.RecordAssociation(ctx, Owner, task, "Input linked to Task", now)
	}
	_, err := l.record(ctx, Owner, task, sqlc.InsertTaskEntryParams{Kind: KindNote, Content: content, CreatedAt: now})
	return err
}

func (l *Ledger) recordExternal(ctx context.Context, task sqlc.Task, content, eventID string, now int64) error {
	_, err := l.record(ctx, Source, task, sqlc.InsertTaskEntryParams{
		Kind: KindExternal, Content: content, CreatedAt: now, EventID: sql.NullString{String: eventID, Valid: true},
	})
	return err
}

// LinkSession links an authorized Session and, if requested, makes it primary.
// It leaves a replay of an existing link and role unchanged.
func (l *Ledger) LinkSession(ctx context.Context, actor Actor, task sqlc.Task, sessionID int64, promote bool, now int64) error {
	exec, err := l.q.LinkTaskSession(ctx, sqlc.LinkTaskSessionParams{TaskID: task.ID, SessionID: sessionID, LinkedAt: now})
	added := err == nil
	if errors.Is(err, sql.ErrNoRows) {
		exec, err = l.q.GetTaskSession(ctx, sqlc.GetTaskSessionParams{TaskID: task.ID, SessionID: sessionID})
	}
	if err != nil {
		return err
	}
	// A Task can link to a child after its first turn has already settled.
	if err := l.q.ProjectTaskChildSession(ctx, sessionID); err != nil {
		return err
	}
	changePrimary := promote && exec.IsPrimary == 0
	if !added && !changePrimary {
		return nil
	}
	if changePrimary {
		if err := l.q.ClearTaskPrimarySession(ctx, task.ID); err != nil {
			return err
		}
		if err := l.q.PromoteTaskSession(ctx, exec.ID); err != nil {
			return err
		}
	}
	meta, err := json.Marshal(struct {
		ExecutionID string `json:"executionID"`
		SessionID   int64  `json:"conversationID"`
		Primary     bool   `json:"primary"`
	}{exec.ID, sessionID, changePrimary || exec.IsPrimary == 1})
	if err != nil {
		return err
	}
	_, err = l.record(ctx, actor, task, sqlc.InsertTaskEntryParams{Kind: KindExecution, MetaJson: string(meta), CreatedAt: now})
	return err
}
