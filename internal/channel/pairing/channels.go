// Package pairing coordinates channel settings changes across platforms.
package pairing

import (
	"context"
	"sync"

	"github.com/recally-io/dex/internal/channel/feishu"
	"github.com/recally-io/dex/internal/channel/telegram"
	"github.com/recally-io/dex/internal/channel/wechat"
	"github.com/recally-io/dex/internal/db/sqlc"
)

// Service owns cross-platform views, setting changes, and owner approvals.
type Service struct {
	q                *sqlc.Queries
	mu               *sync.Mutex
	telegram         *telegram.SetupService
	feishu           *feishu.SetupService
	wechat           *wechat.SetupService
	channelApprovals map[string]channelApproval // guarded by mu
}

// New connects the three setup services. All three must use mu for their settings lock.
func New(q *sqlc.Queries, mu *sync.Mutex, tg *telegram.SetupService, fs *feishu.SetupService, wx *wechat.SetupService) *Service {
	return &Service{q: q, mu: mu, telegram: tg, feishu: fs, wechat: wx, channelApprovals: make(map[string]channelApproval)}
}

// Channels holds the public settings and runtime state for one bot.
type Channels struct {
	Feishu   feishu.Feishu     `json:"feishu"`
	Telegram telegram.Telegram `json:"telegram"`
	Wechat   wechat.Wechat     `json:"wechat"`
}

func (s *Service) Channels(ctx context.Context, botID string) (Channels, error) {
	tg, err := s.telegram.View(ctx, botID)
	if err != nil {
		return Channels{}, err
	}
	wx, err := s.wechat.View(ctx, botID)
	if err != nil {
		return Channels{}, err
	}
	fs, err := s.feishu.View(ctx, botID)
	if err != nil {
		return Channels{}, err
	}
	return Channels{Telegram: tg, Wechat: wx, Feishu: fs}, nil
}
