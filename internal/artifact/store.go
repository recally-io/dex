// Package artifact keeps snapshots of files explicitly delivered by an agent.
package artifact

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"uuid"
)

// MaxSize limits the space and memory needed to deliver one file.
const MaxSize = 20 << 20

type SnapshotInfo struct {
	ID   string
	Name string
	Size int64
}

// Snapshot copies a workspace-relative regular file into agent-owned storage.
// The caller must record the source conversation before exposing this ID.
func Snapshot(agentDir, name string) (SnapshotInfo, error) {
	if !filepath.IsLocal(name) || name == "." {
		return SnapshotInfo{}, errors.New("artifact path must be relative to the workspace")
	}
	agent, err := os.OpenRoot(agentDir)
	if err != nil {
		return SnapshotInfo{}, err
	}
	defer agent.Close()
	info, err := agent.Lstat("workspace")
	if err != nil {
		return SnapshotInfo{}, err
	}
	if !info.IsDir() {
		return SnapshotInfo{}, errors.New("workspace must be a directory")
	}
	workspace, err := agent.OpenRoot("workspace")
	if err != nil {
		return SnapshotInfo{}, err
	}
	defer workspace.Close()
	opened, err := workspace.Stat(".")
	if err != nil {
		return SnapshotInfo{}, err
	}
	if !os.SameFile(info, opened) {
		return SnapshotInfo{}, errors.New("workspace changed while opening")
	}
	// Nonblocking open prevents a replaced FIFO from hanging the service.
	f, err := workspace.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return SnapshotInfo{}, err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return SnapshotInfo{}, err
	}
	if !info.Mode().IsRegular() {
		return SnapshotInfo{}, errors.New("artifact must be a regular file")
	}
	if info.Size() > MaxSize {
		return SnapshotInfo{}, errors.New("artifact exceeds 20 MB")
	}
	if err := agent.MkdirAll("artifacts", 0700); err != nil {
		return SnapshotInfo{}, err
	}
	root, err := agent.OpenRoot("artifacts")
	if err != nil {
		return SnapshotInfo{}, err
	}
	defer root.Close()
	id := uuid.NewV7().String()
	out, err := root.OpenFile(id, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return SnapshotInfo{}, err
	}
	n, copyErr := io.Copy(out, io.LimitReader(f, MaxSize+1))
	copyErr = errors.Join(copyErr, out.Close())
	if copyErr != nil || n > MaxSize {
		_ = root.Remove(id)
		if copyErr != nil {
			return SnapshotInfo{}, copyErr
		}
		return SnapshotInfo{}, errors.New("artifact exceeds 20 MB")
	}
	return SnapshotInfo{ID: id, Name: filepath.Base(name), Size: n}, nil
}

// Open opens an opaque snapshot ID. The caller must check its owner first.
func Open(agentDir, id string) (*os.File, error) {
	root, err := artifactRoot(agentDir, id)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(id)
	if err != nil {
		return nil, fmt.Errorf("open artifact: %w", err)
	}
	return f, nil
}

// Remove discards a snapshot that did not get a durable database record.
func Remove(agentDir, id string) error {
	root, err := artifactRoot(agentDir, id)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.Remove(id)
}

func artifactRoot(agentDir, id string) (*os.Root, error) {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id {
		return nil, fs.ErrNotExist
	}
	agent, err := os.OpenRoot(agentDir)
	if err != nil {
		return nil, err
	}
	defer agent.Close()
	root, err := agent.OpenRoot("artifacts")
	if err != nil {
		return nil, err
	}
	return root, nil
}
