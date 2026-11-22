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
	"uuid"

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
	db  *sql.DB
	q   *sqlc.Queries
	now func() time.Time
}

func New(conn *sql.DB) *Service {
	return &Service{db: conn, q: sqlc.New(conn), now: time.Now}
}

// Handlers binds the engine tools to this service. links are the tools of the enabled plugins.
func (s *Service) Handlers(links []worktask.LinkTool) engine.WorkTaskTools {
	tools := engine.WorkTaskTools{List: s.List, Get: s.Get, New: s.New, Update: s.Update, Append: s.Append}
	for _, link := range links {
		tools.Links = append(tools.Links, engine.WorkTaskLinkTool{
			Name: link.Name, Description: link.Description, Rules: link.Rules, Properties: link.Properties, Required: link.Required,
			Invoke: func(ctx context.Context, c engine.TaskContext, taskID string, args []byte) (engine.WorkTaskInfo, error) {
				l, err := link.Parse(args)
				if err != nil {
					return engine.WorkTaskInfo{}, invalid("%v", err)
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
	if c.Scope != "" {
		return sqlc.Conversation{}, sqlc.Submission{}, invalid("Tasks are not available in a group")
	}
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

func (s *Service) List(ctx context.Context, c engine.TaskContext, a engine.WorkTaskListArgs) ([]engine.WorkTaskInfo, error) {
	if a.Lifecycle != "" && a.Lifecycle != "open" && a.Lifecycle != "all" {
		return nil, invalid("lifecycle is open or all")
	}
	if _, _, err := caller(ctx, s.q, c); err != nil {
		return nil, err
	}
	rows, err := s.q.ListAgentWorkTasks(ctx, sqlc.ListAgentWorkTasksParams{AgentID: c.AgentID, IncludeClosed: a.Lifecycle == "all"})
	if err != nil {
		return nil, err
	}
	out := make([]engine.WorkTaskInfo, 0, len(rows))
	for _, row := range rows {
		info := engine.WorkTaskInfo{ID: row.ID, Title: row.Title, Lifecycle: row.Lifecycle, UpdatedAt: row.UpdatedAt}
		if row.SessionID != 0 {
			info.Session = &engine.WorkTaskSession{ID: strconv.FormatInt(row.SessionID, 10)}
		}
		out = append(out, info)
	}
	return out, nil
}

func (s *Service) Get(ctx context.Context, c engine.TaskContext, id string) (engine.WorkTaskDetail, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return engine.WorkTaskDetail{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	if _, _, err := caller(ctx, q, c); err != nil {
		return engine.WorkTaskDetail{}, err
	}
	task, err := q.GetWorkTask(ctx, sqlc.GetWorkTaskParams{ID: id, AgentID: c.AgentID})
	if err != nil {
		return engine.WorkTaskDetail{}, notFound(err)
	}
	info, err := taskInfo(ctx, q, task)
	if err != nil {
		return engine.WorkTaskDetail{}, err
	}
	detail := engine.WorkTaskDetail{WorkTaskInfo: info, Entries: []engine.WorkTaskEntryInfo{}, Questions: []engine.WorkTaskQuestionInfo{}, Links: []engine.WorkTaskLinkInfo{}}
	entries, err := q.ListWorkTaskTimeline(ctx, sqlc.ListWorkTaskTimelineParams{TaskID: task.ID, MaxRows: timelineEntries})
	if err != nil {
		return engine.WorkTaskDetail{}, err
	}
	for _, e := range entries {
		detail.Entries = append(detail.Entries, entryInfo(e))
	}
	questions, err := q.ListWorkTaskQuestions(ctx, task.ID)
	if err != nil {
		return engine.WorkTaskDetail{}, err
	}
	for _, question := range questions {
		detail.Questions = append(detail.Questions, engine.WorkTaskQuestionInfo{ID: question.ID, Text: question.Text, Queued: question.Queued != 0})
	}
	objects, err := q.ListWorkTaskObjects(ctx, task.ID)
	if err != nil {
		return engine.WorkTaskDetail{}, err
	}
	waits, err := q.ListWorkTaskWaits(ctx, sqlc.ListWorkTaskWaitsParams{AgentID: c.AgentID, TaskID: task.ID})
	if err != nil {
		return engine.WorkTaskDetail{}, err
	}
	for _, object := range objects {
		link := engine.WorkTaskLinkInfo{Namespace: object.Namespace, Type: object.ObjectType, Key: object.ObjectKey, Waits: []engine.WorkTaskWaitInfo{}}
		for _, w := range waits {
			if w.Namespace == object.Namespace && w.ObjectType == object.ObjectType && w.ObjectKey == object.ObjectKey {
				link.Waits = append(link.Waits, engine.WorkTaskWaitInfo{Event: w.EventType, Revision: w.Revision, Resolved: w.ResolvedBy.Valid, AlertMain: w.Alert == 1})
			}
		}
		detail.Links = append(detail.Links, link)
	}
	return detail, tx.Commit()
}

func entryInfo(e sqlc.ListWorkTaskTimelineRow) engine.WorkTaskEntryInfo {
	info := engine.WorkTaskEntryInfo{ID: e.ID, CreatedAt: e.CreatedAt}
	if e.EventID != "" {
		// External text stays out of the model context. The owner reads it in the Inbox.
		info.Author, info.EventType, info.SourceURL, info.AuthorClass = "external", e.EventType, e.SourceUrl, e.AuthorAssociation
		return info
	}
	info.Author = e.Author
	if info.Author == "" {
		info.Author = "unknown"
	}
	info.Content = e.Content
	if utf8.RuneCountInString(e.Content) > entryPreview {
		info.Content, info.Truncated = string([]rune(e.Content)[:entryPreview]), true
	}
	return info
}

func taskInfo(ctx context.Context, q *sqlc.Queries, task sqlc.WorkTask) (engine.WorkTaskInfo, error) {
	info := engine.WorkTaskInfo{ID: task.ID, Title: task.Title, Lifecycle: task.Lifecycle, UpdatedAt: task.UpdatedAt}
	session, err := q.GetWorkTaskSession(ctx, task.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return info, nil
	}
	if err != nil {
		return info, err
	}
	info.Session = &engine.WorkTaskSession{ID: strconv.FormatInt(session.ID, 10), Title: session.Title, BotID: session.AgentID}
	return info, nil
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return invalid("no Task of this bot has this ID; call task_list")
	}
	return err
}

// mutate runs one change and stores its result under the durable key of the tool call. A replay
// of the call returns the stored result.
func mutate[R any](ctx context.Context, s *Service, c engine.TaskContext, action string, change func(*sqlc.Queries, sqlc.Conversation, sqlc.Submission, int64) (R, error)) (R, error) {
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
	result, err := change(q, conv, source, s.now().UnixMilli())
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
	return result, tx.Commit()
}

func (s *Service) New(ctx context.Context, c engine.TaskContext, a engine.WorkTaskNewArgs) (engine.WorkTaskInfo, error) {
	title, err := worktask.Title(a.Title)
	if err != nil {
		return engine.WorkTaskInfo{}, invalid("the title has 1 to 160 characters")
	}
	note := strings.TrimSpace(a.Note)
	if note != "" {
		if note, err = worktask.Content(note); err != nil {
			return engine.WorkTaskInfo{}, invalid("the note has at most 32000 characters")
		}
	}
	return mutate(ctx, s, c, "new", func(q *sqlc.Queries, _ sqlc.Conversation, source sqlc.Submission, now int64) (engine.WorkTaskInfo, error) {
		if !fromUser(source) {
			return engine.WorkTaskInfo{}, invalid("create a Task only for a user request; this run started from %s", source.Kind)
		}
		task, err := q.CreateWorkTask(ctx, sqlc.CreateWorkTaskParams{ID: uuid.NewV7().String(), AgentID: c.AgentID, Title: title, At: now})
		if err != nil {
			return engine.WorkTaskInfo{}, err
		}
		if note != "" {
			if _, err := q.InsertWorkTaskEntry(ctx, sqlc.InsertWorkTaskEntryParams{TaskID: task.ID, AgentID: c.AgentID, Content: note, CreatedAt: now, Author: worktask.AuthorAgent}); err != nil {
				return engine.WorkTaskInfo{}, err
			}
		}
		return engine.WorkTaskInfo{ID: task.ID, Title: task.Title, Lifecycle: task.Lifecycle, UpdatedAt: task.UpdatedAt}, nil
	})
}

func (s *Service) Append(ctx context.Context, c engine.TaskContext, a engine.WorkTaskAppendArgs) (engine.WorkTaskEntryInfo, error) {
	content, err := worktask.Content(a.Content)
	if err != nil {
		return engine.WorkTaskEntryInfo{}, invalid("the content has 1 to 32000 characters")
	}
	return mutate(ctx, s, c, "append", func(q *sqlc.Queries, _ sqlc.Conversation, _ sqlc.Submission, now int64) (engine.WorkTaskEntryInfo, error) {
		entry, err := q.InsertWorkTaskEntry(ctx, sqlc.InsertWorkTaskEntryParams{TaskID: a.ID, AgentID: c.AgentID, Content: content, CreatedAt: now, Author: worktask.AuthorAgent})
		if err != nil {
			return engine.WorkTaskEntryInfo{}, notFound(err)
		}
		if err := q.TouchWorkTask(ctx, sqlc.TouchWorkTaskParams{ID: a.ID, At: now}); err != nil {
			return engine.WorkTaskEntryInfo{}, err
		}
		return engine.WorkTaskEntryInfo{ID: entry.ID, Author: worktask.AuthorAgent, CreatedAt: entry.CreatedAt}, nil
	})
}

func (s *Service) Update(ctx context.Context, c engine.TaskContext, a engine.WorkTaskUpdateArgs) (engine.WorkTaskInfo, error) {
	var title string
	if a.Title != "" {
		var err error
		if title, err = worktask.Title(a.Title); err != nil {
			return engine.WorkTaskInfo{}, invalid("the title has 1 to 160 characters")
		}
	}
	switch a.Lifecycle {
	case "", "open", "completed", "cancelled":
	default:
		return engine.WorkTaskInfo{}, invalid("lifecycle is open, completed, or cancelled")
	}
	if title == "" && a.Lifecycle == "" && a.Session == "" {
		return engine.WorkTaskInfo{}, invalid("give at least one field to change")
	}
	return mutate(ctx, s, c, "update", func(q *sqlc.Queries, conv sqlc.Conversation, source sqlc.Submission, now int64) (engine.WorkTaskInfo, error) {
		task, err := q.GetWorkTask(ctx, sqlc.GetWorkTaskParams{ID: a.ID, AgentID: c.AgentID})
		if err != nil {
			return engine.WorkTaskInfo{}, notFound(err)
		}
		if a.Lifecycle != "" && a.Lifecycle != task.Lifecycle {
			if !fromUser(source) {
				return engine.WorkTaskInfo{}, invalid("only the user can complete, cancel, or reopen a Task; this run started from %s", source.Kind)
			}
			if task, err = q.SetWorkTaskLifecycle(ctx, sqlc.SetWorkTaskLifecycleParams{ID: task.ID, AgentID: c.AgentID, Lifecycle: a.Lifecycle, UpdatedAt: now}); err != nil {
				return engine.WorkTaskInfo{}, err
			}
		}
		if title != "" && title != task.Title {
			if task, err = q.RenameWorkTask(ctx, sqlc.RenameWorkTaskParams{ID: task.ID, AgentID: c.AgentID, Title: title, UpdatedAt: now}); err != nil {
				return engine.WorkTaskInfo{}, err
			}
		}
		if a.Session != "" {
			child, err := childOf(ctx, q, conv, a.Session)
			if err != nil {
				return engine.WorkTaskInfo{}, err
			}
			if err := q.SetWorkTaskSession(ctx, sqlc.SetWorkTaskSessionParams{TaskID: task.ID, ConversationID: child.ID}); err != nil {
				return engine.WorkTaskInfo{}, err
			}
		}
		if err := q.TouchWorkTask(ctx, sqlc.TouchWorkTaskParams{ID: task.ID, At: now}); err != nil {
			return engine.WorkTaskInfo{}, err
		}
		if task, err = q.GetWorkTask(ctx, sqlc.GetWorkTaskParams{ID: task.ID, AgentID: c.AgentID}); err != nil {
			return engine.WorkTaskInfo{}, err
		}
		return taskInfo(ctx, q, task)
	})
}

// childOf accepts only a child session of the caller, as session_send does.
func childOf(ctx context.Context, q *sqlc.Queries, parent sqlc.Conversation, id string) (sqlc.Conversation, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return sqlc.Conversation{}, invalid("session is the ID of a child session of this conversation")
	}
	child, err := q.GetConversation(ctx, n)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (!child.ParentID.Valid || child.ParentID.Int64 != parent.ID) {
		return child, invalid("session %s is not a child session of this conversation", id)
	}
	return child, err
}

// Link writes the link of one plugin tool call with its replay record. A wait starts at the call time.
func (s *Service) Link(ctx context.Context, c engine.TaskContext, tool, taskID string, l worktask.Link) (engine.WorkTaskInfo, error) {
	return mutate(ctx, s, c, tool, func(q *sqlc.Queries, _ sqlc.Conversation, _ sqlc.Submission, now int64) (engine.WorkTaskInfo, error) {
		task, err := q.GetWorkTask(ctx, sqlc.GetWorkTaskParams{ID: taskID, AgentID: c.AgentID})
		if err != nil {
			return engine.WorkTaskInfo{}, notFound(err)
		}
		n, err := q.LinkWorkTaskObject(ctx, sqlc.LinkWorkTaskObjectParams{AgentID: c.AgentID, TaskID: task.ID, Namespace: l.Namespace, ObjectType: l.Object.Type, ObjectKey: l.Object.Key})
		if err != nil {
			return engine.WorkTaskInfo{}, err
		}
		if n == 0 {
			return engine.WorkTaskInfo{}, invalid("%s is linked to another Task", l.Name)
		}
		if l.WaitEvent != "" {
			if task.Lifecycle != "open" {
				return engine.WorkTaskInfo{}, invalid("a closed Task cannot wait for %s", l.Name)
			}
			var alert int64
			if l.Alert {
				alert = 1
			}
			if _, err := q.AddWorkTaskWait(ctx, sqlc.AddWorkTaskWaitParams{AgentID: c.AgentID, TaskID: task.ID, Namespace: l.Namespace, ObjectType: l.Object.Type, ObjectKey: l.Object.Key, EventType: l.WaitEvent, Revision: l.WaitRevision, AfterAt: now, Alert: alert}); err != nil {
				return engine.WorkTaskInfo{}, err
			}
		}
		if err := q.TouchWorkTask(ctx, sqlc.TouchWorkTaskParams{ID: task.ID, At: now}); err != nil {
			return engine.WorkTaskInfo{}, err
		}
		if task, err = q.GetWorkTask(ctx, sqlc.GetWorkTaskParams{ID: task.ID, AgentID: c.AgentID}); err != nil {
			return engine.WorkTaskInfo{}, err
		}
		return taskInfo(ctx, q, task)
	})
}
