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

func (*Policy) AdmitInput(peer string) error {
	if session.NonPrivateTelegramPeer(peer) {
		return session.ErrTelegramUnauthorized
	}
	return nil
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
	return p.sessions.TelegramInputAllowed(ctx, p.botID, peer, source)
}
