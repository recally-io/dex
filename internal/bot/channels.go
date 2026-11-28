package bot

import (
	"context"
	"crypto/sha256"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// telegramSetup is the Telegram capability that bot needs until channel coordination moves out.
type telegramSetup interface {
	State(ctx context.Context, botID string) (state string, paired bool, err error)
	NotificationPeers(ctx context.Context, botID string) (account string, peers []string, err error)
	FingerprintLocked(ctx context.Context, botID string) ([sha256.Size]byte, error)
	DisconnectLocked(ctx context.Context, botID string) error
	RevokePairingLocked(ctx context.Context, botID string) error
	PrepareStartLocked(ctx context.Context, botID string) (func(context.Context), error)
	CancelManagedSetupsInLocked(ctx context.Context, q *sqlc.Queries, botID string) error
	StopLocked(botID string)
}

// BindTelegram connects channel coordination before the service starts.
func (s *Service) BindTelegram(setup telegramSetup) { s.telegram = setup }

// feishuSetup is the Feishu capability that bot needs until channel coordination moves out.
type feishuSetup interface {
	State(ctx context.Context, botID string) (state string, paired bool, err error)
	Owner(ctx context.Context, botID string) (account, owner string, err error)
	FingerprintLocked(ctx context.Context, botID string) ([sha256.Size]byte, error)
	DisconnectLocked(ctx context.Context, botID string) error
	RevokePairingLocked(ctx context.Context, botID string) error
	PrepareStartLocked(ctx context.Context, botID string) (func(context.Context), error)
	CancelRegistrationLocked(botID string)
	StopLocked(botID string)
}

// BindFeishu connects channel coordination before the service starts.
func (s *Service) BindFeishu(setup feishuSetup) { s.feishu = setup }

// wechatSetup is the WeChat capability that bot needs until channel coordination moves out.
type wechatSetup interface {
	State(ctx context.Context, botID string) (state string, paired bool, err error)
	Owner(ctx context.Context, botID string) (account, owner string, err error)
	FingerprintLocked(ctx context.Context, botID string) ([sha256.Size]byte, error)
	DisconnectLocked(ctx context.Context, botID string) error
	PrepareStartLocked(ctx context.Context, botID string) (func(context.Context), error)
	CancelLoginLocked(botID string)
	StopLocked(botID string)
}

// BindWechat connects channel coordination before the service starts.
func (s *Service) BindWechat(setup wechatSetup) { s.wechat = setup }
