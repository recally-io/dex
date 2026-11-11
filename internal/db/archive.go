package db

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// Reasons why a bot cannot be archived. The web, the tool, and the owner approval share them, so
// the rules stay the same on every path.
var (
	ErrAgentMissing  = errors.New("bot not found")
	ErrAgentArchived = errors.New("the bot is archived already")
	ErrDefaultAgent  = errors.New("the default bot cannot be archived")
	ErrAgentBusy     = errors.New("the bot has a running session; stop its sessions first")
)

// CheckArchive reads whether a bot can be archived now. Call it in the transaction that archives,
// so the check and the write see one snapshot.
func CheckArchive(ctx context.Context, q *sqlc.Queries, id string) error {
	reason, err := q.AgentArchiveBlocker(ctx, id)
	if err != nil {
		return err
	}
	switch reason {
	case "":
		return nil
	case "missing":
		return ErrAgentMissing
	case "archived":
		return ErrAgentArchived
	case "default":
		return ErrDefaultAgent
	default:
		return ErrAgentBusy
	}
}

// ArchiveAgent archives a bot when the rules allow it, and says why when they do not.
func ArchiveAgent(ctx context.Context, q *sqlc.Queries, id string, now time.Time) error {
	rows, err := q.ArchiveAgent(ctx, sqlc.ArchiveAgentParams{AgentID: id, Now: sql.NullInt64{Int64: now.UnixMilli(), Valid: true}})
	if err != nil || rows > 0 {
		return err
	}
	// The statement refused. Name the reason; a bot that became busy since a check has none left.
	if err := CheckArchive(ctx, q, id); err != nil {
		return err
	}
	return ErrAgentBusy
}
