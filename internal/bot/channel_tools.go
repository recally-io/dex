package bot

import (
	"cmp"
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"maps"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/session"
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

func (s *Service) ChannelToolHandlers(sessions *session.Service) ChannelTools {
	return ChannelTools{
		Read: func(ctx context.Context, c engine.TaskContext, view, bot string) (any, error) {
			if _, err := sessions.ChannelToolCaller(ctx, c); err != nil {
				return nil, err
			}
			return s.channelToolRead(ctx, cmp.Or(bot, c.BotID), view)
		},
		Request: func(ctx context.Context, c engine.TaskContext, bot, op, target string) (any, error) {
			session, err := sessions.ChannelToolCaller(ctx, c)
			if err != nil {
				auditChannelMutation("denied", channelApproval{caller: c.BotID})
				return nil, err
			}
			return s.requestChannelChange(ctx, c.BotID, session, cmp.Or(bot, c.BotID), op, target)
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
	var err error
	tgState, tgPaired := "off", false
	if s.telegram != nil {
		tgState, tgPaired, err = s.telegram.State(ctx, id)
		if err != nil {
			return nil, errors.New("cannot read channel settings")
		}
	}
	fsState, fsPaired := "off", false
	if s.feishu != nil {
		fsState, fsPaired, err = s.feishu.State(ctx, id)
		if err != nil {
			return nil, errors.New("cannot read channel settings")
		}
	}
	wxState, wxPaired := "off", false
	if s.wechat != nil {
		wxState, wxPaired, err = s.wechat.State(ctx, id)
		if err != nil {
			return nil, errors.New("cannot read channel settings")
		}
	}
	if view == "channel_pairing" {
		return map[string]bool{"telegram": tgPaired, "feishu": fsPaired, "wechat": wxPaired}, nil
	}
	// Provider runtime errors can contain upstream response text. Return states only.
	return map[string]string{"telegram": tgState, "feishu": fsState, "wechat": wxState}, nil
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
	bot, err := s.q.GetBot(ctx, id)
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
	switch platform {
	case "telegram":
		if s.telegram == nil {
			return [sha256.Size]byte{}, ErrChannelApproval
		}
		return s.telegram.FingerprintLocked(ctx, bot)
	case "feishu":
		if s.feishu == nil {
			return [sha256.Size]byte{}, ErrChannelApproval
		}
		return s.feishu.FingerprintLocked(ctx, bot)
	case "wechat":
		if s.wechat == nil {
			return [sha256.Size]byte{}, ErrChannelApproval
		}
		return s.wechat.FingerprintLocked(ctx, bot)
	default:
		return [sha256.Size]byte{}, ErrChannelApproval
	}
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
	conv, err := s.q.GetSession(ctx, p.session)
	if err != nil || conv.BotID != p.caller || conv.ArchivedAt.Valid || conv.ParentID.Valid || !session.Writable(conv.ChannelKey) {
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
			return s.telegram.DisconnectLocked(ctx, p.BotID)
		case "feishu":
			return s.feishu.DisconnectLocked(ctx, p.BotID)
		case "wechat":
			return s.wechat.DisconnectLocked(ctx, p.BotID)
		}
	case "revoke_pairing":
		switch p.target {
		case "telegram":
			return s.telegram.RevokePairingLocked(ctx, p.BotID)
		case "feishu":
			return s.feishu.RevokePairingLocked(ctx, p.BotID)
		}
	}
	return ErrChannelApproval
}

func auditChannelMutation(outcome string, p channelApproval) {
	slog.Info("channel tool mutation", "outcome", outcome, "operation", p.operation, "caller", p.caller, "session", p.session, "bot", p.BotID)
}
