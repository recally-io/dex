// Package artifact manages regular files in each bot's artifacts directory.
package artifact

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/recally-io/dex/internal/agent"
	"github.com/recally-io/dex/internal/engine"
)

const MaxSize = 20 << 20

var (
	ErrInvalidName = errors.New("invalid artifact name")
	ErrConflict    = errors.New("artifact file name already exists")
)

type Info struct {
	Name       string
	Size       int64
	ModifiedAt string
}

// Service has no artifact index. The mutex only orders concurrent file mutations.
type Service struct {
	dataDir string
	mu      sync.Mutex
}

func New(dataDir string) *Service { return &Service{dataDir: dataDir} }

func checkName(name string) error {
	if strings.TrimSpace(name) == "" || name == "." || name == ".." || len(name) > 255 || strings.ContainsAny(name, "/\\") || strings.ContainsFunc(name, unicode.IsControl) {
		return fmt.Errorf("%w: use a file name of 1 to 255 bytes without paths or control characters", ErrInvalidName)
	}
	return nil
}

func openDirectory(parent *os.Root, name string) (*os.Root, error) {
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() {
		return nil, errors.New("artifact directory must not be a symlink or file")
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		_ = root.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("artifact directory changed while opening")
	}
	return root, nil
}

func (s *Service) root(agentID string, create bool) (*os.Root, error) {
	home, err := os.OpenRoot(agent.Home(s.dataDir, agentID))
	if err != nil {
		return nil, err
	}
	defer home.Close()
	if create {
		if err := home.Mkdir("artifacts", 0700); err != nil && !os.IsExist(err) {
			return nil, err
		}
	}
	return openDirectory(home, "artifacts")
}

func openFile(root *os.Root, name string) (*os.File, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fs.ErrNotExist
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		return nil, fs.ErrNotExist
	}
	return f, nil
}

func fileInfo(name string, stat os.FileInfo) Info {
	return Info{Name: name, Size: stat.Size(), ModifiedAt: stat.ModTime().UTC().Format(time.RFC3339Nano)}
}

func (s *Service) List(agentID string) ([]Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.root(agentID, false)
	if os.IsNotExist(err) {
		return []Info{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, err
	}
	out := make([]Info, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		f, err := openFile(root, entry.Name())
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		stat, err := f.Stat()
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, fileInfo(entry.Name(), stat))
	}
	return out, nil
}

func (s *Service) Open(agentID, name string) (Info, *os.File, error) {
	root, err := s.root(agentID, false)
	if err != nil {
		return Info{}, nil, err
	}
	defer root.Close()
	f, err := openFile(root, name)
	if err != nil {
		return Info{}, nil, err
	}
	stat, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return Info{}, nil, err
	}
	return fileInfo(name, stat), f, nil
}

func (s *Service) Rename(agentID, old, name string) (Info, error) {
	if err := checkName(name); err != nil {
		return Info{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.root(agentID, false)
	if err != nil {
		return Info{}, err
	}
	defer root.Close()
	source, err := openFile(root, old)
	if err != nil {
		return Info{}, err
	}
	defer source.Close()
	if old != name {
		if _, err := root.Lstat(name); err == nil {
			return Info{}, ErrConflict
		} else if !os.IsNotExist(err) {
			return Info{}, err
		}
		if err := root.Rename(old, name); err != nil {
			return Info{}, err
		}
	}
	stat, err := source.Stat()
	if err != nil {
		return Info{}, err
	}
	return fileInfo(name, stat), nil
}

func (s *Service) Delete(agentID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.root(agentID, false)
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := openFile(root, name)
	if err != nil {
		return err
	}
	_ = f.Close()
	return root.Remove(name)
}

// Deliver copies a workspace file under its actual name. Existing names are conflicts.
func (s *Service) Deliver(_ context.Context, caller engine.TaskContext, path string) (engine.ArtifactInfo, error) {
	if !filepath.IsLocal(path) || path == "." {
		return engine.ArtifactInfo{}, ErrInvalidName
	}
	name := filepath.Base(path)
	if err := checkName(name); err != nil {
		return engine.ArtifactInfo{}, err
	}
	home, err := os.OpenRoot(agent.Home(s.dataDir, caller.AgentID))
	if err != nil {
		return engine.ArtifactInfo{}, err
	}
	defer home.Close()
	workspace, err := openDirectory(home, "workspace")
	if err != nil {
		return engine.ArtifactInfo{}, err
	}
	defer workspace.Close()
	source, err := workspace.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return engine.ArtifactInfo{}, err
	}
	defer source.Close()
	stat, err := source.Stat()
	if err != nil {
		return engine.ArtifactInfo{}, err
	}
	if !stat.Mode().IsRegular() || stat.Size() > MaxSize {
		return engine.ArtifactInfo{}, errors.New("deliver a regular file of at most 20 MB")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.root(caller.AgentID, true)
	if err != nil {
		return engine.ArtifactInfo{}, err
	}
	defer root.Close()
	out, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return engine.ArtifactInfo{}, ErrConflict
	}
	if err != nil {
		return engine.ArtifactInfo{}, err
	}
	size, err := io.Copy(out, io.LimitReader(source, MaxSize+1))
	err = errors.Join(err, out.Close())
	if err != nil || size > MaxSize {
		_ = root.Remove(name)
		if err != nil {
			return engine.ArtifactInfo{}, err
		}
		return engine.ArtifactInfo{}, errors.New("artifact exceeds 20 MB")
	}
	return engine.ArtifactInfo{Name: name, Size: size, URL: "/api/agents/" + url.PathEscape(caller.AgentID) + "/artifacts/" + url.PathEscape(name)}, nil
}
