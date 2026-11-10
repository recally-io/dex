package channel

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"strconv"
	"strings"

	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db/sqlc"
)

// authorizedNotification rechecks the current account and recipient, without exposing credentials.
func (m *Manager) authorizedNotification(ctx context.Context, d sqlc.RoutineNotificationDelivery) (bool, error) {
	t, err := m.q.GetRoutineNotificationTarget(ctx, sqlc.GetRoutineNotificationTargetParams{ID: d.TargetID, AgentID: m.agentID})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if t.Platform != d.Platform || t.AccountID != d.AccountID || t.Peer != d.Peer {
		return false, nil
	}
	switch d.Platform {
	case "telegram":
		cfg, err := m.q.GetTelegramSettings(ctx, m.agentID)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		account, _, ok := strings.Cut(cfg.Token, ":")
		if !ok || account != d.AccountID {
			return false, nil
		}
		user, ok := strings.CutPrefix(d.Peer, "telegram:")
		if !ok {
			return false, nil
		}
		id, err := strconv.ParseInt(user, 10, 64)
		if err != nil || id <= 0 {
			return false, nil
		}
		for item := range strings.SplitSeq(cfg.AllowedUsers, ",") {
			if strings.TrimSpace(item) == user {
				return true, nil
			}
		}
		return false, nil
	case "feishu":
		cfg, err := m.q.GetFeishuSettings(ctx, m.agentID)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return cfg.AppID == d.AccountID && cfg.OwnerID != "" && d.Peer == "feishu:"+cfg.OwnerID, nil
	case "wechat":
		cfg, err := m.q.GetWechatSettings(ctx, m.agentID)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return cfg.BotID == d.AccountID && cfg.Owner != "" && d.Peer == "wechat:"+cfg.Owner, nil
	default:
		return false, nil
	}
}

func (m *Manager) flushNotifications(ctx context.Context, platform string, bot Bot) error {
	rows, err := m.q.ListRoutineNotifications(ctx, sqlc.ListRoutineNotificationsParams{AgentID: m.agentID, Platform: platform})
	if err != nil {
		return err
	}
	for _, d := range rows {
		parts := []string{}
		if d.RenderedParts == "" {
			parts = bot.Parts(chat.Reply{Status: "done", Answer: d.Body})
			data, err := json.Marshal(parts)
			if err != nil {
				return err
			}
			if err = m.q.SetRoutineNotificationParts(ctx, sqlc.SetRoutineNotificationPartsParams{RunID: d.RunID, RenderedParts: string(data)}); err != nil {
				return err
			}
		} else if err := json.Unmarshal([]byte(d.RenderedParts), &parts); err != nil {
			return err
		}
		for i := int(d.Parts); i < len(parts); i++ {
			current, err := m.q.GetRoutineNotification(ctx, d.RunID)
			if err != nil {
				return err
			}
			if current.Status != "pending" {
				break
			}
			allowed, err := m.authorizedNotification(ctx, current)
			if err != nil {
				return err
			}
			if !allowed {
				if err := m.q.SetRoutineNotificationStatus(ctx, sqlc.SetRoutineNotificationStatusParams{RunID: d.RunID, Status: "rejected", Reason: "destination no longer authorized"}); err != nil {
					return err
				}
				break
			}
			_, err = bot.Send(ctx, d.Peer, "routine:"+d.RunID, i, parts[i])
			if errors.Is(err, ErrPermanent) {
				if err := m.q.SetRoutineNotificationStatus(ctx, sqlc.SetRoutineNotificationStatusParams{RunID: d.RunID, Status: "rejected", Reason: "platform rejected delivery"}); err != nil {
					return err
				}
				break
			}
			if err != nil {
				return err
			}
			if err = m.q.AdvanceRoutineNotification(ctx, d.RunID); err != nil {
				return err
			}
		}
		if err := m.q.SetRoutineNotificationStatus(ctx, sqlc.SetRoutineNotificationStatusParams{RunID: d.RunID, Status: "delivered"}); err != nil {
			return err
		}
	}
	return nil
}
