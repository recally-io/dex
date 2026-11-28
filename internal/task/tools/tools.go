// Package tools implements the task_* tools of Main and writable Sessions.
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

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/session"
	tasksvc "github.com/recally-io/dex/internal/task"
)

// ErrInvalid means the tool call cannot change this Task. The message tells the model why.
var ErrInvalid = errors.New("invalid task tool call")

// Service checks the caller and applies each change with its replay record in one transaction.
type Service struct {
	db    *sql.DB
	q     *sqlc.Queries
	now   func() time.Time
	tasks *tasksvc.Service
}

func New(conn *sql.DB) *Service {
	return &Service{db: conn, q: sqlc.New(conn), now: time.Now}
}

// ConfigureTasks binds Task entry notifications before the tools start.
func (s *Service) ConfigureTasks(tasks *tasksvc.Service) {
	s.tasks = tasks
}

// Handlers binds the engine tools to this service. links are the tools of the enabled plugins.
func (s *Service) Handlers(links []tasksvc.LinkTool) TaskTools {
	tools := TaskTools{List: s.List, Read: s.Read, New: s.New, Update: s.Update, Post: s.Post, Review: s.Review, Split: s.Split, Assign: s.Assign, Workload: s.MyWorkload}
	for _, link := range links {
		tools.Links = append(tools.Links, TaskLinkTool{
			Name: link.Name, Description: link.Description, Rules: link.Rules, Properties: link.Properties, Required: link.Required,
			Invoke: func(ctx context.Context, c engine.TaskContext, taskID string, args []byte) (TaskInfo, error) {
				l, err := link.Parse(args)
				if err != nil {
					return TaskInfo{}, invalid("%v", err)
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

// caller returns the writable conversation and its authenticated source input.
func caller(ctx context.Context, q *sqlc.Queries, c engine.TaskContext) (sqlc.Session, sqlc.Submission, error) {
	conv, err := q.GetSessionByExecution(ctx, sqlc.GetSessionByExecutionParams{BotID: c.BotID, ExecutionID: sql.NullInt64{Int64: c.ConversationID, Valid: true}})
	if err != nil {
		return conv, sqlc.Submission{}, err
	}
	if !session.Writable(conv.ChannelKey) {
		return conv, sqlc.Submission{}, invalid("Tasks need main or a writable Session")
	}
	source, err := q.GetSubmission(ctx, c.SourceRequestID)
	if err != nil {
		return conv, source, err
	}
	if source.SessionID != conv.ID {
		return conv, source, invalid("the run does not belong to this conversation")
	}
	return conv, source, nil
}

func taskOrigin(source sqlc.Submission) tasksvc.Origin {
	switch {
	case source.Kind == "message" && session.UserOrigin(source.Origin):
		return tasksvc.OriginUser
	case source.Kind == "task_wake":
		return tasksvc.OriginTaskWake
	// Only CreateTurnResult sets result_child_id, and it sets native_answer_id only for a native answer.
	// A rejected, failed, or interrupted child turn and a session_report stay ordinary results.
	case source.Producer == session.ProducerSubSession && source.Kind == "session_result" && source.ResultChildID.Valid && source.NativeAnswerID.Valid:
		return tasksvc.OriginNativeResult
	case source.Producer == session.ProducerSubSession && (source.Kind == "session_result" || source.Kind == "message" && source.ParentRequestID.Valid):
		return tasksvc.OriginSessionResult
	case source.Kind == "wake":
		return tasksvc.OriginWake
	default:
		return tasksvc.OriginOther
	}
}

// readableTask resolves public keys before applying the central ACL. An unknown
// key and an inaccessible Task produce the same response.
func readableTask(ctx context.Context, q *sqlc.Queries, actor tasksvc.Actor, id string) (sqlc.Task, error) {
	task, err := tasksvc.Lookup(ctx, q, id)
	if err == nil {
		var allowed bool
		allowed, err = tasksvc.CanRead(ctx, q, actor, task)
		if err == nil && !allowed {
			err = sql.ErrNoRows
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		err = invalid("no Task visible to this Bot has this ID or key; call task_list")
	}
	return task, err
}

// mutate commits the change, its wake pointers, and its replay result together.
func mutate[R any](ctx context.Context, s *Service, c engine.TaskContext, action string, change func(*sqlc.Queries, *tasksvc.Ledger, tasksvc.Actor, tasksvc.Origin, int64) (R, error)) (R, error) {
	var zero R
	if c.Key == "" || !strings.HasPrefix(c.Key, c.BotID+"/") {
		return zero, invalid("a durable tool key is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	if err := q.LockTaskWriter(ctx); err != nil {
		return zero, err
	}
	conv, source, err := caller(ctx, q, c)
	if err != nil {
		return zero, err
	}
	prior, err := q.GetTaskToolOperation(ctx, c.Key)
	if err == nil {
		if prior.BotID != c.BotID || prior.SessionID != conv.ID || prior.SourceRequestID != source.ID || prior.Action != action {
			return zero, invalid("the tool key belongs to another operation")
		}
		var out R
		err := json.Unmarshal([]byte(prior.Result), &out)
		return out, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return zero, err
	}
	ledger := tasksvc.NewLedger(q)
	result, err := change(q, ledger, tasksvc.BotInConversation(conv), taskOrigin(source), s.now().UnixMilli())
	if err != nil {
		return zero, err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return zero, err
	}
	if err := q.CreateTaskToolOperation(ctx, sqlc.CreateTaskToolOperationParams{Key: c.Key, BotID: c.BotID, SessionID: conv.ID, SourceRequestID: source.ID, Action: action, Result: string(data)}); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	s.tasks.Notify(ledger)
	return result, nil
}

func (s *Service) New(ctx context.Context, c engine.TaskContext, a TaskNewArgs) (TaskInfo, error) {
	return mutate(ctx, s, c, "new", func(_ *sqlc.Queries, l *tasksvc.Ledger, actor tasksvc.Actor, origin tasksvc.Origin, now int64) (TaskInfo, error) {
		task, err := l.CreateTask(ctx, actor, origin, tasksvc.CreateTaskParams{
			AssigneeBotID: c.BotID, Title: a.Title, Note: strings.TrimSpace(a.Note), Status: a.Status,
		}, now)
		return summary(task), err
	})
}

func (s *Service) Post(ctx context.Context, c engine.TaskContext, a TaskPostArgs) (TaskEntryInfo, error) {
	return mutate(ctx, s, c, "post", func(q *sqlc.Queries, l *tasksvc.Ledger, actor tasksvc.Actor, origin tasksvc.Origin, now int64) (TaskEntryInfo, error) {
		task, err := readableTask(ctx, q, actor, a.ID)
		if err != nil {
			return TaskEntryInfo{}, err
		}
		entry, err := l.PostEntry(ctx, actor, origin, task, tasksvc.PostEntryParams{Kind: a.Kind, Tag: a.Tag, Content: a.Content}, now)
		return TaskEntryInfo{ID: entry.ID, Seq: entry.Seq, Kind: entry.Kind, Tag: entry.Tag, Trust: entry.Trust, CreatedAt: entry.CreatedAt}, err
	})
}

func (s *Service) Update(ctx context.Context, c engine.TaskContext, a TaskUpdateArgs) (TaskInfo, error) {
	var title string
	if a.Title != "" {
		var err error
		if title, err = tasksvc.Title(a.Title); err != nil {
			return TaskInfo{}, invalid("the title has 1 to 160 characters")
		}
	}
	if a.Primary && a.Session == "" {
		return TaskInfo{}, invalid("primary requires a session")
	}
	if title == "" && a.Status == "" && a.Session == "" {
		return TaskInfo{}, invalid("give at least one field to change")
	}
	return mutate(ctx, s, c, "update", func(q *sqlc.Queries, l *tasksvc.Ledger, actor tasksvc.Actor, origin tasksvc.Origin, now int64) (TaskInfo, error) {
		task, err := readableTask(ctx, q, actor, a.ID)
		if err != nil {
			return TaskInfo{}, err
		}
		if a.Status != "" && a.Status != task.Status {
			if a.Status == tasksvc.StatusCancelled || !tasksvc.IsOpenStatus(task.Status) {
				return TaskInfo{}, fmt.Errorf("%w: only the Owner can cancel or reopen a Task", tasksvc.ErrForbidden)
			}
			if task, err = l.TransitionStatus(ctx, actor, origin, task, a.Status, now); err != nil {
				return TaskInfo{}, err
			}
		}
		if title != "" && title != task.Title {
			if task, err = l.Rename(ctx, actor, origin, task, title, now); err != nil {
				return TaskInfo{}, err
			}
		}
		if a.Session != "" {
			childID, err := strconv.ParseInt(a.Session, 10, 64)
			if err != nil {
				return TaskInfo{}, invalid("session is the ID of a child session of this conversation")
			}
			if err := tasksvc.AuthorizeSessionLink(ctx, q, actor, origin, task, childID); err != nil {
				return TaskInfo{}, err
			}
			if err := l.LinkSession(ctx, actor, task, childID, a.Primary, now); err != nil {
				return TaskInfo{}, err
			}
			if task, err = q.GetTask(ctx, task.ID); err != nil {
				return TaskInfo{}, err
			}
		}
		return taskInfo(ctx, q, task)
	})
}

func (s *Service) Review(ctx context.Context, c engine.TaskContext, a TaskReviewArgs) (TaskInfo, error) {
	return mutate(ctx, s, c, "review", func(q *sqlc.Queries, l *tasksvc.Ledger, actor tasksvc.Actor, origin tasksvc.Origin, now int64) (TaskInfo, error) {
		task, err := readableTask(ctx, q, actor, a.ID)
		if err != nil {
			return TaskInfo{}, err
		}
		task, err = l.Review(ctx, actor, origin, task, a.Decision, a.ReviewSeq, a.Note, now)
		if err != nil {
			return TaskInfo{}, err
		}
		return taskInfo(ctx, q, task)
	})
}

// Link writes the link of one plugin tool call with its replay record. A wait starts at the call time.
func (s *Service) Link(ctx context.Context, c engine.TaskContext, tool, taskID string, l tasksvc.Link) (TaskInfo, error) {
	return mutate(ctx, s, c, tool, func(q *sqlc.Queries, ledger *tasksvc.Ledger, actor tasksvc.Actor, origin tasksvc.Origin, now int64) (TaskInfo, error) {
		task, err := readableTask(ctx, q, actor, taskID)
		if err != nil {
			return TaskInfo{}, err
		}
		allowed, err := tasksvc.CanWrite(ctx, q, actor, origin, task, tasksvc.ActionLinkObject)
		if err != nil {
			return TaskInfo{}, err
		}
		if !allowed {
			return TaskInfo{}, tasksvc.ErrForbidden
		}
		n, err := q.LinkTaskObject(ctx, sqlc.LinkTaskObjectParams{BotID: task.AssigneeBotID, TaskID: task.ID, Namespace: l.Namespace, ObjectType: l.Object.Type, ObjectKey: l.Object.Key})
		if err != nil {
			return TaskInfo{}, err
		}
		if n == 0 {
			return TaskInfo{}, invalid("%s is linked to another Task", l.Name)
		}
		if l.WaitEvent != "" {
			if !tasksvc.IsOpenStatus(task.Status) {
				return TaskInfo{}, invalid("a closed Task cannot wait for %s", l.Name)
			}
			var alert int64
			if l.Alert {
				alert = 1
			}
			if _, err := q.AddTaskWait(ctx, sqlc.AddTaskWaitParams{BotID: task.AssigneeBotID, TaskID: task.ID, Namespace: l.Namespace, ObjectType: l.Object.Type, ObjectKey: l.Object.Key, EventType: l.WaitEvent, Revision: l.WaitRevision, AfterAt: now, Alert: alert}); err != nil {
				return TaskInfo{}, err
			}
		}
		if err := ledger.RecordAssociation(ctx, actor, task, "Linked "+l.Name, now); err != nil {
			return TaskInfo{}, err
		}
		if task, err = q.GetTask(ctx, task.ID); err != nil {
			return TaskInfo{}, err
		}
		return taskInfo(ctx, q, task)
	})
}
