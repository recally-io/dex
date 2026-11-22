package chat

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"strings"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

var errChannelFollowUp = errors.New("follow-up requires an active commitment and the original channel authorization")

const followUpPrompt = "You committed to report on this delegation. Plain final text is internal. Use notify_user for your reviewed conclusion, ask_user if blocked on a decision, or stay_silent to intentionally suppress the follow-up. Do not forward private drafts or raw child output."

func followUpFinalKey(delegationID string) string { return "follow-up-final:" + delegationID }

func createChannelFollowUp(ctx context.Context, q *sqlc.Queries, parent, child sqlc.Conversation, source sqlc.Submission, turn string) error {
	// Group sessions have their own tool policy and cannot gain notify_user via a delegation.
	if source.Kind != "message" || !isUserOrigin(source.Origin) || peerFromOrigin(source.Origin) == "" || source.ConversationID != parent.ID || parent.ExecutionScope != "" || !source.SourceKey.Valid || source.ChannelRevoked != 0 {
		return errChannelFollowUp
	}
	platform, key, _ := strings.Cut(source.SourceKey.String, ":")
	account, _, ok := strings.Cut(key, ":")
	if !ok || account == "" || !strings.HasPrefix(source.Origin, platform+":") {
		return errChannelFollowUp
	}
	sender := strings.TrimPrefix(source.Origin, platform+":")
	if platform == "telegram" {
		var identity ChannelContext
		if err := json.Unmarshal([]byte(source.ChannelContext), &identity); err != nil {
			return err
		}
		if identity.AccountID != account || identity.SenderID == "" || groupPeer(source.Origin) {
			return errChannelFollowUp
		}
		sender = identity.SenderID
	}
	f := sqlc.ChannelFollowUp{DelegationID: turn, SourceID: source.ID, ConversationID: parent.ID, ChildID: child.ID, AgentID: parent.AgentID, Peer: source.Origin, AccountID: account, SenderID: sender, ChannelContext: source.ChannelContext, ResultID: "result:" + turn}
	if allowed, err := channelFollowUpAllowed(ctx, q, f); err != nil || !allowed {
		if err != nil {
			return err
		}
		return errChannelFollowUp
	}
	return q.CreateChannelFollowUp(ctx, sqlc.CreateChannelFollowUpParams{
		DelegationID: f.DelegationID, SourceID: f.SourceID, ConversationID: f.ConversationID, ChildID: f.ChildID, AgentID: f.AgentID,
		Peer: f.Peer, AccountID: f.AccountID, SenderID: f.SenderID, ChannelContext: f.ChannelContext, ResultID: f.ResultID,
	})
}

func channelFollowUpAllowed(ctx context.Context, q *sqlc.Queries, f sqlc.ChannelFollowUp) (bool, error) {
	source, err := q.GetSubmission(ctx, f.SourceID)
	if err != nil {
		return false, err
	}
	if f.State == "cancelled" || source.ChannelRevoked != 0 || source.ConversationID != f.ConversationID || source.Origin != f.Peer || source.ChannelContext != f.ChannelContext {
		return false, nil
	}
	if archived, err := q.ConversationAgentArchived(ctx, f.ConversationID); err != nil || archived != 0 {
		return false, err
	}
	switch platform, _, _ := strings.Cut(f.Peer, ":"); platform {
	case "telegram":
		err := authorizeTelegramActor(ctx, q, f.AgentID, f.AccountID, f.SenderID)
		if errors.Is(err, ErrGroupUnauthorized) {
			return false, nil
		}
		return err == nil, err
	case "feishu":
		settings, err := q.GetFeishuSettings(ctx, f.AgentID)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return err == nil && settings.AppID == f.AccountID && settings.OwnerID == f.SenderID, err
	case "wechat":
		settings, err := q.GetWechatSettings(ctx, f.AgentID)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return err == nil && settings.BotID == f.AccountID && settings.Owner == f.SenderID, err
	}
	return false, nil
}

// settledFollowUp handles only the original result System Turn. It never uses ordinary final text
// as delivery content. The question body was composed solely from the explicit ask_user call.
func settledFollowUp(ctx context.Context, q *sqlc.Queries, f sqlc.ChannelFollowUp, sub sqlc.Submission, conv sqlc.Conversation, event engine.Outcome, now int64) error {
	if f.State != "open" {
		return nil
	}
	state := "missed_follow_up"
	switch allowed, err := channelFollowUpAllowed(ctx, q, f); {
	case err != nil:
		return err
	case !allowed, event.Reason == "stopped", event.Reason == "aborted", event.Reason == "withdrawn":
		state = "cancelled"
	case sub.ExitAction == "ask_user" && event.Status == "done":
		sub.Origin, sub.ChannelContext = f.Peer, f.ChannelContext
		_, err := writeOutboxOnSettle(ctx, q, sub, conv, event, now)
		return err
	}
	return q.SetOpenChannelFollowUpState(ctx, sqlc.SetOpenChannelFollowUpStateParams{DelegationID: f.DelegationID, State: state})
}

// ChannelFollowUpReplyAllowed is the extra Outbox authorization check for the dedicated
// follow-up source. Other Outbox messages keep their existing delivery rules.
func (s *Service) ChannelFollowUpReplyAllowed(ctx context.Context, agentID, inputID, peer, identity string) (bool, error) {
	f, err := s.q.GetChannelFollowUpByInput(ctx, inputID)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if f.AgentID != agentID || f.Peer != peer || f.ChannelContext != identity {
		return false, nil
	}
	return channelFollowUpAllowed(ctx, s.q, f)
}

// requireChannelFollowUp checks live authorization inside the effect's writer transaction.
func requireChannelFollowUp(ctx context.Context, q *sqlc.Queries, f sqlc.ChannelFollowUp, conv sqlc.Conversation, sub sqlc.Submission) error {
	if f.AgentID != conv.AgentID || f.ConversationID != conv.ID || sub.ConversationID != conv.ID || sub.Status != "pending" || sub.ChannelRevoked != 0 {
		return errChannelFollowUp
	}
	allowed, err := channelFollowUpAllowed(ctx, q, f)
	if err != nil {
		return err
	}
	if !allowed {
		return errChannelFollowUp
	}
	return nil
}
