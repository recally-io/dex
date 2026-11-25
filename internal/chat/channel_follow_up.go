package chat

import (
	"context"
	"database/sql"
	"errors"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

var errChannelFollowUp = errors.New("follow-up requires an active commitment and the original channel authorization")

const followUpPrompt = "You committed to report on this delegation. Plain final text is internal. Use notify_user for your reviewed conclusion, or ask_user if blocked on a decision. Do not forward private drafts or raw child output."

func followUpFinalKey(delegationID string) string { return "follow-up-final:" + delegationID }

func createChannelFollowUp(ctx context.Context, q *sqlc.Queries, parent, child sqlc.Conversation, source sqlc.Submission, turn string) error {
	if source.Kind != "message" || !isUserOrigin(source.Origin) || peerFromOrigin(source.Origin) == "" || !source.SourceKey.Valid || source.ChannelRevoked != 0 || parent.ExecutionScope != "" {
		return errChannelFollowUp
	}
	return q.CreateChannelFollowUp(ctx, sqlc.CreateChannelFollowUpParams{
		DelegationID: turn, ResultID: "result:" + turn, ConversationID: parent.ID,
		ChildID: sql.NullInt64{Int64: child.ID, Valid: true}, ParentRequestID: sql.NullString{String: source.ID, Valid: true}, ParentSourceKey: source.SourceKey,
	})
}

// followUpParent verifies the recorded direct-parent lineage and the original receipt.
// The caller uses the writer snapshot when staging, settling or authorizing a send.
func followUpParent(ctx context.Context, q *sqlc.Queries, f sqlc.ChannelFollowUp, child sqlc.Conversation, turn sqlc.Submission) (sqlc.Submission, error) {
	if f.DelegationID != turn.ID || !f.ChildID.Valid || f.ChildID.Int64 != child.ID || turn.ConversationID != child.ID ||
		!child.ParentID.Valid || child.ParentID.Int64 != f.ConversationID || !f.ParentRequestID.Valid || turn.ParentRequestID != f.ParentRequestID {
		return sqlc.Submission{}, nil
	}
	parent, err := q.GetSubmission(ctx, f.ParentRequestID.String)
	if errors.Is(err, sql.ErrNoRows) {
		return sqlc.Submission{}, nil
	}
	if err != nil {
		return sqlc.Submission{}, err
	}
	if parent.ConversationID != f.ConversationID || !f.ParentSourceKey.Valid || parent.SourceKey != f.ParentSourceKey {
		return sqlc.Submission{}, nil
	}
	return parent, nil
}

func bindChannelFollowUp(ctx context.Context, q *sqlc.Queries, id string, activation int64) (int64, error) {
	return q.BindChannelFollowUpActivation(ctx, sqlc.BindChannelFollowUpActivationParams{
		DelegationID: id, NativeActivationID: sql.NullInt64{Int64: activation, Valid: true},
	})
}

const finalReportOperation = "stage_session_report"

// stageFinalReport handles all positively marked lineages, including an older
// shared run with an opted-in input. Membership can only veto a standalone wake.
func stageFinalReport(ctx context.Context, q *sqlc.Queries, r engine.TaskContext, child sqlc.Conversation, source sqlc.Submission, body string) (string, error) {
	prior, err := q.GetRoutineToolOperation(ctx, r.Key)
	if err == nil {
		if prior.AgentID != r.AgentID || prior.ConversationID != child.ID || prior.SourceRequestID != source.ID || prior.Action != finalReportOperation {
			return "", errSessionInvalid
		}
		return prior.Result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	f, err := q.GetChannelFollowUpByDelegation(ctx, source.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	protected := f.DelegationID != "" || r.Isolation != ""
	for _, id := range r.RunInputIDs {
		if protected {
			break
		}
		if id != source.ID {
			_, err := q.GetChannelFollowUpByDelegation(ctx, id)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return "", err
			}
			protected = err == nil
		}
	}
	if !protected {
		return "", nil
	}
	reason, err := stageProtectedReport(ctx, q, r, f, child, source, body)
	if err != nil {
		return "", err
	}
	disposition := engine.ReportStaged
	if reason != "" {
		disposition = engine.ReportIgnored
	}
	if err := q.CreateRoutineToolOperation(ctx, sqlc.CreateRoutineToolOperationParams{
		Key: r.Key, AgentID: r.AgentID, ConversationID: child.ID, SourceRequestID: source.ID, Action: finalReportOperation, Result: disposition,
	}); err != nil {
		return "", err
	}
	if reason != "" {
		receiptTrace(child.ID, source.ID).InfoContext(ctx, "session report", "phase", "report_ignored", "reason", reason)
	}
	return disposition, nil
}

// stageProtectedReport returns an empty reason when it stages the report.
func stageProtectedReport(ctx context.Context, q *sqlc.Queries, r engine.TaskContext, f sqlc.ChannelFollowUp, child sqlc.Conversation, source sqlc.Submission, body string) (string, error) {
	switch {
	case r.Isolation != engine.IsolatedDelegation || r.ActivationID <= 0:
		if len(r.RunInputIDs) > 1 {
			return "shared_run", nil
		}
		return "no_activation", nil
	case source.ChannelRevoked != 0:
		return "revoked", nil
	case f.State != "open" || source.Status != "pending" || source.StopRequested != 0 || child.AbortPending != 0:
		return "not_open", nil
	case f.StagedReport.Valid:
		return "already_staged", nil
	}
	parent, err := followUpParent(ctx, q, f, child, source)
	if err != nil {
		return "", err
	}
	if parent.ID == "" {
		return "no_lineage", nil
	}
	if parent.ChannelRevoked != 0 {
		return "revoked", nil
	}
	archived, err := q.ConversationAgentArchived(ctx, f.ConversationID)
	if err != nil {
		return "", err
	}
	if archived != 0 {
		return "archived", nil
	}
	// The tool can run before control records native admission. The native
	// owner proof recovers the same immutable binding in this transaction.
	bound, err := bindChannelFollowUp(ctx, q, source.ID, r.ActivationID)
	if err != nil {
		return "", err
	}
	if bound != 1 {
		return "no_activation", nil
	}
	return "", q.StageChannelFollowUpReport(ctx, sqlc.StageChannelFollowUpReportParams{DelegationID: source.ID, StagedReport: sql.NullString{String: body, Valid: true}})
}

// cancelChannelFollowUps runs inside the Stop or Clear transaction. Outbox revocation
// prevents a future send; a worker may still record a receipt for a send already in flight.
func cancelChannelFollowUps(ctx context.Context, q *sqlc.Queries, chatID int64) error {
	ids, err := q.CancelChannelFollowUps(ctx, chatID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := q.RevokeOutboxBySubmission(ctx, sql.NullString{String: id, Valid: true}); err != nil {
			return err
		}
	}
	return nil
}
