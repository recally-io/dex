package chat

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/worker"
)

func taskInstructions(base string) string {
	return base + "\n\nThis is one background execution stage. Complete the given goal and return a report. Do not spawn or finish user tasks. Use foreground, non-interactive tools. Do not detach processes or start daemons. State the changed files, review results, test commands and results, artifact paths, and remaining work for coding tasks. If user input is required, start the report with NEEDS_INPUT: followed by the question. A report must not claim that unrun or failed tests passed."
}

// settleTask saves the stage, its parent input, and channel notice in one transaction.
func (s *Service) settleTask(ctx context.Context, event worker.Settled) (bool, error) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	tx, q, err := s.taskTransaction(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	stage, err := q.GetTaskStage(ctx, event.RequestID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !occupied(stage.Status) || stage.Status == "unknown" {
		return true, nil
	}
	goal, err := q.GetUserTaskByID(ctx, stage.UserTaskID)
	if err != nil {
		return true, err
	}
	status, report := "failed", event.Reason
	switch {
	case !event.ExecutionConfirmed:
		status = "unknown"
		report = cmpReport(event.Reason, "Execution ended without confirmation. Check external processes and artifacts before starting new work.")
	case stage.Status == "cancelling" || goal.StopRequestedAt.Valid:
		status, report = "cancelled", cmpReport(event.Answer, "Controlled execution has ended. Previous changes were not rolled back.")
	case event.Status == "done":
		status, report = "done", event.Answer
		if strings.HasPrefix(strings.TrimSpace(report), "NEEDS_INPUT:") {
			status = "needs_input"
		}
	}
	now := s.now().UnixMilli()
	if status == "unknown" {
		if err = q.MarkTaskStageUnknown(ctx, sqlc.MarkTaskStageUnknownParams{ID: stage.ID, Report: report, LastEventAt: now}); err != nil {
			return true, err
		}
	} else {
		if _, err = q.SettleTaskStage(ctx, sqlc.SettleTaskStageParams{ID: stage.ID, Status: status, Report: report, LastEventAt: now, EndedAt: sql.NullInt64{Int64: now, Valid: true}}); err != nil {
			return true, err
		}
	}
	if _, err = q.SettleSubmission(ctx, sqlc.SettleSubmissionParams{ID: stage.ID, Status: event.Status, Answer: sql.NullString{String: event.Answer, Valid: event.Status == "done"}, Reason: sql.NullString{String: event.Reason, Valid: event.Reason != ""}, SettledAt: sql.NullInt64{Int64: now, Valid: true}}); err != nil {
		return true, err
	}
	if status == "unknown" || (status == "needs_input" && goal.Status != "unknown") {
		goal.Status, goal.Report = status, report
		if err = q.SetUserTaskState(ctx, sqlc.SetUserTaskStateParams{ID: goal.ID, Status: goal.Status, Report: goal.Report}); err != nil {
			return true, err
		}
	}
	if goal.StopRequestedAt.Valid {
		if err = s.settleStoppedGoal(ctx, q, &goal); err != nil {
			return true, err
		}
	}
	payload := struct {
		Result any `json:"task-result"`
	}{struct {
		ID      string `json:"id"`
		ShortID string `json:"shortId"`
		StageID string `json:"stageId"`
		Status  string `json:"status"`
		Report  string `json:"report"`
	}{goal.ID, goal.ShortID, stage.ID, status, truncateTaskReport(report)}}
	content, err := json.Marshal(payload)
	if err != nil {
		return true, err
	}
	if err = q.CreateTaskResult(ctx, sqlc.CreateTaskResultParams{ID: "task:" + stage.ID + ":result", ConversationID: goal.ParentConversationID, Content: string(content), SourceKey: sql.NullString{String: "task:" + stage.ID + ":result", Valid: true}, CreatedAt: now}); err != nil {
		return true, err
	}
	if status != "cancelled" {
		if err = s.taskNotice(ctx, q, goal, stage.ID+":"+status, report); err != nil {
			return true, err
		}
	}
	if err = tx.Commit(); err != nil {
		return true, err
	}
	s.taskChanged(ctx, goal)
	return true, nil
}

func cmpReport(report, fallback string) string {
	if report != "" {
		return report
	}
	return fallback
}

func truncateTaskReport(report string) string {
	runes := []rune(report)
	if len(runes) <= 4000 {
		return report
	}
	return string(runes[:3000]) + "\n[Full report saved in task details.]\n" + string(runes[len(runes)-1000:])
}

func (s *Service) settleStoppedGoal(ctx context.Context, q *sqlc.Queries, goal *sqlc.UserTask) error {
	stages, err := q.ListTaskStages(ctx, goal.ID)
	if err != nil {
		return err
	}
	status := "cancelled"
	for _, p := range stages {
		if p.Status == "unknown" {
			status = "unknown"
			break
		}
		if occupied(p.Status) {
			status = "cancelling"
		}
	}
	goal.Status = status
	switch status {
	case "cancelled":
		goal.EndedAt = sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}
		goal.Report = "Controlled execution has ended. Previous changes were not rolled back."
	case "unknown":
		goal.Report = "Execution is unknown. Check external processes and artifacts before starting new work."
	}
	if err = q.SetUserTaskState(ctx, sqlc.SetUserTaskStateParams{ID: goal.ID, Status: goal.Status, Report: goal.Report, EndedAt: goal.EndedAt}); err != nil {
		return err
	}
	if status != "cancelling" {
		return s.taskNotice(ctx, q, *goal, "stop:"+status, goal.Report)
	}
	return nil
}

func (s *Service) cancelPendingTasks(ctx context.Context) {
	stages, err := s.q.ListCancellingStages(ctx)
	if err != nil {
		slog.Error("list task cancellations", "err", err)
		return
	}
	for _, p := range stages {
		if ctx.Err() != nil {
			return
		}
		s.deliveryMu.Lock()
		current, err := s.q.GetTaskStage(ctx, p.ID)
		if err != nil || current.Status != "cancelling" {
			s.deliveryMu.Unlock()
			continue
		}
		if !current.StartedAt.Valid {
			s.deliveryMu.Unlock()
			_, err = s.settleTask(ctx, worker.Settled{RequestID: p.ID, Status: "unanswered", Reason: "cancelled before execution", ExecutionConfirmed: true})
		} else {
			s.deliveryMu.Unlock()
			// Cancellation is outside the main queue. A slow tool must not block other chats.
			if _, busy := s.cancelling.LoadOrStore(p.ID, struct{}{}); busy {
				continue
			}
			s.taskWG.Go(func() {
				defer s.cancelling.Delete(p.ID)
				callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				var result worker.AbortResult
				err := s.worker.Call(callCtx, "abort", worker.AbortParams{AgentID: p.AgentID, ConversationID: p.WorkerConversationID.Int64, TaskStageID: p.ID}, &result)
				event := worker.Settled{RequestID: p.ID, Status: "unanswered", Reason: "aborted", ExecutionConfirmed: err == nil && result.ExecutionConfirmed}
				if err != nil {
					event.Reason = fmt.Sprintf("Cancellation could not confirm execution ended: %v", err)
				}
				if _, err := s.settleTask(ctx, event); err != nil {
					slog.Error("settle task cancellation", "stage", p.ID, "err", err)
				}
				s.inFlight.Delete(p.ID)
			})
		}
		if err != nil {
			slog.Error("cancel task", "stage", p.ID, "err", err)
		}
	}
	// A goal with no live stages still needs its cancellation receipt.
	s.finishEmptyCancellations(ctx)
}

func (s *Service) finishEmptyCancellations(ctx context.Context) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	tx, q, err := s.taskTransaction(ctx)
	if err != nil {
		slog.Error("begin task cancellation", "err", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	goals, err := q.ListStoppingTasks(ctx)
	if err != nil {
		slog.Error("list stopping goals", "err", err)
		return
	}
	for _, goal := range goals {
		if err = s.settleStoppedGoal(ctx, q, &goal); err != nil {
			slog.Error("settle stopping goal", "err", err)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		slog.Error("commit task cancellation", "err", err)
		return
	}
	for _, goal := range goals {
		s.taskChanged(ctx, goal)
	}
}
