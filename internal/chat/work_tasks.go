package chat

import (
	"context"
	"database/sql"
	"errors"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/worktask"
)

// ErrWorkTaskConflict means the selected Task does not match the question.
var ErrWorkTaskConflict = errors.New("the question belongs to different work; select the matching Task or send a note")

func workTaskForInput(ctx context.Context, q *sqlc.Queries, id string) (string, error) {
	taskID, err := q.WorkTaskForInput(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return taskID, err
}

// linkTaskChild records a new child only when its source input belongs to an open Task.
func linkTaskChild(ctx context.Context, q *sqlc.Queries, l *worktask.Ledger, sourceID string, childID, now int64) error {
	taskID, err := workTaskForInput(ctx, q, sourceID)
	if err != nil || taskID == "" {
		return err
	}
	task, err := q.GetBoardTask(ctx, taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil || !worktask.IsOpenStatus(task.Status) {
		return err
	}
	// A queued wake can outlive a handoff. Its old Main must not link new children.
	source, err := q.GetSubmission(ctx, sourceID)
	if err != nil {
		return err
	}
	if source.Kind == "task_wake" {
		main, err := q.GetConversation(ctx, source.ConversationID)
		if err != nil {
			return err
		}
		if main.AgentID != task.AssigneeBotID {
			return nil
		}
	}
	// Other source receipts authorize the association, including for a child in another Bot.
	return l.LinkSession(ctx, worktask.System, task, childID, false, now)
}

func workTaskForMessage(ctx context.Context, q *sqlc.Queries, conv sqlc.Conversation, m Message) (string, error) {
	if conv.ChannelKey != MainKey {
		if m.WorkTaskID != "" {
			return "", ErrWorkTaskConflict
		}
		return "", nil
	}
	if m.AnswersQuestion == "" {
		return m.WorkTaskID, nil
	}
	linked, err := q.WorkTaskForQuestion(ctx, sqlc.WorkTaskForQuestionParams{QuestionID: m.AnswersQuestion, ConversationID: conv.ID})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if m.WorkTaskID != "" && m.WorkTaskID != linked {
		return "", ErrWorkTaskConflict
	}
	return linked, nil
}

// promoteTaskQuestion opens the oldest settled Task question and queues its delivery in the same transaction.
func promoteTaskQuestion(ctx context.Context, q *sqlc.Queries, conv sqlc.Conversation, now int64) error {
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

// ConfigureWorkTasks binds Task entry notifications before the chat service starts.
func (s *Service) ConfigureWorkTasks(tasks *worktask.Service) {
	s.workTasks = tasks
}

func recordWorkTaskMessage(ctx context.Context, q *sqlc.Queries, l *worktask.Ledger, agentID, taskID string, m Message, now int64) error {
	var task sqlc.WorkTask
	var err error
	if m.AnswersQuestion != "" {
		// The question is validated in its original Session, even after reassignment.
		task, err = q.GetBoardTask(ctx, taskID)
	} else {
		task, err = q.GetWorkTask(ctx, sqlc.GetWorkTaskParams{ID: taskID, AgentID: agentID})
	}
	if err != nil {
		return err
	}
	if !worktask.IsOpenStatus(task.Status) && m.AnswersQuestion == "" {
		return ErrWorkTaskConflict
	}
	if err := q.AssociateWorkTaskInput(ctx, sqlc.AssociateWorkTaskInputParams{SubmissionID: m.ID, TaskID: taskID}); err != nil {
		return err
	}
	if m.Content != "" {
		return l.RecordOwnerMessage(ctx, task, m.Content, now)
	}
	return l.RecordAssociation(ctx, worktask.Owner, task, "Task input associated", now)
}
