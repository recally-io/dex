package chat

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"strings"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func taskInstructions(base string, task sqlc.Task) string {
	var instructions strings.Builder
	instructions.WriteString(base)
	instructions.WriteString("\n\nThis is one background execution. Complete the saved goal and call report_task before your final answer. You cannot create or manage background executions. All coding tools, including bash, remain available. Constraints below are instructions. Use foreground, non-interactive tools. Do not detach processes or start daemons. Never claim that failed or unrun checks passed. The main agent decides the next action.\n\nGoal:\n")
	instructions.WriteString(task.Goal)
	for _, section := range []struct{ name, data string }{{"Constraints", task.ConstraintsJson}, {"Done criteria", task.DoneCriteriaJson}} {
		instructions.WriteString("\n\n" + section.name + ":\n")
		instructions.WriteString(section.data)
	}
	return instructions.String()
}

// Save the report and its parent input together. Terminal records ignore late events.
func (s *Service) settleTask(ctx context.Context, id string, event engine.Outcome) (bool, error) {
	return s.settleTaskOutcome(ctx, id, event, "")
}

func (s *Service) settleTaskOutcome(ctx context.Context, id string, event engine.Outcome, recoveredStatus string) (bool, error) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	tx, q, err := s.taskTransaction(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	t, err := q.GetTask(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !occupied(t.Status) {
		return true, nil
	}
	status := "failed"
	if event.Status == "done" {
		status = "done"
	}
	if recoveredStatus != "" {
		status = recoveredStatus
	}
	report, now := cmp.Or(event.Answer, event.Reason), s.now().UnixMilli()
	if t.StructuredReport != "" {
		report = t.StructuredReport
	}
	if recoveredStatus == "interrupted" {
		report = "The process restarted before this task finished. Tell the user that the task was interrupted. Files or other side effects may exist. Check artifacts and prior actions before starting new work."
		if t.StructuredReport != "" {
			report += "\nProvisional structured report: " + t.StructuredReport
		}
		if err = q.RequestExecutionAbort(ctx, t.ConversationID); err != nil {
			return true, err
		}
	} else if t.StructuredReport != "" && event.Status != "done" {
		report += "\nExecution failure: " + event.Reason
	}
	changed, err := q.SettleTask(ctx, sqlc.SettleTaskParams{ID: t.ID, Status: status, Report: report, EndedAt: sql.NullInt64{Int64: now, Valid: true}})
	if err != nil || changed == 0 {
		return true, err
	}
	if _, err = q.SettleSubmission(ctx, sqlc.SettleSubmissionParams{ID: t.ID, Status: event.Status, Answer: sql.NullString{String: event.Answer, Valid: event.Status == "done"}, Reason: sql.NullString{String: event.Reason, Valid: event.Reason != ""}, SettledAt: sql.NullInt64{Int64: now, Valid: true}}); err != nil {
		return true, err
	}
	var structured *engine.TaskReport
	if t.StructuredReport != "" {
		if err := json.Unmarshal([]byte(t.StructuredReport), &structured); err != nil {
			return true, err
		}
	}
	goalMet := status == "done" && structured != nil && structured.Outcome == "done"
	payload := struct {
		Result any `json:"task-result"`
	}{struct {
		ID                  string `json:"id"`
		Title               string `json:"title"`
		Status              string `json:"status"`
		Report              string `json:"report"`
		HasStructuredReport bool   `json:"hasStructuredReport"`
		GoalMet             bool   `json:"goalMet"`
	}{t.ID, t.Title, status, truncateTaskReport(report), structured != nil, goalMet}}
	content, err := json.Marshal(payload)
	if err != nil {
		return true, err
	}
	if err = q.CreateTaskResult(ctx, sqlc.CreateTaskResultParams{ID: "task:" + t.ID + ":result", TaskID: t.ID, ConversationID: t.ParentConversationID, Content: string(content), SourceKey: sql.NullString{String: "task:" + t.ID + ":result", Valid: true}, CreatedAt: now}); err != nil {
		return true, err
	}
	if err = tx.Commit(); err != nil {
		return true, err
	}
	s.signal(t.ConversationID)
	s.signal(t.ParentConversationID)
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
