package agent

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"maps"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/engine"
)

const (
	ChannelApprovalPath = "/channel-approvals/"
	channelApprovalTTL  = 10 * time.Minute
	maxChannelApprovals = 100
)

var ErrChannelApproval = errors.New("channel change is unavailable, expired, or already used")

// ChannelApproval is the server-owned review page data. It has no credentials.
type ChannelApproval struct {
	BotID, BotName, Description, Destination string
}

type channelApproval struct {
	ChannelApproval
	caller      string
	session     int64
	operation   string
	target      string
	fingerprint [sha256.Size]byte
	expires     time.Time
}

func (s *Service) ChannelToolHandlers(chats *chat.Service) engine.ChannelTools {
	return engine.ChannelTools{
		Read: func(ctx context.Context, c engine.TaskContext, view, bot string) (any, error) {
			if _, err := chats.ChannelToolCaller(ctx, c); err != nil {
				return nil, err
			}
			return s.channelToolRead(ctx, cmp.Or(bot, c.AgentID), view)
		},
		Request: func(ctx context.Context, c engine.TaskContext, bot, op, target string) (any, error) {
			session, err := chats.ChannelToolCaller(ctx, c)
			if err != nil {
				auditChannelMutation("denied", channelApproval{caller: c.AgentID})
				return nil, err
			}
			return s.requestChannelChange(ctx, c.AgentID, session, cmp.Or(bot, c.AgentID), op, target)
		},
	}
}

func (s *Service) channelToolRead(ctx context.Context, id, view string) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if db.RequireActive(ctx, s.q, id) != nil {
		return nil, errors.New("bot is not available")
	}
	if view == "channel_notification_targets" {
		targets, err := s.notificationTargets(ctx, id)
		if err != nil {
			return nil, errors.New("cannot read notification destinations")
		}
		return targets, nil
	}
	channels, err := s.channels(ctx, id)
	if err != nil {
		return nil, errors.New("cannot read channel settings")
	}
	if view == "channel_pairing" {
		return map[string]bool{"telegram": len(channels.Telegram.AllowedUsers) != 0, "feishu": channels.Feishu.Paired, "wechat": channels.Wechat.Enabled}, nil
	}
	// Provider runtime errors can contain upstream response text. Return states only.
	return map[string]string{"telegram": channels.Telegram.Status.State, "feishu": channels.Feishu.Status, "wechat": channels.Wechat.Status.State}, nil
}

func (s *Service) requestChannelChange(ctx context.Context, caller string, session int64, id, op, target string) (_ any, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := channelApproval{caller: caller, session: session}
	defer func() {
		if err != nil {
			auditChannelMutation("denied", p)
		}
	}()
	switch op {
	case "disconnect", "revoke_pairing", "set_notification_target", "clear_notification_target":
		p.operation = op
	default:
		return nil, ErrChannelApproval
	}
	bot, err := s.q.GetAgent(ctx, id)
	if err != nil || bot.ArchivedAt.Valid {
		return nil, ErrChannelApproval
	}
	p.BotName, p.BotID, p.target = bot.Name, bot.ID, target
	p.expires = time.Now().Add(channelApprovalTTL)
	switch op {
	case "disconnect", "revoke_pairing":
		if op == "revoke_pairing" && target != "telegram" && target != "feishu" {
			return nil, errors.New("only Telegram and Feishu support revoking pairing; disconnect WeChat instead")
		}
		p.fingerprint, err = s.channelFingerprint(ctx, id, target)
		if err != nil {
			return nil, ErrChannelApproval
		}
		p.Destination = target
		p.Description = "Disconnect the channel and remove its saved credentials"
		if op == "revoke_pairing" {
			p.Description = "Revoke the current pairing and invalidate any pairing code"
		}
	case "set_notification_target":
		list, err := s.notificationTargets(ctx, id)
		if err != nil {
			return nil, ErrChannelApproval
		}
		for _, d := range list.Targets {
			if d.ID == target {
				p.Destination = d.Peer
				break
			}
		}
		if p.Destination == "" {
			return nil, ErrChannelApproval
		}
		p.Description = "Set the primary notification destination"
	case "clear_notification_target":
		if target != "" {
			return nil, ErrChannelApproval
		}
		p.Description = "Clear the primary notification destination"
	}
	if s.channelApprovals == nil {
		s.channelApprovals = make(map[string]channelApproval)
	}
	maps.DeleteFunc(s.channelApprovals, func(_ string, p channelApproval) bool { return !time.Now().Before(p.expires) })
	if len(s.channelApprovals) >= maxChannelApprovals {
		return nil, errors.New("too many pending channel changes; retry after they expire")
	}
	key := uuid.New().String()
	s.channelApprovals[key] = p
	return map[string]string{"status": "pending_owner_confirmation", "approvalPath": ChannelApprovalPath + key}, nil
}

// channelFingerprint detects replacement of the account or pairing before approval.
// Only the hash stays in the pending request, and it never leaves control.
func (s *Service) channelFingerprint(ctx context.Context, bot, platform string) ([sha256.Size]byte, error) {
	var config any
	var err error
	session := s.sessions[bot]
	switch platform {
	case "telegram":
		row, e := s.q.GetTelegramSettings(ctx, bot)
		row.RetryAt = 0
		config, err = row, e
	case "feishu":
		row, e := s.q.GetFeishuSettings(ctx, bot)
		var setup string
		if session != nil {
			setup = session.feishuSessionID
		}
		config, err = []any{row, setup}, e
	case "wechat":
		row, e := s.q.GetWechatSettings(ctx, bot)
		var setup string
		if session != nil {
			setup = session.loginID
		}
		config, err = []any{row, setup}, e
	default:
		return [sha256.Size]byte{}, ErrChannelApproval
	}
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	encoded, err := json.Marshal(config)
	return sha256.Sum256(encoded), err
}

func (s *Service) ChannelApproval(id string) (ChannelApproval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.channelApprovals[id]
	if !ok || !time.Now().Before(p.expires) {
		return ChannelApproval{}, ErrChannelApproval
	}
	return p.ChannelApproval, nil
}

// ConfirmChannelChange is called only by the owner surface. It takes no
// operation or target from the request. Consume the request before execution.
func (s *Service) ConfirmChannelChange(ctx context.Context, id string) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.channelApprovals[id]
	delete(s.channelApprovals, id)
	defer func() {
		outcome := "applied"
		if err != nil {
			outcome = "denied"
		}
		auditChannelMutation(outcome, p)
	}()
	if !ok || !time.Now().Before(p.expires) {
		return ErrChannelApproval
	}
	conv, err := s.q.GetConversation(ctx, p.session)
	if err != nil || conv.AgentID != p.caller || conv.ArchivedAt.Valid || conv.ParentID.Valid || conv.ExecutionScope != "" || !chat.Writable(conv.ChannelKey) {
		return ErrChannelApproval
	}
	if db.RequireActive(ctx, s.q, p.caller) != nil || db.RequireActive(ctx, s.q, p.BotID) != nil {
		return ErrChannelApproval
	}
	if p.operation == "disconnect" || p.operation == "revoke_pairing" {
		fingerprint, err := s.channelFingerprint(ctx, p.BotID, p.target)
		if err != nil || fingerprint != p.fingerprint {
			return ErrChannelApproval
		}
	}
	if s.applyChannelChange(ctx, p) != nil {
		return ErrChannelApproval
	}
	return nil
}

func (s *Service) applyChannelChange(ctx context.Context, p channelApproval) error {
	switch p.operation {
	case "set_notification_target", "clear_notification_target":
		return s.setPrimaryNotificationTarget(ctx, p.BotID, p.target, p.Destination)
	case "disconnect":
		switch p.target {
		case "telegram":
			return s.deleteTelegram(ctx, p.BotID)
		case "feishu":
			return s.deleteFeishu(ctx, p.BotID)
		case "wechat":
			return s.deleteWechat(ctx, p.BotID)
		}
	case "revoke_pairing":
		switch p.target {
		case "telegram":
			return s.setTelegram(ctx, p.BotID, "", nil, nil)
		case "feishu":
			return s.revokeFeishuPairing(ctx, p.BotID)
		}
	}
	return ErrChannelApproval
}

func auditChannelMutation(outcome string, p channelApproval) {
	slog.Info("channel tool mutation", "outcome", outcome, "operation", p.operation, "caller", p.caller, "session", p.session, "bot", p.BotID)
}
