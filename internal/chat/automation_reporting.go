package chat

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

// ToolReportCheck stores a provisional report. Settlement decides the final outcome.
func (s *Service) ToolReportCheck(ctx context.Context, caller engine.TaskContext, report engine.CheckReport) error {
	conv, err := s.toolCaller(ctx, caller)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(conv.ChannelKey, "automation:") {
		return errors.New("report_check is only available in automation conversations")
	}
	report.Summary = strings.TrimSpace(report.Summary)
	report.ChangeKey = strings.TrimSpace(report.ChangeKey)
	if report.Outcome != "changed" && report.Outcome != "unchanged" && report.Outcome != "failed" {
		return errors.New("outcome must be changed, unchanged, or failed")
	}
	if report.Summary == "" || utf8.RuneCountInString(report.Summary) > 1000 || utf8.RuneCountInString(report.ChangeKey) > 200 {
		return errors.New("summary must have 1 to 1000 characters; change key must have at most 200")
	}
	changed, err := s.q.ReportAutomationRun(ctx, sqlc.ReportAutomationRunParams{ID: caller.SourceRequestID, Outcome: report.Outcome, Summary: report.Summary, ChangeKey: report.ChangeKey})
	if err != nil {
		return err
	}
	if changed > 0 {
		return nil
	}
	prior, err := s.q.GetAutomationRun(ctx, caller.SourceRequestID)
	if err != nil {
		return err
	}
	if prior.Outcome == report.Outcome && prior.Summary == report.Summary && prior.ChangeKey == report.ChangeKey {
		return nil
	}
	return errors.New("this automation run already has a report or has ended")
}

// settleInput commits the receipt, the routine result, and the result for the parent session in one
// control transaction. It returns the parent session that got a result, or zero.
func (s *Service) settleInput(ctx context.Context, id string, event engine.Outcome) (changed, parent int64, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	changed, parent, err = s.settleIn(ctx, s.q.WithTx(tx), id, event, 0)
	if err != nil || changed == 0 {
		return 0, 0, err
	}
	return changed, parent, tx.Commit()
}

// settleIn writes the outcome of one input in the transaction of q.
func (s *Service) settleIn(ctx context.Context, q *sqlc.Queries, id string, event engine.Outcome, withdrawn int) (changed, parent int64, err error) {
	changed, err = q.SettleSubmission(ctx, sqlc.SettleSubmissionParams{ID: id, Status: event.Status, Answer: sql.NullString{String: event.Answer, Valid: event.Status == "done"}, Reason: sql.NullString{String: event.Reason, Valid: event.Reason != ""}, SettledAt: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}})
	if err != nil || changed == 0 {
		return 0, 0, err
	}
	// An answer that ran closes its question. One that never produced a reply opens it again.
	if event.Status == "done" {
		err = q.ConsumeQuestion(ctx, sqlc.ConsumeQuestionParams{SubmissionID: sql.NullString{String: id, Valid: true}, Now: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}})
	} else {
		err = reopenQuestion(ctx, q, id)
	}
	if err != nil {
		return 0, 0, err
	}
	reason := event.Reason
	if event.Status != "done" && reason == "" {
		reason = "automation execution failed"
	}
	if err = q.FinalizeAutomationRun(ctx, sqlc.FinalizeAutomationRunParams{ID: id, ExecutionStatus: event.Status, Failure: reason}); err != nil {
		return 0, 0, err
	}
	if err = q.QueueRoutineNotification(ctx, id); err != nil {
		return 0, 0, err
	}
	if parent, err = s.sendResult(ctx, q, id, event, withdrawn); err != nil {
		return 0, 0, err
	}
	return changed, parent, nil
}
