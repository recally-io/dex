package server

import (
	"context"
	"database/sql"
	"encoding/json/v2"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func taskTime(n sql.NullInt64) *int64 {
	if n.Valid {
		return new(n.Int64)
	}
	return nil
}

func reportOutcome(raw string) *api.TaskSummaryReportOutcome {
	if raw == "" {
		return nil
	}
	var report engine.TaskReport
	if err := json.Unmarshal([]byte(raw), &report); err != nil || report.Outcome == "" {
		return nil
	}
	outcome := api.TaskSummaryReportOutcome(report.Outcome)
	return &outcome
}

func applyActivity(summary *api.TaskSummary, status string, executionID sql.NullInt64, activity engine.Activity) {
	if status != "running" || !executionID.Valid {
		return
	}
	if activity.LastAction != "" {
		summary.LastAction = new(activity.LastAction)
	}
	summary.LastActivityAt = activity.LastActivityAt
	summary.ToolStartedAt = activity.ToolStartedAt
}

func taskView(t sqlc.Task, executionID int64, activity engine.Activity, waitReason string) api.Task {
	summary := api.TaskSummary{
		Id: t.ID, Title: t.Title, Status: api.TaskSummaryStatus(t.Status),
		CreatedAt: t.CreatedAt, StartedAt: taskTime(t.StartedAt), EndedAt: taskTime(t.EndedAt),
		ConversationId: t.ConversationID, ParentConversationId: t.ParentConversationID,
		ReportOutcome: reportOutcome(t.StructuredReport), WaitReason: optional(waitReason),
	}
	applyActivity(&summary, t.Status, sql.NullInt64{Int64: executionID, Valid: executionID > 0}, activity)
	return api.Task{
		Id: summary.Id, Title: summary.Title, Status: api.TaskStatus(summary.Status),
		CreatedAt: summary.CreatedAt, StartedAt: summary.StartedAt, EndedAt: summary.EndedAt,
		ConversationId: summary.ConversationId, ParentConversationId: summary.ParentConversationId,
		LastAction: summary.LastAction, LastActivityAt: summary.LastActivityAt, ToolStartedAt: summary.ToolStartedAt,
		ReportOutcome: taskReportOutcome(summary.ReportOutcome), Report: t.Report,
	}
}

func taskReportOutcome(outcome *api.TaskSummaryReportOutcome) *api.TaskReportOutcome {
	if outcome == nil {
		return nil
	}
	value := api.TaskReportOutcome(*outcome)
	return &value
}

func taskSummary(t sqlc.ListAgentTasksRow, activity engine.Activity, waitReason string) api.TaskSummary {
	summary := api.TaskSummary{
		Id: t.ID, Title: t.Title, Status: api.TaskSummaryStatus(t.Status),
		CreatedAt: t.CreatedAt, StartedAt: taskTime(t.StartedAt), EndedAt: taskTime(t.EndedAt),
		ConversationId: t.ConversationID, ParentConversationId: t.ParentConversationID,
		ReportOutcome: reportOutcome(t.StructuredReport), WaitReason: optional(waitReason),
	}
	applyActivity(&summary, t.Status, t.ExecutionID, activity)
	return summary
}

func (h *handler) CancelTask(ctx context.Context, req api.CancelTaskRequestObject) (api.CancelTaskResponseObject, error) {
	t, err := h.Chats.TaskRecord(ctx, agentID(ctx), req.TaskId)
	if err != nil {
		return nil, err
	}
	if _, err := h.Chats.CancelTask(ctx, agentID(ctx), t.ParentConversationID, req.TaskId); err != nil {
		return nil, err
	}
	t, err = h.Chats.TaskRecord(ctx, agentID(ctx), req.TaskId)
	if err != nil {
		return nil, err
	}
	executionID, err := h.Chats.TaskExecution(ctx, agentID(ctx), req.TaskId)
	if err != nil {
		return nil, err
	}
	var activity engine.Activity
	if t.Status == "running" {
		activity = h.Engine.Activity([]engine.Ref{{AgentID: agentID(ctx), ID: executionID}})[engine.Ref{AgentID: agentID(ctx), ID: executionID}]
	}
	waitReason, err := h.Chats.TaskWaitReason(ctx, agentID(ctx), req.TaskId)
	if err != nil {
		return nil, err
	}
	return api.CancelTask200JSONResponse(taskView(t, executionID, activity, waitReason)), nil
}
func (h *handler) GetTask(ctx context.Context, req api.GetTaskRequestObject) (api.GetTaskResponseObject, error) {
	t, err := h.Chats.TaskRecord(ctx, agentID(ctx), req.TaskId)
	if err != nil {
		return nil, err
	}
	executionID, err := h.Chats.TaskExecution(ctx, agentID(ctx), req.TaskId)
	if err != nil {
		return nil, err
	}
	var activity engine.Activity
	if t.Status == "running" {
		activity = h.Engine.Activity([]engine.Ref{{AgentID: agentID(ctx), ID: executionID}})[engine.Ref{AgentID: agentID(ctx), ID: executionID}]
	}
	waitReason, err := h.Chats.TaskWaitReason(ctx, agentID(ctx), req.TaskId)
	if err != nil {
		return nil, err
	}
	return api.GetTask200JSONResponse(taskView(t, executionID, activity, waitReason)), nil
}
