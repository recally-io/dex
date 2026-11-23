package app

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

const migratedSuffix = ".migrated"

func migrateAgentInstructions(ctx context.Context, conn *sql.DB, cfg Config) error {
	q := sqlc.New(conn)
	pending, err := q.GetAgentInstructionImport(ctx)
	var paths []string
	switch {
	case errors.Is(err, sql.ErrNoRows):
		paths, err = importAgentInstructions(ctx, conn, cfg)
	case err == nil:
		err = json.Unmarshal([]byte(pending), &paths)
	}
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}
	// The import committed before any rename. A restart only finishes the renames.
	for _, path := range paths {
		backup := path + migratedSuffix
		if _, err := os.Lstat(backup); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := os.Rename(path, backup); err != nil {
			return err
		}
	}
	return q.FinishAgentInstructionImport(ctx)
}

func importAgentInstructions(ctx context.Context, conn *sql.DB, cfg Config) ([]string, error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := sqlc.New(tx)
	ids, err := q.ListAgentIDsForInstructionImport(ctx)
	if err != nil {
		return nil, err
	}
	data, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	paths := []string{}
	contents := map[string]string{}
	read := func(root string) (path, text string, err error) {
		// Resolve the directory, but rename the file's link rather than its target.
		dir, err := filepath.EvalSymlinks(filepath.Join(root, ".agents"))
		if errors.Is(err, fs.ErrNotExist) {
			return "", "", nil
		}
		if err != nil {
			return "", "", err
		}
		path = filepath.Join(dir, "AGENTS.md")
		if text, ok := contents[path]; ok {
			return path, text, nil
		}
		text, found, err := engine.ReadLegacyAgentsMD(ctx, path, cfg.Home)
		if err != nil || !found {
			return "", "", err
		}
		if _, err := os.Lstat(path + migratedSuffix); err == nil {
			return "", "", fmt.Errorf("backup already exists: %s", path+migratedSuffix)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", "", err
		}
		paths = append(paths, path)
		contents[path] = text
		return path, text, nil
	}
	dexPath, shared, err := read(data)
	if err != nil {
		return nil, err
	}
	section := func(root, text string) string {
		return "## Imported from " + filepath.Join(root, ".agents", "AGENTS.md") + "\n\n" + text
	}
	var common string
	if shared != "" {
		common = section(data, shared)
	}
	for _, id := range ids {
		root := filepath.Join(data, "agents", id)
		path, own, err := read(root)
		if err != nil {
			return nil, err
		}
		text := common
		if own != "" && path != dexPath {
			if text != "" {
				text += "\n\n---\n\n"
			}
			text += section(root, own)
		}
		if text != "" {
			if err := q.AppendAgentInstructions(ctx, sqlc.AppendAgentInstructionsParams{ID: id, Text: text}); err != nil {
				return nil, err
			}
		}
	}
	pending, err := json.Marshal(paths)
	if err != nil {
		return nil, err
	}
	if err := q.RecordAgentInstructionImport(ctx, string(pending)); err != nil {
		return nil, err
	}
	return paths, tx.Commit()
}
