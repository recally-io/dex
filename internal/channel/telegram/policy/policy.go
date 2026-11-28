// Package policy implements Telegram's durable channel rules.
package policy

import (
	"context"
	"database/sql"
	"strings"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/session"
)

const peerPrefix = "telegram:"

// Policy owns the Telegram rules for one bot, even when its transport is off.
type Policy struct {
	db       *sql.DB
	q        *sqlc.Queries
	sessions *session.Service
	botID    string
}

func New(db *sql.DB, sessions *session.Service, botID string) *Policy {
	return &Policy{db: db, q: sqlc.New(db), sessions: sessions, botID: botID}
}

func (*Policy) AdmitInput(peer string) error { return CheckPeer(peer) }

// CheckPeer rejects non-private Telegram input before it can reach a Session.
func CheckPeer(peer string) error {
	if !PrivatePeer(peer) {
		return ErrTelegramUnauthorized
	}
	return nil
}

// RequirePairedChannel checks the private peer and the allowed sender.
func (p *Policy) RequirePairedChannel(ctx context.Context, q *sqlc.Queries, peer string, actor session.ChannelContext) error {
	allowed, err := telegramInputAllowed(ctx, q, p.botID, peer, actor)
	if err == nil && !allowed {
		return ErrTelegramUnauthorized
	}
	return err
}

// IsCommand reports whether Telegram text is a command rather than model input.
func (*Policy) IsCommand(text string) bool {
	switch text {
	case "/clear", "/new", "/stop", "/help", "/menu", "/status", "/settings", "/sessions", "/workflows", "/question":
		return true
	}
	return channel.IsFeedbackCommand(text) || strings.HasPrefix(text, "/run ") || strings.HasPrefix(text, "/answer ")
}

// SetTelegramRetryAt persists a FloodWait retry deadline for polling.
func (p *Policy) SetTelegramRetryAt(ctx context.Context, token string, until int64) error {
	return p.q.SetTelegramRetryAt(ctx, sqlc.SetTelegramRetryAtParams{BotID: p.botID, Token: token, RetryAt: until})
}

func (p *Policy) TelegramInputAllowed(ctx context.Context, peer string, source session.ChannelContext) (bool, error) {
	return telegramInputAllowed(ctx, p.q, p.botID, peer, source)
}
