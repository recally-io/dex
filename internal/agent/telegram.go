package agent

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/telegram"
)

// CreateTelegramPairing replaces the previous link. The code grants host access.
func (s *Service) CreateTelegramPairing(ctx context.Context, agentID string) (string, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := s.TelegramConfig(ctx, agentID)
	if err != nil {
		return "", 0, err
	}
	if cfg == nil {
		return "", 0, fmt.Errorf("%w: connect a Telegram bot first", ErrInvalid)
	}
	if len(cfg.AllowedUsers) != 0 {
		return "", 0, fmt.Errorf("%w: Telegram already has an owner", ErrInvalid)
	}
	username, err := telegram.Username(ctx, *cfg)
	if err != nil {
		return "", 0, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	code := make([]byte, 24)
	_, _ = rand.Read(code)
	value := hex.EncodeToString(code)
	expires := time.Now().Add(10 * time.Minute).Unix()
	if err = s.q.SetTelegramPairing(ctx, sqlc.SetTelegramPairingParams{AgentID: agentID, PairingCode: value, PairingExpires: expires}); err != nil {
		return "", 0, err
	}
	return "https://t.me/" + username + "?start=" + value, expires, nil
}

func (s *Service) pairTelegram(agentID, token string) func(context.Context, string, int64) ([]int64, error) {
	return func(ctx context.Context, code string, user int64) ([]int64, error) {
		if user <= 0 {
			return nil, nil
		}
		users, err := s.q.PairTelegramOwner(ctx, sqlc.PairTelegramOwnerParams{AgentID: agentID, Token: token, Code: code, UserID: strconv.FormatInt(user, 10), Now: time.Now().Unix()})
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return parseUsers(users)
	}
}
