package policy

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/session"
)

const callbackSourcePrefix = "telegram-callback:"

var ErrTelegramUnauthorized = errors.New("telegram input is not authorized")

// telegramCallbackObject checks the explicit object in the fixed Telegram command set.
func telegramCallbackObject(row sqlc.TelegramCallback) bool {
	command, rest, _ := strings.Cut(row.Action, " ")
	if command == "/run" || command == "/answer" {
		object, _, _ := strings.Cut(rest, " ")
		return object != "" && object == row.ObjectID
	}
	switch command {
	case "/menu", "/help", "/status", "/settings", "/sessions", "/workflows", "/question", "/stop", "/clear", "/new":
		return row.ObjectID == strconv.FormatInt(row.SessionID, 10)
	}
	return false
}

// CheckCommandIn rechecks a claimed callback under the effect's existing SQLite writer.
func CheckCommandIn(ctx context.Context, q *sqlc.Queries, source, botID string, sessionID int64) error {
	return validateTelegramControl(ctx, q, source, botID, sessionID)
}

// validateTelegramControl is the read-only validity check of a claimed callback command. Other
// sources pass. The command must keep its button's peer, conversation, generation, object,
// expiry, actor, and active binding. It returns ErrTelegramUnauthorized for a stale command.
// Effects must call it again under their writer through CheckCommandIn.
func validateTelegramControl(ctx context.Context, q *sqlc.Queries, source, botID string, sessionID int64) error {
	id, callback := strings.CutPrefix(source, callbackSourcePrefix)
	if !callback {
		return nil
	}
	row, err := q.GetTelegramCallback(ctx, id)
	if err != nil {
		return err
	}
	command, err := q.GetChannelCommand(ctx, source)
	if err != nil {
		return err
	}
	conv, err := q.GetSession(ctx, row.SessionID)
	if err != nil {
		return err
	}
	revision, err := q.TelegramContextRevision(ctx, conv.ID)
	if err != nil {
		return err
	}
	if !telegramCallbackObject(row) || row.BotID != botID || row.SessionID != sessionID || !row.ConsumedAt.Valid || row.ExpiresAt <= time.Now().UnixMilli() || command.BotID != row.BotID || command.Peer != row.Peer || command.Command != row.Action || command.ChannelKey != conv.ChannelKey || conv.BotID != row.BotID || conv.ExecutionID.Int64 != row.ExecutionID || revision != row.ContextRevision {
		return ErrTelegramUnauthorized
	}
	var actor, original session.ChannelContext
	if err := json.Unmarshal([]byte(command.ChannelContext), &actor); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(row.ChannelContext), &original); err != nil {
		return err
	}
	if !PrivatePeer(row.Peer) || row.MessageID == "" || actor.AccountID != original.AccountID || actor.SenderID != original.SenderID || actor.MessageID != row.MessageID {
		return ErrTelegramUnauthorized
	}
	return authorizeTelegramActor(ctx, q, botID, actor.AccountID, actor.SenderID)
}

// PrivatePeer reports whether peer is a canonical private Telegram route.
func PrivatePeer(peer string) bool {
	id, telegram := strings.CutPrefix(peer, peerPrefix)
	return telegram && positiveTelegramID(id)
}

func positiveTelegramID(value string) bool {
	id, err := strconv.ParseInt(value, 10, 64)
	return err == nil && id > 0 && strconv.FormatInt(id, 10) == value
}

func authorizeTelegramActor(ctx context.Context, q *sqlc.Queries, botID, accountID, senderID string) error {
	if !positiveTelegramID(accountID) || !positiveTelegramID(senderID) {
		return ErrTelegramUnauthorized
	}
	if err := db.RequireActive(ctx, q, botID); err != nil {
		if errors.Is(err, db.ErrBotArchived) {
			return errors.Join(ErrTelegramUnauthorized, err)
		}
		return err
	}
	cfg, err := q.GetTelegramSettings(ctx, botID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrTelegramUnauthorized
	}
	if err != nil {
		return err
	}
	account, _, ok := strings.Cut(cfg.Token, ":")
	if !ok || account != accountID {
		return ErrTelegramUnauthorized
	}
	for allowed := range strings.SplitSeq(cfg.AllowedUsers, ",") {
		if strings.TrimSpace(allowed) == senderID {
			return nil
		}
	}
	return ErrTelegramUnauthorized
}

// CheckInputActorIn rechecks the actor before a Session stores a Telegram input.
func CheckInputActorIn(ctx context.Context, q *sqlc.Queries, botID, peer string, source session.ChannelContext) error {
	if !PrivatePeer(peer) {
		return ErrTelegramUnauthorized
	}
	return authorizeTelegramActor(ctx, q, botID, source.AccountID, source.SenderID)
}

// telegramInputAllowed checks current pairing before a transport downloads bytes or claims a button.
func telegramInputAllowed(ctx context.Context, q *sqlc.Queries, botID, peer string, source session.ChannelContext) (bool, error) {
	err := CheckInputActorIn(ctx, q, botID, peer, source)
	if errors.Is(err, ErrTelegramUnauthorized) {
		return false, nil
	}
	return err == nil, err
}
