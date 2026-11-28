package pairing

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
)

var ErrNotificationTargetUnavailable = errors.New("invalid settings: notification target unavailable")

// NotificationTarget describes a channel recipient authorized by the owner.
type NotificationTarget struct {
	ID       string `json:"id"`
	Platform string `json:"platform"`
	Peer     string `json:"peer"`
}

// NotificationTargets contains only this bot's currently authorized targets.
type NotificationTargets struct {
	Targets   []NotificationTarget `json:"targets"`
	PrimaryID string               `json:"primaryId"`
}

// NotificationTargets resolves destinations from configured channel owners, never arbitrary peers.
func (s *Service) NotificationTargets(ctx context.Context, botID string) (NotificationTargets, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.notificationTargetsLocked(ctx, botID)
}

func (s *Service) notificationTargetsLocked(ctx context.Context, botID string) (NotificationTargets, error) {
	bot, err := s.q.GetBot(ctx, botID)
	if err != nil {
		return NotificationTargets{}, err
	}
	stored, err := s.q.ListAutomationNotificationTargets(ctx, botID)
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
		if err := s.q.InsertAutomationNotificationTarget(ctx, sqlc.InsertAutomationNotificationTargetParams{ID: id, BotID: botID, Platform: platform, AccountID: account, Peer: peer}); err != nil {
			return err
		}
		out.Targets = append(out.Targets, NotificationTarget{ID: id, Platform: platform, Peer: peer})
		return nil
	}
	account, peers, err := s.telegram.NotificationPeers(ctx, botID)
	if err != nil {
		return NotificationTargets{}, err
	}
	for _, peer := range peers {
		if err := add("telegram", account, peer); err != nil {
			return NotificationTargets{}, err
		}
	}
	for _, setup := range []struct {
		platform string
		owner    func(context.Context, string) (string, string, error)
	}{
		{"feishu", s.feishu.Owner},
		{"wechat", s.wechat.Owner},
	} {
		account, owner, err := setup.owner(ctx, botID)
		if err != nil {
			return NotificationTargets{}, err
		}
		if owner != "" {
			if err := add(setup.platform, account, setup.platform+":"+owner); err != nil {
				return NotificationTargets{}, err
			}
		}
	}
	return out, nil
}

// SetPrimaryNotificationTarget chooses an authorized target or clears the default.
func (s *Service) SetPrimaryNotificationTarget(ctx context.Context, botID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setPrimaryNotificationTargetLocked(ctx, botID, id, "")
}

// An approval must match both the current target ID and the peer on the review page.
func (s *Service) setPrimaryNotificationTargetLocked(ctx context.Context, botID, id, expectedPeer string) error {
	list, err := s.notificationTargetsLocked(ctx, botID)
	if err != nil {
		return err
	}
	if id != "" && !slices.ContainsFunc(list.Targets, func(target NotificationTarget) bool {
		return target.ID == id && (expectedPeer == "" || target.Peer == expectedPeer)
	}) {
		return ErrNotificationTargetUnavailable
	}
	_, err = s.q.SetPrimaryNotificationTarget(ctx, sqlc.SetPrimaryNotificationTargetParams{ID: botID, PrimaryNotificationTargetID: sql.NullString{String: id, Valid: id != ""}})
	return err
}
