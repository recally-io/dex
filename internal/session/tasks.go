package session

import (
	"context"
	"database/sql"
	"errors"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	tasksvc "github.com/recally-io/dex/internal/task"
)

// ErrTaskConflict means the selected Task does not match the question.
var ErrTaskConflict = errors.New("the question belongs to different work; select the matching Task or send a note")

func taskForInput(ctx context.Context, q *sqlc.Queries, id string) (string, error) {
	taskID, err := q.TaskForInput(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return taskID, err
}

// linkTaskChild records a new child only when its source input belongs to an open Task.
func linkTaskChild(ctx context.Context, q *sqlc.Queries, l *tasksvc.Ledger, sourceID string, childID, now int64) error {
	taskID, err := taskForInput(ctx, q, sourceID)
	if err != nil || taskID == "" {
		return err
	}
	task, err := q.GetTask(ctx, taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil || !tasksvc.IsOpenStatus(task.Status) {
		return err
	}
	// A queued wake can outlive a handoff. Its old Main must not link new children.
	source, err := q.GetSubmission(ctx, sourceID)
	if err != nil {
		return err
	}
	if source.Kind == "task_wake" {
		main, err := q.GetSession(ctx, source.SessionID)
		if err != nil {
			return err
		}
		if main.BotID != task.AssigneeBotID {
			return nil
		}
	}
	// Other source receipts authorize the association, including for a child in another Bot.
	return l.LinkSession(ctx, tasksvc.System, task, childID, false, now)
}

func taskForMessage(ctx context.Context, q *sqlc.Queries, conv sqlc.Session, m Message) (string, error) {
	if conv.ChannelKey != MainKey {
		if m.TaskID != "" {
			return "", ErrTaskConflict
		}
		return "", nil
	}
	selected := m.TaskID
	if _, ok := tasksvc.ParseKey(selected); ok {
		task, err := tasksvc.Lookup(ctx, q, selected)
		if err != nil {
			return "", err
		}
		selected = task.ID
	}
	if m.AnswersQuestion == "" {
		return selected, nil
	}
	linked, err := q.TaskForQuestion(ctx, sqlc.TaskForQuestionParams{QuestionID: m.AnswersQuestion, SessionID: conv.ID})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if selected != "" && selected != linked {
		return "", ErrTaskConflict
	}
	return linked, nil
}

// promoteTaskQuestion opens the oldest settled Task question and queues its delivery in the same transaction.
func promoteTaskQuestion(ctx context.Context, q *sqlc.Queries, conv sqlc.Session, now int64) error {
	question, err := q.PromoteQueuedQuestion(ctx, conv.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	source, err := q.GetSubmission(ctx, question.SourceSubmissionID)
	if err != nil {
		return err
	}
	_, err = writeOutboxOnSettle(ctx, q, source, conv, engine.Outcome{Status: "done", Answer: source.Answer.String}, now)
	return err
}

// ConfigureTasks binds Task entry notifications before the session service starts.
func (s *Service) ConfigureTasks(tasks *tasksvc.Service) {
	s.tasks = tasks
}

func recordTaskMessage(ctx context.Context, q *sqlc.Queries, l *tasksvc.Ledger, botID, taskID string, m Message, now int64) error {
	var task sqlc.Task
	var err error
	if m.AnswersQuestion != "" {
		// The question is validated in its original Session, even after reassignment.
		task, err = q.GetTask(ctx, taskID)
	} else {
		task, err = q.GetAssignedTask(ctx, sqlc.GetAssignedTaskParams{ID: taskID, BotID: botID})
	}
	if err != nil {
		return err
	}
	if !tasksvc.IsOpenStatus(task.Status) && m.AnswersQuestion == "" {
		return ErrTaskConflict
	}
	if err := q.AssociateTaskInput(ctx, sqlc.AssociateTaskInputParams{SubmissionID: m.ID, TaskID: taskID}); err != nil {
		return err
	}
	if m.Content != "" {
		return l.RecordOwnerMessage(ctx, task, m.Content, now)
	}
	return l.RecordAssociation(ctx, tasksvc.Owner, task, "Task input associated", now)
}
