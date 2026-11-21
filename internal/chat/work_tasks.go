package chat

import (
	"context"
	"database/sql"
	"errors"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
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

func recordWorkTaskMessage(ctx context.Context, q *sqlc.Queries, agentID, taskID string, m Message, now int64) error {
	task, err := q.GetWorkTask(ctx, sqlc.GetWorkTaskParams{ID: taskID, AgentID: agentID})
	if err != nil {
		return err
	}
	if task.Lifecycle != "open" && m.AnswersQuestion == "" {
		return ErrWorkTaskConflict
	}
	if err := q.AssociateWorkTaskInput(ctx, sqlc.AssociateWorkTaskInputParams{SubmissionID: m.ID, TaskID: taskID}); err != nil {
		return err
	}
	if m.Content != "" {
		if _, err := q.InsertWorkTaskEntry(ctx, sqlc.InsertWorkTaskEntryParams{TaskID: taskID, AgentID: agentID, Content: m.Content, CreatedAt: now, Author: "owner"}); err != nil {
			return err
		}
	}
	return q.TouchWorkTask(ctx, sqlc.TouchWorkTaskParams{ID: taskID, At: now})
}
