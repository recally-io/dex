package artifact

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/agent"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

// Service uses business storage only to authorize a new delivery's active input
// and resolve its source/parent. All artifact state and replay identities live on disk.
type Service struct {
	db        *sql.DB
	dataDir   string
	mu        sync.Mutex
	OnChanged func(int64)
}

func New(db *sql.DB, dataDir string) *Service { return &Service{db: db, dataDir: dataDir} }

func (s *Service) Deliver(ctx context.Context, caller engine.TaskContext, path string) (engine.ArtifactInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := diskRoot(s.dataDir)
	if err != nil {
		return engine.ArtifactInfo{}, err
	}
	defer root.Close()
	hash := operationHash(caller.AgentID, caller.Key)
	prior, err := find(root, 0, "", hash)
	if err == nil {
		return prior.ArtifactInfo, nil
	}
	if !os.IsNotExist(err) {
		return engine.ArtifactInfo{}, err
	}
	q := sqlc.New(s.db)
	conv, err := q.GetConversationByExecution(ctx, sqlc.GetConversationByExecutionParams{AgentID: caller.AgentID, ExecutionID: sql.NullInt64{Int64: caller.ConversationID, Valid: true}})
	if err != nil {
		return engine.ArtifactInfo{}, err
	}
	parent := conv.ID
	switch {
	case conv.ChannelKey == "main", strings.HasPrefix(conv.ChannelKey, "chat:"), strings.HasPrefix(conv.ChannelKey, "automation:"), strings.HasPrefix(conv.ChannelKey, "task:"):
	default:
		return engine.ArtifactInfo{}, errors.New("this conversation cannot deliver files")
	}
	if conv.ParentID.Valid {
		source, err := q.GetSubmission(ctx, caller.SourceRequestID)
		if err != nil {
			return engine.ArtifactInfo{}, err
		}
		if source.ParentRequestID.Valid {
			parent = conv.ParentID.Int64
		}
	}
	if _, err := q.GetActiveArtifactInput(ctx, sqlc.GetActiveArtifactInputParams{ID: caller.SourceRequestID, ConversationID: conv.ID}); err != nil {
		return engine.ArtifactInfo{}, errors.Join(errors.New("delivery requires an active input"), err)
	}
	pending, size, err := stage(root, agent.Home(s.dataDir, caller.AgentID), path)
	if err != nil {
		return engine.ArtifactInfo{}, err
	}
	defer func() { _ = root.RemoveAll(pending) }()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return engine.ArtifactInfo{}, err
	}
	defer func() { _ = tx.Rollback() }()
	// Stop and settlement take this same SQLite writer. A closed input cannot publish a new file.
	locked, err := sqlc.New(tx).LockArtifactInput(ctx, sqlc.LockArtifactInputParams{ID: caller.SourceRequestID, ConversationID: conv.ID})
	if err != nil {
		return engine.ArtifactInfo{}, err
	}
	if locked != 1 {
		return engine.ArtifactInfo{}, errors.New("delivery requires an active input")
	}
	parentDir := route(conv.ID, parent)
	if err := root.Mkdir(parentDir, 0700); err != nil && !os.IsExist(err) {
		return engine.ArtifactInfo{}, err
	}
	dir, err := openDirectory(root, parentDir)
	if err != nil {
		return engine.ArtifactInfo{}, err
	}
	_ = dir.Close()
	id := uuid.NewV7().String()
	if err := root.Rename(pending, parentDir+"/"+id+"_"+hash); err != nil {
		return engine.ArtifactInfo{}, err
	}
	if err := syncPublication(root, parentDir); err != nil {
		return engine.ArtifactInfo{}, err
	}
	// The atomic directory publication is the receipt. A failed commit does not remove it;
	// a replay returns that identity instead of copying another snapshot.
	if err := tx.Commit(); err != nil {
		return engine.ArtifactInfo{}, err
	}
	s.changed(conv.ID, parent)
	return engine.ArtifactInfo{ID: id, Name: filepath.Base(path), Size: size, ConversationID: conv.ID, DisplayConversationID: parent}, nil
}

func (s *Service) List(_ context.Context, conversationID int64) ([]Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := diskRoot(s.dataDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	records, err := scan(root, conversationID, "")
	if err != nil {
		return nil, err
	}
	out := make([]Info, 0, len(records))
	for _, record := range records {
		if record.Name != "" {
			out = append(out, record)
		}
	}
	return out, nil
}

func (s *Service) Open(_ context.Context, conversationID int64, id string) (Info, *os.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := diskRoot(s.dataDir)
	if err != nil {
		return Info{}, nil, err
	}
	defer root.Close()
	item, err := find(root, conversationID, id, "")
	if err != nil {
		return Info{}, nil, err
	}
	if item.Name == "" {
		return Info{}, nil, fs.ErrNotExist
	}
	dir, err := snapshotDirectory(root, item.directory)
	if err != nil {
		return Info{}, nil, err
	}
	defer dir.Close()
	f, err := regularFile(dir, item.Name)
	if err != nil {
		return Info{}, nil, err
	}
	stat, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return Info{}, nil, err
	}
	item.Size = stat.Size()
	item.ModifiedAt = stat.ModTime().UTC().Format(time.RFC3339Nano)
	return item, f, nil
}

func (s *Service) Rename(_ context.Context, conversationID int64, id, name string) (Info, error) {
	if err := checkName(name); err != nil {
		return Info{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := diskRoot(s.dataDir)
	if err != nil {
		return Info{}, err
	}
	defer root.Close()
	item, err := find(root, conversationID, id, "")
	if err != nil {
		return Info{}, err
	}
	if item.Name == "" {
		return Info{}, fs.ErrNotExist
	}
	dir, err := snapshotDirectory(root, item.directory)
	if err != nil {
		return Info{}, err
	}
	defer dir.Close()
	if name != item.Name {
		if _, err := dir.Lstat(name); err == nil {
			return Info{}, ErrConflict
		} else if !os.IsNotExist(err) {
			return Info{}, err
		}
		if err := dir.Rename(item.Name, name); err != nil {
			return Info{}, err
		}
	}
	if err := syncDirectory(dir); err != nil {
		return Info{}, err
	}
	item.Name = name
	s.changed(item.ConversationID, item.DisplayConversationID)
	return item, nil
}

func (s *Service) Delete(_ context.Context, conversationID int64, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := diskRoot(s.dataDir)
	if err != nil {
		return err
	}
	defer root.Close()
	item, err := find(root, conversationID, id, "")
	if err != nil {
		return err
	}
	if item.Name != "" {
		dir, err := snapshotDirectory(root, item.directory)
		if err != nil {
			return err
		}
		err = dir.Remove(item.Name)
		if err == nil {
			err = syncDirectory(dir)
		}
		_ = dir.Close()
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	// Keep the empty identity directory. Replays cannot publish the same delivery again.
	s.changed(item.ConversationID, item.DisplayConversationID)
	return nil
}

func (s *Service) changed(source, display int64) {
	if s.OnChanged == nil {
		return
	}
	s.OnChanged(source)
	if source != display {
		s.OnChanged(display)
	}
}
