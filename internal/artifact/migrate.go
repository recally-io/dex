package artifact

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
)

const legacyMarker = ".legacy-imported"

// Migrate imports the old SQLite metadata once, before execution or HTTP starts.
// Existing identity directories always win over stale legacy names and sizes.
func (s *Service) Migrate(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := diskRoot(s.dataDir)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := discardPending(root); err != nil {
		return err
	}
	marker, err := regularFile(root, legacyMarker)
	if err == nil {
		content, readErr := io.ReadAll(io.LimitReader(marker, 16))
		_ = marker.Close()
		if readErr != nil {
			return readErr
		}
		if string(content) == "1\n" {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	rows, err := sqlc.New(s.db).ListLegacyArtifacts(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := s.importLegacy(root, row); err != nil {
			return fmt.Errorf("import artifact %s: %w", row.ID, err)
		}
	}
	// An interrupted marker write is safe: the next start repeats the idempotent import.
	if err := root.Remove(legacyMarker); err != nil && !os.IsNotExist(err) {
		return err
	}
	out, err := root.OpenFile(legacyMarker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := out.WriteString("1\n")
	if err := errors.Join(writeErr, out.Sync(), out.Close()); err != nil {
		return err
	}
	return syncDirectory(root)
}

func (s *Service) importLegacy(root *os.Root, row sqlc.Artifact) error {
	parsed, err := uuid.Parse(row.ID)
	if err != nil || parsed.String() != row.ID || row.ConversationID <= 0 || row.DisplayConversationID <= 0 {
		return errors.New("invalid legacy artifact identity")
	}
	parent := route(row.ConversationID, row.DisplayConversationID)
	directory := parent + "/" + row.ID + "_" + operationHash(row.AgentID, row.OperationKey)
	existing, err := snapshotDirectory(root, directory)
	published := err == nil
	if published {
		_ = existing.Close()
	} else if !os.IsNotExist(err) {
		return err
	}
	old, err := s.legacyRoot(row.AgentID)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var source *os.File
	if old != nil {
		defer old.Close()
		stat, err := old.Lstat(row.ID)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil {
			if !stat.Mode().IsRegular() {
				return errors.New("legacy snapshot is not a regular file")
			}
			source, err = regularFile(old, row.ID)
			if err != nil {
				return err
			}
			defer source.Close()
		}
	}
	if !published {
		if err := root.Mkdir(parent, 0700); err != nil && !os.IsExist(err) {
			return err
		}
		dir, err := openDirectory(root, parent)
		if err != nil {
			return err
		}
		_ = dir.Close()
		if source == nil {
			// A missing legacy file becomes an empty replay receipt, never a phantom file.
			if err := root.Mkdir(directory, 0700); err != nil {
				return err
			}
		} else {
			if row.Name == "" || row.Name == "." || row.Name == ".." || filepath.Base(row.Name) != row.Name {
				return errors.New("invalid legacy file name")
			}
			pending, _, err := stageFile(root, source, row.Name, 0)
			if err != nil {
				return err
			}
			defer func() { _ = root.RemoveAll(pending) }()
			if err := root.Rename(pending, directory); err != nil {
				return err
			}
		}
	}
	if err := syncPublication(root, parent); err != nil {
		return err
	}
	// Publication precedes removal. A crash can leave two copies, but only the new
	// directory is enumerable. A retry removes the old copy without importing twice.
	if source != nil {
		if err := old.Remove(row.ID); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := syncDirectory(old); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) legacyRoot(agentID string) (*os.Root, error) {
	// Agent IDs came from the legacy FK, but still must be one path component.
	if agentID == "" || agentID == "." || agentID == ".." || !fs.ValidPath(agentID) || strings.ContainsAny(agentID, "/\\") {
		return nil, errors.New("invalid legacy agent ID")
	}
	data, err := os.OpenRoot(s.dataDir)
	if err != nil {
		return nil, err
	}
	defer data.Close()
	agents, err := openDirectory(data, "agents")
	if err != nil {
		return nil, err
	}
	defer agents.Close()
	agent, err := openDirectory(agents, agentID)
	if err != nil {
		return nil, err
	}
	defer agent.Close()
	return openDirectory(agent, "artifacts")
}

// Only startup calls this, before a delivery can own a pending directory.
func discardPending(root *os.Root) error {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		suffix, ok := strings.CutPrefix(entry.Name(), ".pending-")
		id, err := uuid.Parse(suffix)
		if !ok || err != nil || id.String() != suffix || !entry.IsDir() {
			continue
		}
		if err := root.RemoveAll(entry.Name()); err != nil {
			return err
		}
	}
	return nil
}
