package chat

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/worker"
)

func taskInstructions(base string) string {
	return base + "\n\nThis is one background execution. Complete the given goal and return a report. You cannot create or manage background executions. Use foreground, non-interactive tools. Do not detach processes or start daemons. State the changed files, review results, test commands and results, artifact paths, and remaining work for coding tasks. If you fail or need input, save the useful evidence and state the blocker in the report. The main agent decides the next action. Never claim that failed or unrun checks passed."
}

// Save the report and its parent input together. Cleanup evidence never replaces a report.
func (s *Service) settleTask(ctx context.Context, event worker.Settled) (bool, error) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	tx, q, err := s.taskTransaction(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	t, err := q.GetTask(ctx, event.RequestID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	sub, err := q.GetSubmission(ctx, t.ID)
	if err != nil {
		return true, err
	}
	report := cmp.Or(event.Answer, t.Report, sub.Answer.String)
	report = cmp.Or(report, event.Reason)
	note := event.StopNote
	if !event.ExecutionConfirmed {
		note = cmp.Or(note, t.StopNote)
	}
	status, ended := t.Status, t.EndedAt
	if occupied(t.Status) {
		switch {
		case !event.ExecutionConfirmed:
			status = "cancelling"
			note = cmp.Or(note, "Controlled execution has not ended. Remote work may still run.")
		case t.StopRequestedAt.Valid:
			status = "cancelled"
		case event.Status == "done" || sub.Status == "done":
			status = "done"
		default:
			status = "failed"
		}
		if !occupied(status) {
			ended = sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}
		}
	}
	now := s.now().UnixMilli()
	if err = q.SettleTask(ctx, sqlc.SettleTaskParams{ID: t.ID, Status: status, Report: report, StopNote: note, LastEventAt: now, EndedAt: ended}); err != nil {
		return true, err
	}
	if _, err = q.SettleSubmission(ctx, sqlc.SettleSubmissionParams{ID: t.ID, Status: event.Status, Answer: sql.NullString{String: event.Answer, Valid: event.Status == "done"}, Reason: sql.NullString{String: event.Reason, Valid: event.Reason != ""}, SettledAt: sql.NullInt64{Int64: now, Valid: true}}); err != nil {
		return true, err
	}
	if (status == "done" || status == "failed") && !t.StopRequestedAt.Valid {
		payload := struct {
			Result any `json:"task-result"`
		}{struct {
			ID       string `json:"id"`
			Title    string `json:"title"`
			Status   string `json:"status"`
			Report   string `json:"report"`
			StopNote string `json:"stopNote,omitempty"`
		}{t.ID, t.Title, status, truncateTaskReport(report), note}}
		content, err := json.Marshal(payload)
		if err != nil {
			return true, err
		}
		if err = q.CreateTaskResult(ctx, sqlc.CreateTaskResultParams{ID: "task:" + t.ID + ":result", ConversationID: t.ParentConversationID, Content: string(content), SourceKey: sql.NullString{String: "task:" + t.ID + ":result", Valid: true}, CreatedAt: now}); err != nil {
			return true, err
		}
	}
	if err = tx.Commit(); err != nil {
		return true, err
	}
	s.Wake()
	return true, nil
}

func truncateTaskReport(report string) string {
	runes := []rune(report)
	if len(runes) <= 4000 {
		return report
	}
	return string(runes[:3000]) + "\n[Use get_task for the full report.]\n" + string(runes[len(runes)-1000:])
}

func stopContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 6*time.Second)
}

func (s *Service) cancelPendingTasks(ctx context.Context) {
	tasks, err := s.q.ListCancellingTasks(ctx)
	if err != nil {
		slog.Error("list task cancellations", "err", err)
		return
	}
	for _, task := range tasks {
		if ctx.Err() != nil {
			return
		}
		s.taskWG.Go(func() { s.stopTask(ctx, task) })
	}
}

func (s *Service) stopTask(ctx context.Context, task sqlc.ListCancellingTasksRow) bool {
	if _, busy := s.cancelling.LoadOrStore(task.ID, struct{}{}); busy {
		return false
	}
	defer s.cancelling.Delete(task.ID)
	// Recovery and binding share this boundary. A cancellation must not reach a later process.
	s.taskMu.Lock()
	current, err := s.q.GetTask(ctx, task.ID)
	var call worker.CallFunc
	if err == nil && current.Status == "cancelling" && current.StartedAt.Valid {
		call, err = s.worker.Bind()
	}
	s.taskMu.Unlock()
	if err != nil {
		slog.Warn("bind task cancellation", "task", task.ID, "err", err)
		return false
	}
	if current.Status != "cancelling" {
		return !occupied(current.Status)
	}
	event := worker.Settled{RequestID: task.ID, Status: "unanswered", Reason: "aborted", ExecutionConfirmed: !current.StartedAt.Valid}
	if current.StartedAt.Valid {
		callCtx, cancel := stopContext(ctx)
		var result worker.AbortResult
		err = call(callCtx, "abort", worker.AbortParams{AgentID: task.AgentID, ConversationID: task.WorkerConversationID.Int64, TaskStageID: task.ID}, &result)
		cancel()
		event.ExecutionConfirmed = err == nil && result.ExecutionConfirmed
		event.StopNote = result.StopNote
		if err != nil {
			event.StopNote = "Controlled execution has not been confirmed stopped: " + err.Error()
		}
	} else {
		event.Reason = "cancelled before execution"
	}
	if _, err := s.settleTask(ctx, event); err != nil {
		slog.Error("settle task cancellation", "task", task.ID, "err", err)
		return false
	}
	s.inFlight.Delete(task.ID)
	return event.ExecutionConfirmed
}
