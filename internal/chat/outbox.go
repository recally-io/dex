package chat

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

// UserOrigin reports whether a person on the web or a channel wrote an input with this origin.
func UserOrigin(origin string) bool { return isUserOrigin(origin) }

func isUserOrigin(origin string) bool {
	return origin == "web" ||
		strings.HasPrefix(origin, "telegram:") ||
		strings.HasPrefix(origin, "feishu:") ||
		strings.HasPrefix(origin, "wechat:")
}

func peerFromOrigin(origin string) string {
	if strings.Contains(origin, ":") {
		return origin
	}
	return ""
}

func boolPtrOrTrue(v *bool) bool {
	if v == nil {
		return true
	}
	return *v
}

func (s *Service) ToolNotifyUser(ctx context.Context, r engine.TaskContext, a NotifyUserArgs) (string, error) {
	if strings.TrimSpace(a.Content) == "" {
		return "", errors.New("notify_user requires non-empty content")
	}
	conv, err := s.toolCaller(ctx, r)
	if err != nil {
		return "", err
	}
	if conv.ParentID.Valid {
		return "", errors.New("child sessions cannot call notify_user; use session_report to report to the parent instead")
	}
	source, err := s.q.GetSubmission(ctx, r.SourceRequestID)
	if err != nil {
		return "", err
	}
	if source.ConversationID != conv.ID {
		return "", errors.New("notify_user source belongs to another conversation")
	}
	now := s.now().UnixMilli()
	eventID := "notify:" + r.Key
	outboxID := eventID
	final := boolPtrOrTrue(a.Final)

	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	source, err = q.GetSubmission(ctx, source.ID)
	if err != nil {
		return "", err
	}
	if existing, err := q.FindNotificationReceipt(ctx, eventID); err == nil {
		return existing, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	peer, identity, err := notificationRoute(ctx, q, conv, a.Channel)
	if err != nil {
		return "", err
	}
	writeWeb := a.Channel == "" && (source.Origin == "web" || source.Producer != ProducerUser)
	if peer != "" {
		if a.TopicKey != "" {
			if _, err := q.SupersedeOutboxTopic(ctx, sqlc.SupersedeOutboxTopicParams{
				ConversationID: conv.ID, Peer: peer, TopicKey: a.TopicKey,
			}); err != nil {
				return "", err
			}
		}
		if _, err := q.InsertOutboxMessage(ctx, sqlc.InsertOutboxMessageParams{
			ID: outboxID, EventID: eventID, AgentID: conv.AgentID, ConversationID: conv.ID,
			SourceSubmissionID: sql.NullString{String: source.ID, Valid: true},
			Kind:               "proactive", Peer: peer, ChannelContext: identity, TopicKey: a.TopicKey,
			Body: a.Content, Status: "pending", CreatedAt: now,
		}); err != nil {
			return "", err
		}
	} else if !writeWeb {
		return "", errors.New("no default notification channel is configured")
	}
	if writeWeb {
		if err := q.InsertWebNotification(ctx, sqlc.InsertWebNotificationParams{
			ID: outboxID, EventID: eventID, AgentID: conv.AgentID, ConversationID: conv.ID,
			SourceSubmissionID: source.ID, Body: a.Content, CreatedAt: now,
		}); err != nil {
			return "", err
		}
	}
	if final {
		if peer == source.Origin && strings.HasPrefix(peer, "telegram:") {
			if err := bindFileDeliveries(ctx, q, outboxID, source.ID); err != nil {
				return "", err
			}
		} else if err := q.DeleteUnboundOutboxFiles(ctx, sql.NullString{String: source.ID, Valid: true}); err != nil {
			return "", err
		}
		for _, id := range r.RunInputIDs {
			if err := q.SetSubmissionExitAction(ctx, sqlc.SetSubmissionExitActionParams{ID: id, ExitAction: "notify_user"}); err != nil {
				return "", err
			}
			if peer != "" {
				if err := q.SetRequiredChannelReply(ctx, sqlc.SetRequiredChannelReplyParams{SubmissionID: id, OutboxID: outboxID}); err != nil {
					return "", err
				}
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	if peer != "" && s.notify != nil {
		s.notify(peer)
	}
	s.signal(conv.ID)
	return outboxID, nil
}

// ToolReportParent writes a report wake for the direct parent session.
func (s *Service) ToolReportParent(ctx context.Context, r engine.TaskContext, a ReportParentArgs) (string, error) {
	if strings.TrimSpace(a.Content) == "" {
		return "", errors.New("session_report requires non-empty content")
	}
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	conv, err := toolCallerIn(ctx, q, r)
	if err != nil {
		return "", err
	}
	source, err := q.GetSubmission(ctx, r.SourceRequestID)
	if err != nil {
		return "", err
	}
	bodyText, cut := cutRunes(a.Content, textLimit)
	if cut {
		bodyText += fmt.Sprintf("\n[The report is cut. Read the child session with session_read: id %d.]", conv.ID)
	}
	if !conv.ParentID.Valid {
		return "", errors.New("only child sessions can report to a parent")
	}
	if archived, err := q.ConversationAgentArchived(ctx, conv.ParentID.Int64); err != nil {
		return "", err
	} else if archived != 0 {
		return "", errors.New("parent session agent is archived")
	}
	body := sessionResult{
		Session: strconv.FormatInt(conv.ID, 10),
		Title:   conv.Title,
		Turn:    source.ID,
		Outcome: "done",
		Answer:  bodyText,
	}
	content, err := json.Marshal(struct {
		Result sessionResult `json:"task-result"`
	}{body})
	if err != nil {
		return "", err
	}
	// A replay uses the original tool task key. The insert ignores that same ID.
	resultID := "report:" + r.Key
	now := s.now().UnixMilli()
	if _, err := q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{
		ID:             resultID,
		ConversationID: conv.ParentID.Int64,
		Content:        string(content),
		Attachments:    "null",
		Origin:         cmp.Or(source.Origin, "web"),
		Producer:       ProducerSubSession,
		Kind:           "task_result",
		CreatedAt:      now,
	}); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	s.signal(conv.ParentID.Int64)
	s.Wake()
	return resultID, nil
}

func (s *Service) ToolRecentOutbox(ctx context.Context, ref engine.Ref) ([]OutboxSummary, error) {
	conv, err := s.q.GetConversationByExecution(ctx, sqlc.GetConversationByExecutionParams{
		AgentID: ref.AgentID,

		ExecutionID: sql.NullInt64{Int64: ref.ID, Valid: true},
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListRecentDeliveredOutbox(ctx, conv.ID)
	if err != nil {
		return nil, err
	}
	out := make([]OutboxSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, OutboxSummary{
			ID:          row.ID,
			Kind:        row.Kind,
			Peer:        row.Peer,
			TopicKey:    row.TopicKey,
			Body:        cmp.Or(row.Body, row.Reason),
			DeliveredAt: row.DeliveredAt.Int64,
		})
	}
	return out, nil
}

// OutboxEntry is one outbound delivery record visible to the Web UI for a conversation.
type OutboxEntry struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Peer        string `json:"peer"`
	TopicKey    string `json:"topicKey"`
	Body        string `json:"body"`
	Reason      string `json:"reason"`
	Status      string `json:"status"`
	Error       string `json:"error"`
	RetryAt     int64  `json:"retryAt"`
	CreatedAt   int64  `json:"createdAt"`
	DeliveredAt int64  `json:"deliveredAt"`
}

// ListOutbox returns recent outbound messages for a conversation.
func (s *Service) ListOutbox(ctx context.Context, agentID string, chatID int64) ([]OutboxEntry, error) {
	if _, err := s.Get(ctx, agentID, chatID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListOutboxByConversation(ctx, chatID)
	if err != nil {
		return nil, err
	}
	out := make([]OutboxEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, OutboxEntry{
			ID:          row.ID,
			Kind:        row.Kind,
			Peer:        row.Peer,
			TopicKey:    row.TopicKey,
			Body:        row.Body,
			Reason:      row.Reason,
			Status:      row.Status,
			Error:       row.Error,
			RetryAt:     row.RetryAt,
			CreatedAt:   row.CreatedAt,
			DeliveredAt: row.DeliveredAt.Int64,
		})
	}
	return out, nil
}

func writeOutboxOnSettle(ctx context.Context, q *sqlc.Queries, sub sqlc.Submission, conv sqlc.Conversation, event engine.Outcome, now int64) (string, error) {
	if conv.ParentID.Valid {
		return "", nil
	}
	if sub.ExitAction == "ask_user" {
		question, err := q.GetQuestionBySource(ctx, sub.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		if question.Queued != 0 {
			return "", nil
		}
	}
	// Priority 1: delegated child inputs only report back to their parent session, never directly to a channel.
	if sub.ParentRequestID.Valid {
		return "", nil
	}
	if event.Status == "done" {
		if sub.Kind != "message" {
			return "", nil
		}
		runIDs := event.RunInputIDs
		if len(runIDs) == 0 {
			if event.AnswerID > 0 {
				// Run membership/order cannot be verified for this native answer; never guess a destination.
				if err := q.DeleteUnboundOutboxFiles(ctx, sql.NullString{String: sub.ID, Valid: true}); err != nil {
					return "", err
				}
				return "", nil
			}
			runIDs = []string{sub.ID}
		}
		var firstRow *sqlc.Submission
		var lastUser *sqlc.Submission
		for i, rid := range runIDs {
			row, err := q.GetSubmission(ctx, rid)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return "", err
			}
			if i == 0 {
				cp := row
				firstRow = &cp
			}
			if row.Producer == ProducerUser && row.Kind == "message" {
				cp := row
				lastUser = &cp
			}
		}
		// Pure background runs (no user input) never auto-send to channels; only the last user input
		// placed in the answering run decides whether the final reply goes to Web or a Channel.
		// When the Run's source input shares the winning channel destination (e.g. back-to-back
		// messages from the same Telegram peer in one QueueAll run), let the source input own the
		// Outbox row so send_file and ask_user side effects bound to Inputs[0] stay attached.
		var owner *sqlc.Submission
		if lastUser != nil && lastUser.ReplyChannel != "" && lastUser.ReplyPeer != "" {
			owner = lastUser
			if firstRow != nil && firstRow.Producer == ProducerUser && firstRow.Kind == "message" &&
				firstRow.ReplyChannel == lastUser.ReplyChannel && firstRow.ReplyPeer == lastUser.ReplyPeer {
				owner = firstRow
			}
		}
		if owner == nil || owner.ID != sub.ID {
			if err := q.DeleteUnboundOutboxFiles(ctx, sql.NullString{String: sub.ID, Valid: true}); err != nil {
				return "", err
			}
			return "", nil
		}
		if sub.ExitAction == "notify_user" {
			if err := q.DeleteUnboundOutboxFiles(ctx, sql.NullString{String: sub.ID, Valid: true}); err != nil {
				return "", err
			}
			return "", nil
		}
		peer := owner.ReplyChannel + ":" + owner.ReplyPeer
		kind := "reply"
		if sub.ExitAction == "ask_user" {
			kind = "question"
		} else if _, err := q.GetQuestionBySource(ctx, sub.ID); err == nil {
			kind = "question"
		} else if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		identity := sub.ChannelContext
		eventID := "reply:" + conv.AgentID + ":" + sub.ID
		if event.AnswerID > 0 {
			eventID = fmt.Sprintf("reply:%s:%d:%d", conv.AgentID, conv.ID, event.AnswerID)
		}
		status, deliveredAt := outboxInitialStatus(peer, sub.ChannelRevoked, now)
		inserted, err := q.InsertOutboxMessage(ctx, sqlc.InsertOutboxMessageParams{
			ID:                 sub.ID,
			EventID:            eventID,
			AgentID:            conv.AgentID,
			ConversationID:     conv.ID,
			SourceSubmissionID: sql.NullString{String: sub.ID, Valid: true},
			Kind:               kind,
			Peer:               peer,
			ChannelContext:     identity,
			TopicKey:           "",
			Body:               event.Answer,
			Reason:             "",
			Status:             status,
			CreatedAt:          now,
			DeliveredAt:        deliveredAt,
		})
		if err != nil {
			return "", err
		}
		outboxID := sub.ID
		if inserted == 0 {
			existing, err := q.GetOutboxByEvent(ctx, sqlc.GetOutboxByEventParams{EventID: eventID, Peer: peer})
			if err != nil {
				return "", err
			}
			outboxID = existing.ID
		}
		if err := q.SetRequiredChannelReply(ctx, sqlc.SetRequiredChannelReplyParams{SubmissionID: sub.ID, OutboxID: outboxID}); err != nil {
			return "", err
		}
		if err := bindFileDeliveries(ctx, q, outboxID, sub.ID); err != nil {
			return "", err
		}
		if status == "pending" {
			return peer, nil
		}
		return "", nil
	}

	if sub.Kind == "message" && sub.Producer == ProducerUser && sub.ReplyChannel != "" && sub.ReplyPeer != "" {
		if event.Reason == "withdrawn" || event.Reason == "stopped" || event.Reason == "aborted" || sub.ChannelRevoked != 0 {
			return "", nil
		}
		peer := sub.ReplyChannel + ":" + sub.ReplyPeer
		status, deliveredAt := outboxInitialStatus(peer, sub.ChannelRevoked, now)
		if _, err := q.InsertOutboxMessage(ctx, sqlc.InsertOutboxMessageParams{
			ID:                 sub.ID,
			EventID:            "reply:" + conv.AgentID + ":" + sub.ID,
			AgentID:            conv.AgentID,
			ConversationID:     conv.ID,
			SourceSubmissionID: sql.NullString{String: sub.ID, Valid: true},
			Kind:               "reply",
			Peer:               peer,
			ChannelContext:     sub.ChannelContext,
			TopicKey:           "",
			Body:               "",
			Reason:             event.Reason,
			Status:             status,
			CreatedAt:          now,
			DeliveredAt:        deliveredAt,
		}); err != nil {
			return "", err
		}
		if status == "pending" {
			return peer, nil
		}
		return "", nil
	}

	return "", nil
}

func outboxInitialStatus(peer string, channelRevoked int64, now int64) (string, sql.NullInt64) {
	if peer == "" {
		return "sent", sql.NullInt64{Int64: now, Valid: true}
	}
	if channelRevoked != 0 {
		return "failed", sql.NullInt64{}
	}
	return "pending", sql.NullInt64{}
}

func notificationRoute(ctx context.Context, q *sqlc.Queries, conv sqlc.Conversation, channel string) (string, string, error) {
	if channel == "" {
		target, err := q.GetPrimaryNotificationTarget(ctx, conv.AgentID)
		if errors.Is(err, sql.ErrNoRows) {
			return "", "{}", nil
		}
		if err != nil {
			return "", "", err
		}
		identity, err := json.Marshal(ChannelContext{AccountID: target.AccountID})
		return target.Peer, string(identity), err
	}
	platform, _, explicitPeer := strings.Cut(channel, ":")
	paired, err := q.ListPairedChannel(ctx, sqlc.ListPairedChannelParams{AgentID: conv.AgentID, Platform: platform})
	if err != nil {
		return "", "", err
	}
	if !explicitPeer && len(paired) > 1 {
		return "", "", errors.New("multiple targets are paired; specify the channel and target")
	}
	for _, p := range paired {
		if !explicitPeer || p.Peer == channel {
			identity, err := json.Marshal(ChannelContext{AccountID: p.AccountID})
			return p.Peer, string(identity), err
		}
	}
	return "", "", errors.New("notification channel is not paired")
}

func bindFileDeliveries(ctx context.Context, q *sqlc.Queries, outboxID, sourceID string) error {
	source := sql.NullString{String: sourceID, Valid: true}
	if err := q.CreateFileDeliveries(ctx, sqlc.CreateFileDeliveriesParams{OutboxID: outboxID, SubmissionID: source}); err != nil {
		return err
	}
	if err := q.BindFileDeliveries(ctx, source); err != nil {
		return err
	}
	// An unpaired destination has no pending row to own the snapshot.
	return q.DeleteUnboundOutboxFiles(ctx, source)
}

// WakeAutomationOutbox nudges channel delivery after commit without waking any conversation.
func (s *Service) WakeAutomationOutbox(ctx context.Context, runID string) {
	if s.notify != nil {
		if row, err := s.q.GetOutboxMessage(ctx, "routine:"+runID); err == nil && row.Status == "pending" {
			s.notify(row.Peer)
		}
	}
}

func routineFailure(run sqlc.AutomationRun) bool {
	if run.Status == "skipped" {
		switch run.Reason.String {
		case RoutineSkipNoModel, RoutineSkipMissed, routineSkipTargetSession:
			return true
		default:
			reason := run.Reason.String
			return strings.HasPrefix(reason, routineRunnerPrefix) &&
				(strings.HasSuffix(reason, routineUnavailableSuffix) || strings.HasSuffix(reason, routineRemovedSuffix))
		}
	}
	return run.Outcome == "failed"
}

func queueAutomationOutbox(ctx context.Context, q *sqlc.Queries, runID string, convID int64, now int64) error {
	run, err := q.GetAutomationRun(ctx, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // A non-Routine input also uses settlement.
	}
	if err != nil {
		return err
	}
	failure := routineFailure(run)
	if failure {
		// Only the immediately preceding finished run can suppress a failure alert. Do not
		// use or update automation_report's business duplicate flag for this decision.
		prior, err := q.GetPreviousFinishedAutomationRun(ctx, runID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && routineFailure(prior.AutomationRun) && prior.AutomationRun.Reason.String == run.Reason.String &&
			!routineCanceled(prior.ExecutionStatus.String, prior.ExecutionReason.String) {
			return nil
		}
	} else if run.Outcome != "changed" || run.Duplicate != 0 {
		return nil
	}
	info, err := q.GetAutomationOutboxInfo(ctx, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return q.SetAutomationRunNotificationNote(ctx, sqlc.SetAutomationRunNotificationNoteParams{ID: runID, NotificationNote: "not delivered: no notification target"})
	}
	if err != nil {
		return err
	}
	source := sql.NullString{String: runID, Valid: convID != 0}
	if convID == 0 {
		convID, err = q.GetAutomationOutboxConversation(ctx, info.AgentID)
		if errors.Is(err, sql.ErrNoRows) {
			return q.SetAutomationRunNotificationNote(ctx, sqlc.SetAutomationRunNotificationNoteParams{ID: runID, NotificationNote: "not delivered: no existing bot conversation"})
		}
		if err != nil {
			return err
		}
	}
	outcome := run.Outcome
	if failure {
		outcome = "failed"
	}
	body := info.Name + ": " + outcome + "\n" + cmp.Or(run.Summary, run.Reason.String)
	// A failure alert is not a replaceable result summary. A different reason (or the
	// same reason after a success) must not supersede an alert still waiting to send.
	var topicKey string
	if !failure {
		topicKey = "routine:" + run.AutomationID
		if _, err := q.SupersedeOutboxTopic(ctx, sqlc.SupersedeOutboxTopicParams{
			ConversationID: convID,
			Peer:           info.Peer,
			TopicKey:       topicKey,
		}); err != nil {
			return err
		}
	}
	chCtx, err := json.Marshal(struct {
		AccountID string `json:"accountId"`
	}{AccountID: info.AccountID})
	if err != nil {
		return err
	}
	_, err = q.InsertOutboxMessage(ctx, sqlc.InsertOutboxMessageParams{
		ID:                 "routine:" + runID,
		EventID:            "remind:" + runID,
		AgentID:            info.AgentID,
		ConversationID:     convID,
		SourceSubmissionID: source,
		Kind:               "automation",
		Peer:               info.Peer,
		ChannelContext:     string(chCtx),
		TopicKey:           topicKey,
		Body:               body,
		Reason:             "",
		Status:             "pending",
		CreatedAt:          now,
		DeliveredAt:        sql.NullInt64{},
	})
	return err
}
