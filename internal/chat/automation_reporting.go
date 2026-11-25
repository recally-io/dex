package chat

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

// ToolReportCheck stores a provisional report. Settlement decides the final outcome.
func (s *Service) ToolReportCheck(ctx context.Context, caller engine.TaskContext, report CheckReport) error {
	_, err := s.toolCaller(ctx, caller)
	if err != nil {
		return err
	}
	source, err := s.q.GetSubmission(ctx, caller.SourceRequestID)
	if err != nil {
		return err
	}
	if source.Origin != "automation" || source.Kind != "message" {
		return errors.New("automation_report is only available during an Automation run")
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

// A user can stop or withdraw a run. Keep its existing receipt and history, but do not alert.
func routineCanceled(status, reason string) bool {
	return status == "unanswered" && (reason == "stopped" || reason == "withdrawn" || reason == "aborted" || reason == "reset")
}

// Native settlement uses stable codes. Admission errors can also reach this path as raw text;
// do not put their provider or environment details in the Routine result or its alert.
func routineExecutionReason(reason string) string {
	switch reason {
	case "model_error", "no_model", "interrupted", "restart_limit", "nothing_to_answer",
		"faulted", "orphaned", "turn_limit", "stopped", "withdrawn", "aborted", "reset":
		return reason
	default:
		return "automation execution failed"
	}
}

// settleInput commits the receipt, the routine result, and the result for the parent session in one
// control transaction. It returns the parent session that got a result (or zero) and any channel
// peer woken by an Outbox write.
func (s *Service) settleInput(ctx context.Context, id string, event engine.Outcome) (changed, parent int64, outboxPeer string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, "", err
	}
	defer func() { _ = tx.Rollback() }()
	changed, parent, outboxPeer, err = s.settleIn(ctx, s.q.WithTx(tx), id, event, 0)
	if err != nil || changed == 0 {
		return 0, 0, "", err
	}
	return changed, parent, outboxPeer, tx.Commit()
}

// settleIn writes the outcome of one input in the transaction of q.
func (s *Service) settleIn(ctx context.Context, q *sqlc.Queries, id string, event engine.Outcome, withdrawn int) (changed, parent int64, outboxPeer string, err error) {
	changed, err = q.SettleSubmission(ctx, sqlc.SettleSubmissionParams{ID: id, Status: event.Status, Answer: sql.NullString{String: event.Answer, Valid: event.Status == "done"}, Reason: sql.NullString{String: event.Reason, Valid: event.Reason != ""}, SettledAt: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}})
	if err != nil || changed == 0 {
		return 0, 0, "", err
	}
	sub, err := q.GetSubmission(ctx, id)
	if err != nil {
		return 0, 0, "", err
	}
	conv, err := q.GetConversation(ctx, sub.ConversationID)
	if err != nil {
		return 0, 0, "", err
	}
	// The existing child result informs the parent. The projection does not send a wake.
	if conv.ParentID.Valid {
		if err := q.ProjectTaskChildExecution(ctx, conv.ID); err != nil {
			return 0, 0, "", err
		}
	}
	// An answer that ran closes its question. One that never produced a reply opens it again.
	if event.Status == "done" {
		err = q.ConsumeQuestion(ctx, sqlc.ConsumeQuestionParams{SubmissionID: sql.NullString{String: id, Valid: true}, Now: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}})
	} else {
		err = reopenQuestion(ctx, q, id)
	}
	if err != nil {
		return 0, 0, "", err
	}
	reason := routineExecutionReason(event.Reason)
	if err = q.FinalizeAutomationRun(ctx, sqlc.FinalizeAutomationRunParams{ID: id, ExecutionStatus: event.Status, Failure: reason}); err != nil {
		return 0, 0, "", err
	}
	if !routineCanceled(event.Status, event.Reason) {
		if err = queueRoutineOutbox(ctx, q, id, conv.ID, s.now().UnixMilli()); err != nil {
			return 0, 0, "", err
		}
	}
	if outboxPeer, err = writeOutboxOnSettle(ctx, q, sub, conv, event, s.now().UnixMilli()); err != nil {
		return 0, 0, "", err
	}
	if conv.ChannelKey == MainKey {
		if err := promoteTaskQuestion(ctx, q, conv, s.now().UnixMilli()); err != nil {
			return 0, 0, "", err
		}
	}
	// An older run can have a saved exit action when dex resumes after an upgrade.
	if sub.ExitAction != "stay_silent" {
		if parent, err = s.sendResult(ctx, q, id, event, withdrawn); err != nil {
			return 0, 0, "", err
		}
	}
	if parent == 0 && conv.ParentID.Valid && !sub.ParentRequestID.Valid && sub.Origin != "automation" && !isUserOrigin(sub.Origin) && event.Status == "unanswered" && event.Reason != "stopped" && event.Reason != "withdrawn" && event.Reason != "aborted" {
		if archived, aerr := q.ConversationAgentArchived(ctx, conv.ParentID.Int64); aerr != nil {
			return 0, 0, "", aerr
		} else if archived == 0 {
			body := sessionResult{
				Session:   strconv.FormatInt(conv.ID, 10),
				Title:     conv.Title,
				Turn:      sub.ID,
				Outcome:   "failed",
				Reason:    event.Reason,
				Withdrawn: withdrawn,
			}
			if event.Reason == "interrupted" {
				body.Outcome = "interrupted"
			}
			encoded, merr := json.Marshal(struct {
				Result sessionResult `json:"task-result"`
			}{body})
			if merr != nil {
				return 0, 0, "", merr
			}
			if _, cerr := q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{
				ID:             "err:" + sub.ID,
				ConversationID: conv.ParentID.Int64,
				Content:        string(encoded),
				Attachments:    "null",
				Origin:         cmp.Or(sub.Origin, "web"),
				Producer:       ProducerSubSession,
				Kind:           "task_result",
				CreatedAt:      s.now().UnixMilli(),
			}); cerr != nil {
				return 0, 0, "", cerr
			}
			parent = conv.ParentID.Int64
		}
	}
	return changed, parent, outboxPeer, nil
}
