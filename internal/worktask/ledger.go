package worktask

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"strconv"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// Ledger applies Task changes inside an existing write transaction. The caller
// must hold the writer before it reads a Task and abort the transaction on error.
// Call Service.Notify only after the transaction commits.
type Ledger struct {
	q                 *sqlc.Queries
	entryIDs          []int64
	wakeConversations []int64
}

func NewLedger(q *sqlc.Queries) *Ledger { return &Ledger{q: q} }

func (l *Ledger) append(ctx context.Context, actor Actor, task sqlc.WorkTask, status, title string, reviewSeq int64, entry sqlc.InsertWorkTaskEntryParams) (sqlc.WorkTask, sqlc.WorkTaskEntry, error) {
	updated, err := l.q.AdvanceWorkTask(ctx, sqlc.AdvanceWorkTaskParams{
		ID: task.ID, Revision: task.Revision, At: entry.CreatedAt, Status: status, Title: title, ReviewSeq: reviewSeq,
	})
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrConflict
	}
	if err != nil {
		return sqlc.WorkTask{}, sqlc.WorkTaskEntry{}, err
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
	row, err := l.q.InsertWorkTaskEntry(ctx, entry)
	if err != nil {
		return sqlc.WorkTask{}, sqlc.WorkTaskEntry{}, err
	}
	l.entryIDs = append(l.entryIDs, row.ID)
	return updated, row, nil
}

func (l *Ledger) record(ctx context.Context, actor Actor, task sqlc.WorkTask, entry sqlc.InsertWorkTaskEntryParams) (sqlc.WorkTaskEntry, error) {
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

func (l *Ledger) CreateTask(ctx context.Context, actor Actor, origin Origin, p CreateTaskParams, now int64) (sqlc.WorkTask, error) {
	if err := authorize(ctx, l.q, actor, origin, sqlc.WorkTask{}, ActionCreateTopLevel); err != nil {
		return sqlc.WorkTask{}, err
	}
	if p.ReviewerKind == "" && p.ReviewerID == "" && actor.isBot() && actor.botID != p.AssigneeBotID {
		p.ReviewerKind, p.ReviewerID = ActorBot, actor.botID
	}
	return l.createTask(ctx, actor, p, sqlc.WorkTask{}, now)
}

func (l *Ledger) createTask(ctx context.Context, actor Actor, p CreateTaskParams, parent sqlc.WorkTask, now int64) (sqlc.WorkTask, error) {
	var err error
	if p.Title, err = Title(p.Title); err != nil {
		return sqlc.WorkTask{}, err
	}
	if p.Note != "" {
		if p.Note, err = Content(p.Note); err != nil {
			return sqlc.WorkTask{}, err
		}
	}
	switch p.Status {
	case "":
		p.Status = StatusReady
	case StatusBacklog, StatusReady, StatusInProgress:
	default:
		return sqlc.WorkTask{}, ErrInvalid
	}
	if err := checkBoardReviewer(ctx, l.q, p.ReviewerKind, p.ReviewerID); err != nil {
		return sqlc.WorkTask{}, err
	}
	id := uuid.NewV7().String()
	root := sql.NullString{String: id, Valid: true}
	if parent.ID != "" {
		root = parent.AuthorizedByRootID
	}
	task, err := l.createProjection(ctx, sqlc.CreateWorkTaskParams{
		ID: id, AgentID: p.AssigneeBotID, Title: p.Title, At: now, Status: p.Status,
		CreatorKind: actor.kind, CreatorID: actor.botID, AuthorizedByRootID: root,
		ReviewerKind: p.ReviewerKind, ReviewerID: p.ReviewerID,
		ParentTaskID: sql.NullString{String: parent.ID, Valid: parent.ID != ""},
	})
	if err != nil {
		return sqlc.WorkTask{}, err
	}
	entry := sqlc.InsertWorkTaskEntryParams{Kind: KindStatus, MetaJson: statusMeta(task.Status), CreatedAt: now}
	if p.Note != "" {
		entry.Kind, entry.Content, entry.MetaJson = KindNote, p.Note, "{}"
	}
	updated, _, err := l.append(ctx, actor, task, task.Status, task.Title, task.ReviewSeq, entry)
	return updated, err
}

func validReviewer(kind, id string) bool {
	return (kind == "" || kind == ActorOwner) && id == "" || kind == ActorBot && id != ""
}

func (l *Ledger) createProjection(ctx context.Context, p sqlc.CreateWorkTaskParams) (sqlc.WorkTask, error) {
	// Check the Assignee before the allocation; Source rules can fall back to an unmatched event.
	if _, err := l.q.GetActiveWorkTaskAssignee(ctx, p.AgentID); err != nil {
		return sqlc.WorkTask{}, err
	}
	n, err := l.q.AllocateWorkTaskNumber(ctx)
	if err != nil {
		return sqlc.WorkTask{}, err
	}
	p.Number = n
	return l.q.CreateWorkTask(ctx, p)
}

// statusMeta takes only canonical status constants.
func statusMeta(status string) string { return `{"status":"` + status + `"}` }

type PostEntryParams struct {
	Kind    string
	Tag     string
	Content string
}

func (l *Ledger) PostEntry(ctx context.Context, actor Actor, origin Origin, task sqlc.WorkTask, p PostEntryParams, now int64) (sqlc.WorkTaskEntry, error) {
	if err := authorize(ctx, l.q, actor, origin, task, ActionPost); err != nil {
		return sqlc.WorkTaskEntry{}, err
	}
	if p.Kind == "" {
		p.Kind = KindNote
	}
	if p.Kind != KindNote && p.Kind != KindArtifact {
		return sqlc.WorkTaskEntry{}, ErrInvalid
	}
	switch p.Tag {
	case "", TagBrief, TagDecision, TagHandoff, TagUpdate:
	default:
		return sqlc.WorkTaskEntry{}, ErrInvalid
	}
	content, err := Content(p.Content)
	if err != nil {
		return sqlc.WorkTaskEntry{}, err
	}
	return l.record(ctx, actor, task, sqlc.InsertWorkTaskEntryParams{Kind: p.Kind, Tag: p.Tag, Content: content, CreatedAt: now})
}

// SetLegacyLifecycle preserves the three lifecycle decisions an Owner could make
// before the Board. The caller must already have authenticated the Owner input.
func (l *Ledger) SetLegacyLifecycle(ctx context.Context, task sqlc.WorkTask, lifecycle string, now int64) (sqlc.WorkTask, error) {
	status, err := StatusFromLegacyLifecycle(lifecycle)
	if err != nil || LegacyLifecycle(task.Status) == lifecycle {
		return task, err
	}
	return l.changeStatus(ctx, Owner, task, status, now)
}

func (l *Ledger) changeStatus(ctx context.Context, actor Actor, task sqlc.WorkTask, status string, now int64) (sqlc.WorkTask, error) {
	reviewSeq := int64(0)
	if status == StatusInReview {
		reviewSeq = task.NextEntrySeq
	}
	updated, _, err := l.append(ctx, actor, task, status, task.Title, reviewSeq,
		sqlc.InsertWorkTaskEntryParams{Kind: KindStatus, MetaJson: statusMeta(status), CreatedAt: now})
	if err == nil {
		err = l.notifyDependents(ctx, task, updated, now)
	}
	return updated, err
}

// Rename changes the title without changing the Task status or review version.
func (l *Ledger) Rename(ctx context.Context, actor Actor, origin Origin, task sqlc.WorkTask, title string, now int64) (sqlc.WorkTask, error) {
	if err := authorize(ctx, l.q, actor, origin, task, ActionRename); err != nil {
		return sqlc.WorkTask{}, err
	}
	updated, _, err := l.append(ctx, actor, task, task.Status, title, task.ReviewSeq,
		sqlc.InsertWorkTaskEntryParams{Kind: KindStatus, MetaJson: statusMeta(task.Status), Content: title, CreatedAt: now})
	return updated, err
}

// RecordAssociation records a link already authorized by the legacy caller.
func (l *Ledger) RecordAssociation(ctx context.Context, actor Actor, task sqlc.WorkTask, content string, now int64) error {
	_, err := l.record(ctx, actor, task, sqlc.InsertWorkTaskEntryParams{
		Kind: KindArtifact, MetaJson: `{"legacyHidden":true}`, Content: content, CreatedAt: now,
	})
	return err
}

// RecordOwnerMessage retains the exact text of a Main submission in the timeline.
func (l *Ledger) RecordOwnerMessage(ctx context.Context, task sqlc.WorkTask, content string, now int64) error {
	if content == "" {
		return l.RecordAssociation(ctx, Owner, task, "Input linked to Task", now)
	}
	_, err := l.record(ctx, Owner, task, sqlc.InsertWorkTaskEntryParams{Kind: KindNote, Content: content, CreatedAt: now})
	return err
}

func (l *Ledger) recordExternal(ctx context.Context, task sqlc.WorkTask, content, eventID string, now int64) error {
	_, err := l.record(ctx, Source, task, sqlc.InsertWorkTaskEntryParams{
		Kind: KindExternal, Content: content, CreatedAt: now, EventID: sql.NullString{String: eventID, Valid: true},
	})
	return err
}

// LinkSession links an authorized Session and, if requested, makes it primary.
// It leaves a replay of an existing link and role unchanged.
func (l *Ledger) LinkSession(ctx context.Context, actor Actor, task sqlc.WorkTask, conversationID int64, promote bool, now int64) error {
	exec, err := l.q.LinkWorkTaskSession(ctx, sqlc.LinkWorkTaskSessionParams{TaskID: task.ID, ConversationID: conversationID, LinkedAt: now})
	added := err == nil
	if errors.Is(err, sql.ErrNoRows) {
		exec, err = l.q.GetWorkTaskSession(ctx, sqlc.GetWorkTaskSessionParams{TaskID: task.ID, ConversationID: sql.NullInt64{Int64: conversationID, Valid: true}})
	}
	if err != nil {
		return err
	}
	// A Task can link to a child after its first turn has already settled.
	if err := l.q.ProjectTaskChildExecution(ctx, conversationID); err != nil {
		return err
	}
	changePrimary := promote && exec.IsPrimary == 0
	if !added && !changePrimary {
		return nil
	}
	if changePrimary {
		if err := l.q.ClearWorkTaskPrimarySession(ctx, task.ID); err != nil {
			return err
		}
		if err := l.q.PromoteWorkTaskSession(ctx, exec.ID); err != nil {
			return err
		}
	}
	meta, err := json.Marshal(struct {
		ExecutionID    string `json:"executionID"`
		ConversationID int64  `json:"conversationID"`
		Primary        bool   `json:"primary"`
	}{exec.ID, conversationID, changePrimary || exec.IsPrimary == 1})
	if err != nil {
		return err
	}
	_, err = l.record(ctx, actor, task, sqlc.InsertWorkTaskEntryParams{Kind: KindExecution, MetaJson: string(meta), CreatedAt: now})
	return err
}

func tombstoneMeta(seq int64) string { return `{"targetSeq":` + strconv.FormatInt(seq, 10) + `}` }
