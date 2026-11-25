// Package tools implements the task_* tools of Main and writable Chats.
package tools

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/worktask"
)

// ErrInvalid means the tool call cannot change this Task. The message tells the model why.
var ErrInvalid = errors.New("invalid task tool call")

const (
	timelineEntries = 20
	// An entry can have 32000 characters. The model reads a short head and the owner reads the rest.
	entryPreview = 2000
)

// Service checks the caller and applies each change with its replay record in one transaction.
type Service struct {
	db    *sql.DB
	q     *sqlc.Queries
	now   func() time.Time
	tasks *worktask.Service
}

func New(conn *sql.DB) *Service {
	return &Service{db: conn, q: sqlc.New(conn), now: time.Now}
}

// ConfigureWorkTasks binds Task entry notifications before the tools start.
func (s *Service) ConfigureWorkTasks(tasks *worktask.Service) {
	s.tasks = tasks
}

// Handlers binds the engine tools to this service. links are the tools of the enabled plugins.
func (s *Service) Handlers(links []worktask.LinkTool) WorkTaskTools {
	tools := WorkTaskTools{List: s.List, Get: s.Get, New: s.New, Update: s.Update, Append: s.Append}
	for _, link := range links {
		tools.Links = append(tools.Links, WorkTaskLinkTool{
			Name: link.Name, Description: link.Description, Rules: link.Rules, Properties: link.Properties, Required: link.Required,
			Invoke: func(ctx context.Context, c engine.TaskContext, taskID string, args []byte) (WorkTaskInfo, error) {
				l, err := link.Parse(args)
				if err != nil {
					return WorkTaskInfo{}, invalid("%v", err)
				}
				return s.Link(ctx, c, link.Name, taskID, l)
			},
		})
	}
	return tools
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, args...)...)
}

// caller returns the conversation and the input of the run. Only Main and writable Chats have the tools.
func caller(ctx context.Context, q *sqlc.Queries, c engine.TaskContext) (sqlc.Conversation, sqlc.Submission, error) {
	conv, err := q.GetConversationByExecution(ctx, sqlc.GetConversationByExecutionParams{AgentID: c.AgentID, ExecutionID: sql.NullInt64{Int64: c.ConversationID, Valid: true}})
	if err != nil {
		return conv, sqlc.Submission{}, err
	}
	if !chat.Writable(conv.ChannelKey) {
		return conv, sqlc.Submission{}, invalid("Tasks need main or a writable Chat")
	}
	source, err := q.GetSubmission(ctx, c.SourceRequestID)
	if err != nil {
		return conv, source, err
	}
	if source.ConversationID != conv.ID {
		return conv, source, invalid("the run does not belong to this conversation")
	}
	return conv, source, nil
}

// fromUser reports whether a person wrote the input that started the run. A session result, a
// monitor, or a routine is not a user request.
func fromUser(source sqlc.Submission) bool {
	return source.Kind == "message" && chat.UserOrigin(source.Origin)
}

func taskOrigin(source sqlc.Submission) worktask.Origin {
	switch {
	case fromUser(source):
		return worktask.OriginUser
	case source.Kind == "task_wake":
		return worktask.OriginTaskWake
	case source.Kind == "task_result" && source.Producer == chat.ProducerSubSession:
		return worktask.OriginTaskResult
	default:
		return worktask.OriginOther
	}
}

func (s *Service) List(ctx context.Context, c engine.TaskContext, a WorkTaskListArgs) ([]WorkTaskInfo, error) {
	if a.Lifecycle != "" && a.Lifecycle != "open" && a.Lifecycle != "all" {
		return nil, invalid("lifecycle is open or all")
	}
	conv, _, err := caller(ctx, s.q, c)
	if err != nil {
		return nil, err
	}
	params := sqlc.ListAgentWorkTasksParams{AgentID: c.AgentID, IncludeClosed: a.Lifecycle == "all"}
	if conv.ParentID.Valid {
		params.ChildConversationID = conv.ID
	}
	rows, err := s.q.ListAgentWorkTasks(ctx, params)
	if err != nil {
		return nil, err
	}
	out := make([]WorkTaskInfo, 0, len(rows))
	for _, row := range rows {
		info := WorkTaskInfo{ID: row.ID, Title: row.Title, Lifecycle: worktask.LegacyLifecycle(row.Status), UpdatedAt: row.UpdatedAt}
		if row.SessionID != 0 {
			info.Session = &WorkTaskSession{ID: strconv.FormatInt(row.SessionID, 10), IsPrimary: true}
		}
		out = append(out, info)
	}
	return out, nil
}

func (s *Service) Get(ctx context.Context, c engine.TaskContext, id string) (WorkTaskDetail, error) {
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
	task, err := readableTask(ctx, q, conv, id)
	if err != nil {
		return WorkTaskDetail{}, err
	}
	info, err := taskInfo(ctx, q, task)
	if err != nil {
		return WorkTaskDetail{}, err
	}
	detail := WorkTaskDetail{WorkTaskInfo: info, Entries: []WorkTaskEntryInfo{}, Questions: []WorkTaskQuestionInfo{}, Links: []WorkTaskLinkInfo{}}
	entries, err := q.ListWorkTaskTimeline(ctx, sqlc.ListWorkTaskTimelineParams{TaskID: task.ID, MaxRows: timelineEntries})
	if err != nil {
		return WorkTaskDetail{}, err
	}
	for _, e := range entries {
		detail.Entries = append(detail.Entries, entryInfo(e))
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
	waits, err := q.ListWorkTaskWaits(ctx, sqlc.ListWorkTaskWaitsParams{AgentID: c.AgentID, TaskID: task.ID})
	if err != nil {
		return WorkTaskDetail{}, err
	}
	for _, object := range objects {
		link := WorkTaskLinkInfo{Namespace: object.Namespace, Type: object.ObjectType, Key: object.ObjectKey, Waits: []WorkTaskWaitInfo{}}
		for _, w := range waits {
			if w.Namespace == object.Namespace && w.ObjectType == object.ObjectType && w.ObjectKey == object.ObjectKey {
				link.Waits = append(link.Waits, WorkTaskWaitInfo{Event: w.EventType, Revision: w.Revision, Resolved: w.ResolvedBy.Valid, AlertMain: w.Alert == 1})
			}
		}
		detail.Links = append(detail.Links, link)
	}
	return detail, tx.Commit()
}

func entryInfo(e sqlc.ListWorkTaskTimelineRow) WorkTaskEntryInfo {
	info := WorkTaskEntryInfo{ID: e.ID, CreatedAt: e.CreatedAt, Author: worktask.LegacyAuthor(e.Trust, e.MetaJson)}
	if e.Trust == worktask.TrustExternal {
		// External text stays out of the model context. The owner reads it in the Inbox.
		info.EventType, info.SourceURL, info.AuthorClass = e.EventType, e.SourceUrl, e.AuthorAssociation
		return info
	}
	info.Content = e.Content
	if utf8.RuneCountInString(e.Content) > entryPreview {
		info.Content, info.Truncated = string([]rune(e.Content)[:entryPreview]), true
	}
	return info
}

func taskInfo(ctx context.Context, q *sqlc.Queries, task sqlc.WorkTask) (WorkTaskInfo, error) {
	info := WorkTaskInfo{ID: task.ID, Title: task.Title, Lifecycle: worktask.LegacyLifecycle(task.Status), UpdatedAt: task.UpdatedAt}
	sessions, err := q.ListWorkTaskSessions(ctx, task.ID)
	if err != nil {
		return info, err
	}
	for _, s := range sessions {
		link := WorkTaskSession{ID: strconv.FormatInt(s.ConversationID, 10), Title: s.Title, BotID: s.AgentID, BotName: s.AgentName, IsPrimary: s.IsPrimary == 1, LinkedAt: s.LinkedAt}
		info.Sessions = append(info.Sessions, link)
		if link.IsPrimary {
			info.Session = new(link)
		}
	}
	return info, nil
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return invalid("no Task of this bot has this ID; call task_list")
	}
	return err
}

// readableTask applies the central ACL without widening the legacy assignee scope.
func readableTask(ctx context.Context, q *sqlc.Queries, conv sqlc.Conversation, id string) (sqlc.WorkTask, error) {
	task, err := q.GetWorkTask(ctx, sqlc.GetWorkTaskParams{ID: id, AgentID: conv.AgentID})
	if err != nil {
		return task, notFound(err)
	}
	allowed, err := worktask.CanRead(ctx, q, worktask.BotInConversation(conv), task)
	if err == nil && !allowed {
		err = notFound(sql.ErrNoRows)
	}
	return task, err
}

// mutate runs one change and stores its result under the durable key of the tool call. A replay
// of the call returns the stored result.
func mutate[R any](ctx context.Context, s *Service, c engine.TaskContext, action string, change func(*sqlc.Queries, *worktask.Ledger, sqlc.Conversation, sqlc.Submission, int64) (R, error)) (R, error) {
	var zero R
	if c.Key == "" || !strings.HasPrefix(c.Key, c.AgentID+"/") {
		return zero, invalid("a durable tool key is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	if err := q.LockWorkTaskWriter(ctx); err != nil {
		return zero, err
	}
	conv, source, err := caller(ctx, q, c)
	if err != nil {
		return zero, err
	}
	prior, err := q.GetWorkTaskToolOperation(ctx, c.Key)
	if err == nil {
		if prior.AgentID != c.AgentID || prior.ConversationID != conv.ID || prior.SourceRequestID != source.ID || prior.Action != action {
			return zero, invalid("the tool key belongs to another operation")
		}
		var out R
		return out, json.Unmarshal([]byte(prior.Result), &out)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return zero, err
	}
	ledger := worktask.NewLedger(q)
	result, err := change(q, ledger, conv, source, s.now().UnixMilli())
	if err != nil {
		return zero, err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return zero, err
	}
	if err := q.CreateWorkTaskToolOperation(ctx, sqlc.CreateWorkTaskToolOperationParams{Key: c.Key, AgentID: c.AgentID, ConversationID: conv.ID, SourceRequestID: source.ID, Action: action, Result: string(data)}); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	s.tasks.Notify(ledger)
	return result, nil
}

func (s *Service) New(ctx context.Context, c engine.TaskContext, a WorkTaskNewArgs) (WorkTaskInfo, error) {
	title, err := worktask.Title(a.Title)
	if err != nil {
		return WorkTaskInfo{}, invalid("the title has 1 to 160 characters")
	}
	note := strings.TrimSpace(a.Note)
	if note != "" {
		if note, err = worktask.Content(note); err != nil {
			return WorkTaskInfo{}, invalid("the note has at most 32000 characters")
		}
	}
	return mutate(ctx, s, c, "new", func(_ *sqlc.Queries, l *worktask.Ledger, conv sqlc.Conversation, source sqlc.Submission, now int64) (WorkTaskInfo, error) {
		if !fromUser(source) {
			return WorkTaskInfo{}, invalid("create a Task only for a user request; this run started from %s", source.Kind)
		}
		task, err := l.CreateTask(ctx, worktask.BotInConversation(conv), taskOrigin(source), worktask.CreateTaskParams{
			AssigneeBotID: conv.AgentID, Title: title, Note: note,
		}, now)
		if err != nil {
			return WorkTaskInfo{}, err
		}
		return WorkTaskInfo{ID: task.ID, Title: task.Title, Lifecycle: worktask.LegacyLifecycle(task.Status), UpdatedAt: task.UpdatedAt}, nil
	})
}

func (s *Service) Append(ctx context.Context, c engine.TaskContext, a WorkTaskAppendArgs) (WorkTaskEntryInfo, error) {
	content, err := worktask.Content(a.Content)
	if err != nil {
		return WorkTaskEntryInfo{}, invalid("the content has 1 to 32000 characters")
	}
	return mutate(ctx, s, c, "append", func(q *sqlc.Queries, l *worktask.Ledger, conv sqlc.Conversation, source sqlc.Submission, now int64) (WorkTaskEntryInfo, error) {
		task, err := readableTask(ctx, q, conv, a.ID)
		if err != nil {
			return WorkTaskEntryInfo{}, err
		}
		entry, err := l.PostEntry(ctx, worktask.BotInConversation(conv), taskOrigin(source), task, worktask.PostEntryParams{Kind: worktask.KindNote, Content: content}, now)
		if err != nil {
			return WorkTaskEntryInfo{}, err
		}
		return WorkTaskEntryInfo{ID: entry.ID, Author: worktask.LegacyAuthor(entry.Trust, entry.MetaJson), CreatedAt: entry.CreatedAt}, nil
	})
}

func (s *Service) Update(ctx context.Context, c engine.TaskContext, a WorkTaskUpdateArgs) (WorkTaskInfo, error) {
	var title string
	if a.Title != "" {
		var err error
		if title, err = worktask.Title(a.Title); err != nil {
			return WorkTaskInfo{}, invalid("the title has 1 to 160 characters")
		}
	}
	switch a.Lifecycle {
	case "", "open", "completed", "cancelled":
	default:
		return WorkTaskInfo{}, invalid("lifecycle is open, completed, or cancelled")
	}
	if a.Primary && a.Session == "" {
		return WorkTaskInfo{}, invalid("primary requires a session")
	}
	if title == "" && a.Lifecycle == "" && a.Session == "" {
		return WorkTaskInfo{}, invalid("give at least one field to change")
	}
	return mutate(ctx, s, c, "update", func(q *sqlc.Queries, l *worktask.Ledger, conv sqlc.Conversation, source sqlc.Submission, now int64) (WorkTaskInfo, error) {
		task, err := readableTask(ctx, q, conv, a.ID)
		if err != nil {
			return WorkTaskInfo{}, err
		}
		actor, origin := worktask.BotInConversation(conv), taskOrigin(source)
		changeLifecycle := a.Lifecycle != "" && a.Lifecycle != worktask.LegacyLifecycle(task.Status)
		rename := title != "" && title != task.Title
		if changeLifecycle && !fromUser(source) {
			return WorkTaskInfo{}, invalid("only the user can complete, cancel, or reopen a Task; this run started from %s", source.Kind)
		}
		if changeLifecycle || rename {
			allowed, err := worktask.CanWrite(ctx, q, actor, origin, task, worktask.ActionTransition)
			if err != nil {
				return WorkTaskInfo{}, err
			}
			if !allowed {
				return WorkTaskInfo{}, worktask.ErrForbidden
			}
		}
		if changeLifecycle {
			if task, err = l.SetLegacyLifecycle(ctx, task, a.Lifecycle, now); err != nil {
				return WorkTaskInfo{}, err
			}
		}
		if rename {
			if task, err = l.Rename(ctx, actor, task, title, now); err != nil {
				return WorkTaskInfo{}, err
			}
		}
		if a.Session != "" {
			childID, err := strconv.ParseInt(a.Session, 10, 64)
			if err != nil {
				return WorkTaskInfo{}, invalid("session is the ID of a child session of this conversation")
			}
			if err := worktask.AuthorizeSessionLink(ctx, q, actor, origin, task, childID); err != nil {
				return WorkTaskInfo{}, err
			}
			if err := l.LinkSession(ctx, actor, task, childID, a.Primary, now); err != nil {
				return WorkTaskInfo{}, err
			}
			if task, err = q.GetWorkTask(ctx, sqlc.GetWorkTaskParams{ID: task.ID, AgentID: c.AgentID}); err != nil {
				return WorkTaskInfo{}, err
			}
		}
		return taskInfo(ctx, q, task)
	})
}

// Link writes the link of one plugin tool call with its replay record. A wait starts at the call time.
func (s *Service) Link(ctx context.Context, c engine.TaskContext, tool, taskID string, l worktask.Link) (WorkTaskInfo, error) {
	return mutate(ctx, s, c, tool, func(q *sqlc.Queries, ledger *worktask.Ledger, conv sqlc.Conversation, _ sqlc.Submission, now int64) (WorkTaskInfo, error) {
		task, err := readableTask(ctx, q, conv, taskID)
		if err != nil {
			return WorkTaskInfo{}, err
		}
		n, err := q.LinkWorkTaskObject(ctx, sqlc.LinkWorkTaskObjectParams{AgentID: c.AgentID, TaskID: task.ID, Namespace: l.Namespace, ObjectType: l.Object.Type, ObjectKey: l.Object.Key})
		if err != nil {
			return WorkTaskInfo{}, err
		}
		if n == 0 {
			return WorkTaskInfo{}, invalid("%s is linked to another Task", l.Name)
		}
		if l.WaitEvent != "" {
			if !worktask.IsOpenStatus(task.Status) {
				return WorkTaskInfo{}, invalid("a closed Task cannot wait for %s", l.Name)
			}
			var alert int64
			if l.Alert {
				alert = 1
			}
			if _, err := q.AddWorkTaskWait(ctx, sqlc.AddWorkTaskWaitParams{AgentID: c.AgentID, TaskID: task.ID, Namespace: l.Namespace, ObjectType: l.Object.Type, ObjectKey: l.Object.Key, EventType: l.WaitEvent, Revision: l.WaitRevision, AfterAt: now, Alert: alert}); err != nil {
				return WorkTaskInfo{}, err
			}
		}
		if err := ledger.RecordAssociation(ctx, worktask.BotInConversation(conv), task, "Linked "+l.Name, now); err != nil {
			return WorkTaskInfo{}, err
		}
		if task, err = q.GetWorkTask(ctx, sqlc.GetWorkTaskParams{ID: task.ID, AgentID: c.AgentID}); err != nil {
			return WorkTaskInfo{}, err
		}
		return taskInfo(ctx, q, task)
	})
}
