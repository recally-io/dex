// Package artifact stores delivered snapshots and their identities on disk.
package artifact

import (
	"cmp"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"uuid"

	"github.com/recally-io/dex/internal/engine"
)

const MaxSize = 20 << 20

var (
	ErrInvalidName = errors.New("invalid artifact name")
	ErrConflict    = errors.New("artifact directory contains conflicting files")
)

// Info is derived from the directory route, stable ID, and the current file stat.
// An empty directory keeps only a delivery receipt. It is not a visible file.
type Info struct {
	engine.ArtifactInfo
	ModifiedAt string
	directory  string
}

func operationHash(agentID, key string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(agentID+"\x00"+key)))
}

func route(source, display int64) string { return fmt.Sprintf("%d_%d", source, display) }

func checkName(name string) error {
	if strings.TrimSpace(name) == "" || name == "." || name == ".." || len(name) > 255 || strings.ContainsAny(name, "/\\") || strings.ContainsFunc(name, unicode.IsControl) {
		return fmt.Errorf("%w: use a file name of 1 to 255 bytes without paths or control characters", ErrInvalidName)
	}
	return nil
}

// openDirectory refuses symlinks, including links to another directory inside the root.
func openDirectory(parent *os.Root, name string) (*os.Root, error) {
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() {
		return nil, fmt.Errorf("%s: directory required", name)
	}
	dir, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	after, err := dir.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		_ = dir.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("artifact directory changed while opening")
	}
	return dir, nil
}

func diskRoot(dataDir string) (*os.Root, error) {
	data, err := os.OpenRoot(dataDir)
	if err != nil {
		return nil, err
	}
	defer data.Close()
	if err := data.Mkdir("artifacts", 0700); err != nil {
		if !os.IsExist(err) {
			return nil, err
		}
	} else if err := syncDirectory(data); err != nil {
		return nil, err
	}
	return openDirectory(data, "artifacts")
}

func regularFile(root *os.Root, name string) (*os.File, error) {
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

func snapshotDirectory(root *os.Root, directory string) (*os.Root, error) {
	parent, name, ok := strings.Cut(directory, "/")
	if !ok {
		return nil, fs.ErrNotExist
	}
	dir, err := openDirectory(root, parent)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return openDirectory(dir, name)
}

// scan includes empty receipt directories. Callers hide them from public file lists.
func scan(root *os.Root, chatID int64, operation string) ([]Info, error) {
	routes, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, err
	}
	var result []Info
	for _, entry := range routes {
		if !entry.IsDir() {
			continue
		}
		sourceText, displayText, ok := strings.Cut(entry.Name(), "_")
		source, e1 := strconv.ParseInt(sourceText, 10, 64)
		display, e2 := strconv.ParseInt(displayText, 10, 64)
		if !ok || e1 != nil || e2 != nil || source <= 0 || display <= 0 || route(source, display) != entry.Name() {
			continue
		}
		if chatID > 0 && source != chatID && display != chatID {
			continue
		}
		dir, err := openDirectory(root, entry.Name())
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		items, err := scanRoute(dir, entry.Name(), source, display, operation)
		_ = dir.Close()
		if err != nil {
			return nil, err
		}
		result = append(result, items...)
	}
	slices.SortFunc(result, func(a, b Info) int { return cmp.Compare(a.ID, b.ID) })
	return result, nil
}

func scanRoute(root *os.Root, parent string, source, display int64, operation string) ([]Info, error) {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, err
	}
	var result []Info
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id, hash, hasHash := strings.Cut(entry.Name(), "_")
		parsed, err := uuid.Parse(id)
		if err != nil || parsed.String() != id {
			continue
		}
		if hasHash && (len(hash) != 64 || strings.ContainsFunc(hash, func(r rune) bool { return (r < '0' || r > '9') && (r < 'a' || r > 'f') })) {
			continue
		}
		if operation != "" && hash != operation {
			continue
		}
		dir, err := openDirectory(root, entry.Name())
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		info, err := readSnapshot(dir)
		_ = dir.Close()
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("snapshot %s: %w", id, err)
		}
		info.ID = id
		info.ConversationID = source
		info.DisplayConversationID = display
		info.directory = parent + "/" + entry.Name()
		result = append(result, info)
	}
	return result, nil
}

func readSnapshot(root *os.Root) (Info, error) {
	files, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return Info{}, err
	}
	var out Info
	for _, entry := range files {
		// A snapshot holds one regular file. Never open links, devices, or subdirectories.
		if !entry.Type().IsRegular() {
			continue
		}
		f, err := regularFile(root, entry.Name())
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return Info{}, err
		}
		stat, err := f.Stat()
		_ = f.Close()
		if err != nil {
			return Info{}, err
		}
		if out.Name != "" {
			return Info{}, ErrConflict
		}
		out.Name = entry.Name()
		out.Size = stat.Size()
		out.ModifiedAt = stat.ModTime().UTC().Format(time.RFC3339Nano)
	}
	return out, nil
}

func find(root *os.Root, chatID int64, id, operation string) (Info, error) {
	if id != "" {
		parsed, err := uuid.Parse(id)
		if err != nil || parsed.String() != id {
			return Info{}, fs.ErrNotExist
		}
	}
	all, err := scan(root, chatID, operation)
	if err != nil {
		return Info{}, err
	}
	var found *Info
	for _, info := range all {
		if id != "" && info.ID != id {
			continue
		}
		if found != nil {
			return Info{}, ErrConflict
		}
		found = new(info)
	}
	if found == nil {
		return Info{}, fs.ErrNotExist
	}
	return *found, nil
}

// stage copies a workspace file before its stable delivery directory is published.
func stage(root *os.Root, agentDir, name string) (string, int64, error) {
	if !filepath.IsLocal(name) || name == "." {
		return "", 0, errors.New("artifact path must be relative to the workspace")
	}
	if err := checkName(filepath.Base(name)); err != nil {
		return "", 0, err
	}
	agent, err := os.OpenRoot(agentDir)
	if err != nil {
		return "", 0, err
	}
	defer agent.Close()
	workspace, err := openDirectory(agent, "workspace")
	if err != nil {
		return "", 0, err
	}
	defer workspace.Close()
	// os.Root permits links only when they stay within this workspace.
	f, err := workspace.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	if !stat.Mode().IsRegular() {
		return "", 0, errors.New("artifact must be a regular file")
	}
	if stat.Size() > MaxSize {
		return "", 0, errors.New("artifact exceeds 20 MB")
	}
	return stageFile(root, f, filepath.Base(name), MaxSize)
}

func stageFile(root *os.Root, f *os.File, name string, limit int64) (string, int64, error) {
	pending := ".pending-" + uuid.NewV7().String()
	if err := root.Mkdir(pending, 0700); err != nil {
		return "", 0, err
	}
	dir, err := openDirectory(root, pending)
	if err != nil {
		_ = root.Remove(pending)
		return "", 0, err
	}
	n, err := copyFile(dir, f, name, limit)
	if err == nil {
		err = syncDirectory(dir)
	}
	_ = dir.Close()
	if err != nil {
		_ = root.RemoveAll(pending)
		return "", 0, err
	}
	return pending, n, nil
}

func copyFile(root *os.Root, source *os.File, name string, limit int64) (int64, error) {
	out, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return 0, err
	}
	var reader io.Reader = source
	if limit > 0 {
		reader = io.LimitReader(source, limit+1)
	}
	n, err := io.Copy(out, reader)
	err = errors.Join(err, out.Sync(), out.Close())
	if err != nil || limit > 0 && n > limit {
		_ = root.Remove(name)
		if err != nil {
			return 0, err
		}
		return 0, errors.New("artifact exceeds 20 MB")
	}
	return n, nil
}

func syncDirectory(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

func syncPublication(root *os.Root, parent string) error {
	dir, err := openDirectory(root, parent)
	if err != nil {
		return err
	}
	err = syncDirectory(dir)
	_ = dir.Close()
	if err != nil {
		return err
	}
	return syncDirectory(root)
}
