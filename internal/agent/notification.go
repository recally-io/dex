package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// NotificationTarget describes a channel recipient authorized by the owner.
type NotificationTarget struct {
	ID       string `json:"id"`
	Platform string `json:"platform"`
	Peer     string `json:"peer"`
}

// NotificationTargets contains only this agent's currently authorized targets.
type NotificationTargets struct {
	Targets   []NotificationTarget `json:"targets"`
	PrimaryID string               `json:"primaryId"`
}

// NotificationTargets resolves destinations from configured channel owners, never arbitrary peers.
func (s *Service) NotificationTargets(ctx context.Context, agentID string) (NotificationTargets, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.notificationTargets(ctx, agentID)
}

func (s *Service) notificationTargets(ctx context.Context, agentID string) (NotificationTargets, error) {
	bot, err := s.q.GetAgent(ctx, agentID)
	if err != nil {
		return NotificationTargets{}, err
	}
	stored, err := s.q.ListRoutineNotificationTargets(ctx, agentID)
	if err != nil {
		return NotificationTargets{}, err
	}
	out := NotificationTargets{Targets: []NotificationTarget{}}
	add := func(platform, account, peer string) error {
		if account == "" || peer == "" {
			return nil
		}
		for _, target := range stored {
			if target.Platform == platform && target.AccountID == account && target.Peer == peer {
				out.Targets = append(out.Targets, NotificationTarget{ID: target.ID, Platform: platform, Peer: peer})
				if bot.PrimaryNotificationTargetID.Valid && bot.PrimaryNotificationTargetID.String == target.ID {
					out.PrimaryID = target.ID
				}
				return nil
			}
		}
		id := uuid.NewV7().String()
		if err := s.q.InsertRoutineNotificationTarget(ctx, sqlc.InsertRoutineNotificationTargetParams{ID: id, AgentID: agentID, Platform: platform, AccountID: account, Peer: peer}); err != nil {
			return err
		}
		out.Targets = append(out.Targets, NotificationTarget{ID: id, Platform: platform, Peer: peer})
		return nil
	}
	tg, err := s.q.GetTelegramSettings(ctx, agentID)
	if err == nil {
		account, _, ok := strings.Cut(tg.Token, ":")
		if _, e := strconv.ParseUint(account, 10, 64); !ok || e != nil {
			return NotificationTargets{}, fmt.Errorf("%w: invalid Telegram bot identity", ErrInvalid)
		}
		users, e := parseUsers(tg.AllowedUsers)
		if e != nil {
			return NotificationTargets{}, e
		}
		for _, user := range users {
			if err = add("telegram", account, "telegram:"+strconv.FormatInt(user, 10)); err != nil {
				return NotificationTargets{}, err
			}
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return NotificationTargets{}, err
	}
	fs, err := s.q.GetFeishuSettings(ctx, agentID)
	if err == nil {
		if fs.OwnerID != "" {
			if err = add("feishu", fs.AppID, "feishu:"+fs.OwnerID); err != nil {
				return NotificationTargets{}, err
			}
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return NotificationTargets{}, err
	}
	wx, err := s.q.GetWechatSettings(ctx, agentID)
	if err == nil {
		if err = add("wechat", wx.BotID, "wechat:"+wx.Owner); err != nil {
			return NotificationTargets{}, err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return NotificationTargets{}, err
	}
	return out, nil
}

// SetPrimaryNotificationTarget chooses an authorized target or clears the default.
func (s *Service) SetPrimaryNotificationTarget(ctx context.Context, agentID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.notificationTargets(ctx, agentID)
	if err != nil {
		return err
	}
	if id != "" {
		found := false
		for _, target := range list.Targets {
			if target.ID == id {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: notification target unavailable", ErrInvalid)
		}
	}
	_, err = s.q.SetPrimaryNotificationTarget(ctx, sqlc.SetPrimaryNotificationTargetParams{ID: agentID, PrimaryNotificationTargetID: sql.NullString{String: id, Valid: id != ""}})
	return err
}
