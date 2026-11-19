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
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

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

func (s *Service) ToolNotifyUser(ctx context.Context, r engine.TaskContext, a engine.NotifyUserArgs) (string, error) {
	if strings.TrimSpace(a.Content) == "" {
		return "", errors.New("notify_user requires non-empty content")
	}
	conv, err := s.toolCaller(ctx, r)
	if err != nil {
		return "", err
	}
	if conv.ParentID.Valid {
		return "", errors.New("child sessions cannot call notify_user; use report_parent instead")
	}
	source, err := s.q.GetSubmission(ctx, r.SourceRequestID)
	if err != nil {
		return "", err
	}
	if source.ConversationID != conv.ID {
		return "", errors.New("notify_user source belongs to another conversation")
	}
	peer := peerFromOrigin(source.Origin)
	now := s.now().UnixMilli()
	status := "delivered"
	deliveredAt := sql.NullInt64{Int64: now, Valid: true}
	if peer != "" {
		if source.ChannelRevoked != 0 {
			status = "revoked"
			deliveredAt = sql.NullInt64{}
		} else {
			status = "pending"
			deliveredAt = sql.NullInt64{}
		}
	}
	dedupKey := "tool:" + r.Key
	outboxID := uuid.NewV7().String()
	final := boolPtrOrTrue(a.Final)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)

	if a.TopicKey != "" {
		if _, err := q.SupersedeOutboxTopic(ctx, sqlc.SupersedeOutboxTopicParams{
			ConversationID: conv.ID,
			Peer:           peer,
			TopicKey:       a.TopicKey,
		}); err != nil {
			return "", err
		}
	}
	inserted, err := q.InsertOutboxMessage(ctx, sqlc.InsertOutboxMessageParams{
		ID:                 outboxID,
		DedupKey:           dedupKey,
		AgentID:            conv.AgentID,
		ConversationID:     conv.ID,
		SourceSubmissionID: sql.NullString{String: source.ID, Valid: true},
		Kind:               "proactive",
		Peer:               peer,
		ChannelContext:     source.ChannelContext,
		TopicKey:           a.TopicKey,
		Body:               a.Content,
		Reason:             "",
		Status:             status,
		CreatedAt:          now,
		DeliveredAt:        deliveredAt,
	})
	if err != nil {
		return "", err
	}
	if inserted == 0 {
		existing, err := q.GetOutboxByDedupKey(ctx, dedupKey)
		if err != nil {
			return "", err
		}
		outboxID = existing.ID
	}
	if final {
		if _, err := q.BindOutboxFilesToOutbox(ctx, sqlc.BindOutboxFilesToOutboxParams{
			OutboxID:     sql.NullString{String: outboxID, Valid: true},
			SubmissionID: sql.NullString{String: source.ID, Valid: true},
		}); err != nil {
			return "", err
		}
		for _, id := range r.RunInputIDs {
			if err := q.SetSubmissionExitAction(ctx, sqlc.SetSubmissionExitActionParams{ID: id, ExitAction: "notify_user"}); err != nil {
				return "", err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	if peer != "" && status == "pending" && s.notify != nil {
		s.notify(peer)
	}
	s.signal(conv.ID)
	return outboxID, nil
}

func (s *Service) ToolReportParent(ctx context.Context, r engine.TaskContext, a engine.ReportParentArgs) (string, error) {
	if strings.TrimSpace(a.Content) == "" {
		return "", errors.New("report_parent requires non-empty content")
	}
	conv, err := s.toolCaller(ctx, r)
	if err != nil {
		return "", err
	}
	if !conv.ParentID.Valid {
		return "", errors.New("only child sessions can call report_parent")
	}
	if archived, err := s.q.ConversationAgentArchived(ctx, conv.ParentID.Int64); err != nil {
		return "", err
	} else if archived != 0 {
		return "", errors.New("parent session agent is archived")
	}
	source, err := s.q.GetSubmission(ctx, r.SourceRequestID)
	if err != nil {
		return "", err
	}
	bodyText, cut := cutRunes(a.Content, textLimit)
	if cut {
		bodyText += fmt.Sprintf("\n[The report is cut. Read the child session with session_read: id %d.]", conv.ID)
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
	resultID := "report:" + r.Key
	now := s.now().UnixMilli()
	final := boolPtrOrTrue(a.Final)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)

	if _, err := q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{
		ID:             resultID,
		ConversationID: conv.ParentID.Int64,
		Content:        string(content),
		Attachments:    "null",
		Origin:         cmp.Or(source.Origin, "web"),
		Kind:           "task_result",
		CreatedAt:      now,
	}); err != nil {
		return "", err
	}
	if final {
		for _, id := range r.RunInputIDs {
			if err := q.SetSubmissionExitAction(ctx, sqlc.SetSubmissionExitActionParams{ID: id, ExitAction: "report_parent"}); err != nil {
				return "", err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	s.signal(conv.ParentID.Int64)
	s.Wake()
	return resultID, nil
}

func (s *Service) ToolStaySilent(ctx context.Context, r engine.TaskContext, _ engine.StaySilentArgs) error {
	conv, err := s.toolCaller(ctx, r)
	if err != nil {
		return err
	}
	ids := r.RunInputIDs
	if len(ids) == 0 {
		ids = []string{r.SourceRequestID}
	}
	for _, id := range ids {
		sub, err := s.q.GetSubmission(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if sub.ConversationID == conv.ID && sub.Status == "pending" && sub.Kind == "message" && isUserOrigin(sub.Origin) {
			return errors.New("cannot stay silent when an unanswered user message is in this turn")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	for _, id := range ids {
		if err := q.SetSubmissionExitAction(ctx, sqlc.SetSubmissionExitActionParams{ID: id, ExitAction: "stay_silent"}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Service) ToolRecentOutbox(ctx context.Context, ref engine.Ref) ([]engine.OutboxSummary, error) {
	conv, err := s.q.GetConversationByExecution(ctx, sqlc.GetConversationByExecutionParams{
		AgentID:        ref.AgentID,
		ExecutionScope: ref.Scope,
		ExecutionID:    sql.NullInt64{Int64: ref.ID, Valid: true},
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
	out := make([]engine.OutboxSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, engine.OutboxSummary{
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

func writeOutboxOnSettle(ctx context.Context, q *sqlc.Queries, sub sqlc.Submission, conv sqlc.Conversation, event engine.Outcome, now int64) (string, error) {
	if conv.ParentID.Valid {
		return "", nil
	}
	isUserInput := sub.Kind == "message" && isUserOrigin(sub.Origin)
	peer := peerFromOrigin(sub.Origin)

	if isUserInput {
		if event.Status == "done" {
			if sub.ExitAction == "notify_user" {
				return "", nil
			}
			kind := "reply"
			if sub.ExitAction == "ask_user" {
				kind = "question"
			} else if _, err := q.GetQuestionBySource(ctx, sub.ID); err == nil {
				kind = "question"
			} else if !errors.Is(err, sql.ErrNoRows) {
				return "", err
			}
			dedupKey := "reply:" + sub.ID
			if event.AnswerID > 0 {
				dedupKey = fmt.Sprintf("reply:%d:%d:%s", conv.ID, event.AnswerID, peer)
			}
			status, deliveredAt := outboxInitialStatus(peer, sub.ChannelRevoked, now)
			inserted, err := q.InsertOutboxMessage(ctx, sqlc.InsertOutboxMessageParams{
				ID:                 sub.ID,
				DedupKey:           dedupKey,
				AgentID:            conv.AgentID,
				ConversationID:     conv.ID,
				SourceSubmissionID: sql.NullString{String: sub.ID, Valid: true},
				Kind:               kind,
				Peer:               peer,
				ChannelContext:     sub.ChannelContext,
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
				existing, err := q.GetOutboxByDedupKey(ctx, dedupKey)
				if err != nil {
					return "", err
				}
				outboxID = existing.ID
			}
			if _, err := q.BindOutboxFilesToOutbox(ctx, sqlc.BindOutboxFilesToOutboxParams{
				OutboxID:     sql.NullString{String: outboxID, Valid: true},
				SubmissionID: sql.NullString{String: sub.ID, Valid: true},
			}); err != nil {
				return "", err
			}
			if status == "pending" {
				return peer, nil
			}
			return "", nil
		}

		if event.Reason == "withdrawn" || sub.ChannelRevoked != 0 {
			return "", nil
		}
		status, deliveredAt := outboxInitialStatus(peer, sub.ChannelRevoked, now)
		if _, err := q.InsertOutboxMessage(ctx, sqlc.InsertOutboxMessageParams{
			ID:                 sub.ID,
			DedupKey:           "reply:" + sub.ID,
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

	// System Turn in a top-level conversation (Main, Chat, Automation).
	if event.Status == "done" {
		isQuestion := sub.ExitAction == "ask_user"
		if !isQuestion {
			if _, err := q.GetQuestionBySource(ctx, sub.ID); err == nil {
				isQuestion = true
			} else if !errors.Is(err, sql.ErrNoRows) {
				return "", err
			}
		}
		if !isQuestion {
			// Fail-quiet by default: internal trace only unless notify_user or ask_user was called.
			return "", nil
		}
		status, deliveredAt := outboxInitialStatus(peer, sub.ChannelRevoked, now)
		if _, err := q.InsertOutboxMessage(ctx, sqlc.InsertOutboxMessageParams{
			ID:                 sub.ID,
			DedupKey:           "question:" + sub.ID,
			AgentID:            conv.AgentID,
			ConversationID:     conv.ID,
			SourceSubmissionID: sql.NullString{String: sub.ID, Valid: true},
			Kind:               "question",
			Peer:               peer,
			ChannelContext:     sub.ChannelContext,
			TopicKey:           "",
			Body:               event.Answer,
			Reason:             "",
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

	if event.Reason == "stopped" || event.Reason == "withdrawn" || event.Reason == "aborted" || sub.ChannelRevoked != 0 {
		return "", nil
	}
	status, deliveredAt := outboxInitialStatus(peer, sub.ChannelRevoked, now)
	if _, err := q.InsertOutboxMessage(ctx, sqlc.InsertOutboxMessageParams{
		ID:                 "err:" + sub.ID,
		DedupKey:           "error:" + sub.ID,
		AgentID:            conv.AgentID,
		ConversationID:     conv.ID,
		SourceSubmissionID: sql.NullString{String: sub.ID, Valid: true},
		Kind:               "system_error",
		Peer:               peer,
		ChannelContext:     sub.ChannelContext,
		TopicKey:           "",
		Body:               fmt.Sprintf("System event (%s) failed: %s", sub.Kind, cmp.Or(event.Reason, "execution failed")),
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

func outboxInitialStatus(peer string, channelRevoked int64, now int64) (string, sql.NullInt64) {
	if peer == "" {
		return "delivered", sql.NullInt64{Int64: now, Valid: true}
	}
	if channelRevoked != 0 {
		return "revoked", sql.NullInt64{}
	}
	return "pending", sql.NullInt64{}
}

func queueRoutineOutbox(ctx context.Context, q *sqlc.Queries, runID string, convID int64, now int64) error {
	info, err := q.GetRoutineOutboxInfo(ctx, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	topicKey := "routine:" + info.AutomationID
	if _, err := q.SupersedeOutboxTopic(ctx, sqlc.SupersedeOutboxTopicParams{
		ConversationID: convID,
		Peer:           info.Peer,
		TopicKey:       topicKey,
	}); err != nil {
		return err
	}
	chCtx, err := json.Marshal(struct {
		AccountID string `json:"accountId"`
	}{AccountID: info.AccountID})
	if err != nil {
		return err
	}
	_, err = q.InsertOutboxMessage(ctx, sqlc.InsertOutboxMessageParams{
		ID:                 "routine:" + info.RunID,
		DedupKey:           "routine:" + info.RunID,
		AgentID:            info.AgentID,
		ConversationID:     convID,
		SourceSubmissionID: sql.NullString{String: runID, Valid: true},
		Kind:               "routine",
		Peer:               info.Peer,
		ChannelContext:     string(chCtx),
		TopicKey:           topicKey,
		Body:               info.Body,
		Reason:             "",
		Status:             "pending",
		CreatedAt:          now,
		DeliveredAt:        sql.NullInt64{},
	})
	return err
}
