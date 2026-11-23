package app

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

const migratedSuffix = ".migrated"

func migrateAgentInstructions(ctx context.Context, conn *sql.DB, cfg Config) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := sqlc.New(tx)
	ids, err := q.ListAgentIDsForInstructionImport(ctx)
	if err != nil {
		return err
	}
	data, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return err
	}
	contents := map[string]string{}
	read := func(root string) (path, text string) {
		source := filepath.Join(root, ".agents", "AGENTS.md")
		if _, err := os.Lstat(source); errors.Is(err, fs.ErrNotExist) {
			return "", ""
		} else if err != nil {
			slog.Warn("skip legacy bot instructions", "path", source, "err", err)
			return "", ""
		}
		// Resolve the directory, but rename the file's link rather than its target.
		dir, err := filepath.EvalSymlinks(filepath.Dir(source))
		if err != nil {
			slog.Warn("skip legacy bot instructions", "path", source, "err", err)
			return "", ""
		}
		path = filepath.Join(dir, "AGENTS.md")
		if text, ok := contents[path]; ok {
			return path, text
		}
		backup := path + migratedSuffix
		if _, err := os.Lstat(backup); err == nil {
			slog.Warn("skip legacy bot instructions: backup already exists", "path", path, "backup", backup)
			return "", ""
		} else if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("skip legacy bot instructions", "path", path, "err", err)
			return "", ""
		}
		text, found, err := engine.ReadLegacyAgentsMD(ctx, path, cfg.Home)
		if err != nil {
			slog.Warn("skip legacy bot instructions", "path", path, "err", err)
			return "", ""
		}
		if !found {
			return "", ""
		}
		contents[path] = text
		return path, text
	}
	dexPath, shared := read(data)
	section := func(root, text string) string {
		return "## Imported from " + filepath.Join(root, ".agents", "AGENTS.md") + "\n\n" + text
	}
	var common string
	if shared != "" {
		common = section(data, shared)
	}
	for _, id := range ids {
		root := filepath.Join(data, "agents", id)
		path, own := read(root)
		text := common
		if own != "" && path != dexPath {
			if text != "" {
				text += "\n\n---\n\n"
			}
			text += section(root, own)
		}
		if text != "" {
			if err := q.AppendAgentInstructions(ctx, sqlc.AppendAgentInstructionsParams{ID: id, Text: text}); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// A failed rename can cause the text to be imported again on the next start.
	for path := range contents {
		if err := os.Rename(path, path+migratedSuffix); err != nil {
			slog.Warn("rename imported bot instructions", "path", path, "err", err)
		}
	}
	return nil
}
