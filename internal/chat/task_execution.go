package chat

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/worker"
)

func taskInstructions(base string) string {
	return base + "\n\nThis is one background execution. Complete the given goal and return a report. You cannot create or manage background executions. Use foreground, non-interactive tools. Do not detach processes or start daemons. State the changed files, review results, test commands and results, artifact paths, and remaining work for coding tasks. If you fail or need input, save the useful evidence and state the blocker in the report. The main agent decides the next action. Never claim that failed or unrun checks passed."
}

// Save the report and its parent input together. Terminal records ignore late events.
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
	if !occupied(t.Status) {
		return true, nil
	}
	status := "failed"
	if event.Status == "done" {
		status = "done"
	}
	report, now := cmp.Or(event.Answer, event.Reason), s.now().UnixMilli()
	changed, err := q.SettleTask(ctx, sqlc.SettleTaskParams{ID: t.ID, Status: status, Report: report, EndedAt: sql.NullInt64{Int64: now, Valid: true}})
	if err != nil || changed == 0 {
		return true, err
	}
	if _, err = q.SettleSubmission(ctx, sqlc.SettleSubmissionParams{ID: t.ID, Status: event.Status, Answer: sql.NullString{String: event.Answer, Valid: event.Status == "done"}, Reason: sql.NullString{String: event.Reason, Valid: event.Reason != ""}, SettledAt: sql.NullInt64{Int64: now, Valid: true}}); err != nil {
		return true, err
	}
	payload := struct {
		Result any `json:"task-result"`
	}{struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Status string `json:"status"`
		Report string `json:"report"`
	}{t.ID, t.Title, status, truncateTaskReport(report)}}
	content, err := json.Marshal(payload)
	if err != nil {
		return true, err
	}
	if err = q.CreateTaskResult(ctx, sqlc.CreateTaskResultParams{ID: "task:" + t.ID + ":result", ConversationID: t.ParentConversationID, Content: string(content), SourceKey: sql.NullString{String: "task:" + t.ID + ":result", Valid: true}, CreatedAt: now}); err != nil {
		return true, err
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
