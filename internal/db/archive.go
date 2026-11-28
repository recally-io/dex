package db

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// Reasons why a bot cannot be archived. The web, the tool, and the owner approval share them, so
// the rules stay the same on every path.
var (
	ErrBotMissing  = errors.New("bot not found")
	ErrBotArchived = errors.New("the bot is archived already")
	ErrDefaultBot  = errors.New("the default bot cannot be archived")
	ErrBotBusy     = errors.New("the bot has a running session; stop its sessions first")
)

// CheckArchive reads whether a bot can be archived now. Call it in the transaction that archives,
// so the check and the write see one snapshot.
func CheckArchive(ctx context.Context, q *sqlc.Queries, id string) error {
	reason, err := q.BotArchiveBlocker(ctx, id)
	if err != nil {
		return err
	}
	switch reason {
	case "":
		return nil
	case "missing":
		return ErrBotMissing
	case "archived":
		return ErrBotArchived
	case "default":
		return ErrDefaultBot
	default:
		return ErrBotBusy
	}
}

// ArchiveBot archives a bot when the rules allow it, and says why when they do not.
func ArchiveBot(ctx context.Context, q *sqlc.Queries, id string, now time.Time) error {
	rows, err := q.ArchiveBot(ctx, sqlc.ArchiveBotParams{BotID: id, Now: sql.NullInt64{Int64: now.UnixMilli(), Valid: true}})
	if err != nil || rows > 0 {
		return err
	}
	// The statement refused. Name the reason; a bot that became busy since a check has none left.
	if err := CheckArchive(ctx, q, id); err != nil {
		return err
	}
	return ErrBotBusy
}

// RequireActive fails with ErrBotArchived when the bot is archived. An input writer calls it
// after it took the SQLite writer: input first makes ArchiveBot refuse the bot as busy, and
// archive first makes the input fail, so no runnable input exists for an archived bot.
func RequireActive(ctx context.Context, q *sqlc.Queries, id string) error {
	archived, err := q.BotArchived(ctx, id)
	if err != nil {
		return err
	}
	if archived != 0 {
		return ErrBotArchived
	}
	return nil
}

// WriteActiveChannel orders a channel write with archive. Take the SQLite writer
// before checking the bot so the check and write see the same state.
func WriteActiveChannel(ctx context.Context, conn *sql.DB, botID string, write func(*sqlc.Queries) error) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := sqlc.New(tx)
	if err := q.LockChannelWriter(ctx); err != nil {
		return err
	}
	if err := RequireActive(ctx, q, botID); err != nil {
		return err
	}
	if err := write(q); err != nil {
		return err
	}
	return tx.Commit()
}

// CanStartChannel also guards recovery after a failed write. A canceled request
// must still let an active bot restart with its previous credentials.
func CanStartChannel(ctx context.Context, q *sqlc.Queries, botID string) bool {
	err := RequireActive(context.WithoutCancel(ctx), q, botID)
	if err != nil && !errors.Is(err, ErrBotArchived) {
		slog.WarnContext(ctx, "check bot before starting channel", "agent", botID, "err", err)
	}
	return err == nil
}
