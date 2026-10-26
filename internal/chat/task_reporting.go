package chat

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"strings"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

// ToolReportTask stages a report; task settlement decides whether the goal was met.
func (s *Service) ToolReportTask(ctx context.Context, caller engine.TaskContext, report engine.TaskReport) error {
	conv, err := s.q.GetConversationByExecution(ctx, sqlc.GetConversationByExecutionParams{AgentID: caller.AgentID, ExecutionID: sql.NullInt64{Int64: caller.ConversationID, Valid: true}})
	if err != nil {
		return err
	}
	if !strings.HasPrefix(conv.ChannelKey, "task:") {
		return errors.New("report_task is only available in background task executions")
	}
	task, err := s.q.GetTask(ctx, caller.SourceRequestID)
	if err != nil {
		return err
	}
	if task.ConversationID != conv.ID {
		return ErrTaskInvalid
	}
	if report.Outcome != "done" && report.Outcome != "blocked" && report.Outcome != "failed" {
		return errors.New("outcome must be done, blocked, or failed")
	}
	if strings.TrimSpace(report.Summary) == "" {
		return errors.New("summary must not be empty")
	}
	for _, command := range report.Reproduction {
		if strings.TrimSpace(command.Command) == "" {
			return errors.New("reproduction commands must not be empty")
		}
	}
	data, err := json.Marshal(report)
	if err != nil {
		return err
	}
	changed, err := s.q.StageTaskReport(ctx, sqlc.StageTaskReportParams{ID: task.ID, ConversationID: conv.ID, Report: string(data)})
	if err != nil || changed > 0 {
		return err
	}
	prior, err := s.q.GetTask(ctx, task.ID)
	if err != nil {
		return err
	}
	if prior.StructuredReport == string(data) {
		return nil
	}
	return errors.New("this background task already has a report or has ended")
}
