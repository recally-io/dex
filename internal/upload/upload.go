// Package upload stores the files that the user attaches to messages. A file goes to
// <workspace>/uploads/<ID>/<name>, in the agent workspace, so the agent's tools can read it. The
// directory is the only record of an upload: an ID is known while its directory has its file.
package upload

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
	"unicode"
	"uuid"

	"github.com/recally-io/dex/internal/engine"
)

// MaxSize is the largest file, in bytes.
const MaxSize = 20 << 20

var (
	// ErrNotFound means that no upload has the ID.
	ErrNotFound = errors.New("upload not found")
	// ErrTooLarge means that the file is larger than MaxSize.
	ErrTooLarge = errors.New("the file is larger than 20 MB")
	// ErrInvalidName wraps the reason why a file name is refused.
	ErrInvalidName = errors.New("invalid file name")
)

// Store keeps the uploads of one agent.
type Store struct {
	agentDir string // <data>/agents/<agent>, the parent of the workspace
}

// New returns the Store of the agent with the directory agentDir.
func New(agentDir string) *Store {
	return &Store{agentDir: agentDir}
}

// uploadsDir is the uploads directory, relative to the agent directory.
const uploadsDir = "workspace/uploads"

// uploads opens the uploads directory, and makes it first when create is true. The agent's tools
// can write in the workspace, also a symlink in the place of the uploads directory. The root at
// the agent directory refuses each symlink that leads out of it.
func (s *Store) uploads(create bool) (*os.Root, error) {
	// The engine creates the agent directory on first use.
	if create {
		if err := os.MkdirAll(s.agentDir, 0o700); err != nil {
			return nil, err
		}
	}
	agent, err := os.OpenRoot(s.agentDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = agent.Close() }()
	if create {
		if err := agent.MkdirAll(uploadsDir, 0o700); err != nil {
			return nil, err
		}
	}
	return agent.OpenRoot(uploadsDir)
}

// Save stores the file name with the content of r as a new upload.
func (s *Store) Save(name string, r io.Reader) (engine.Attachment, error) {
	if err := checkName(name); err != nil {
		return engine.Attachment{}, err
	}
	root, err := s.uploads(true)
	if err != nil {
		return engine.Attachment{}, err
	}
	defer func() { _ = root.Close() }()
	id := uuid.NewV7().String()
	// The file is complete before its directory exists, so Get never sees a part of a file.
	tmp := ".upload-" + id
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return engine.Attachment{}, err
	}
	defer func() { _ = root.Remove(tmp) }()
	size, err := io.Copy(f, io.LimitReader(r, MaxSize+1))
	if err = errors.Join(err, f.Close()); err != nil {
		return engine.Attachment{}, err
	}
	if size > MaxSize {
		return engine.Attachment{}, ErrTooLarge
	}
	if err := root.Mkdir(id, 0o700); err != nil {
		return engine.Attachment{}, err
	}
	if err := root.Rename(tmp, path.Join(id, name)); err != nil {
		return engine.Attachment{}, err
	}
	return s.Get(id)
}

// checkName refuses a name that is not one plain file name. Browsers send the base name of the
// file, so a name with a path is not from the file picker.
func checkName(name string) error {
	switch {
	case name == "" || name == "." || name == "..":
		return fmt.Errorf("%w: the file has no name", ErrInvalidName)
	case len(name) > 255:
		return fmt.Errorf("%w: the name is longer than 255 bytes", ErrInvalidName)
	case strings.ContainsAny(name, `/\`):
		return fmt.Errorf("%w: the name contains a path", ErrInvalidName)
	case strings.ContainsFunc(name, unicode.IsControl):
		return fmt.Errorf("%w: the name contains a control character", ErrInvalidName)
	}
	return nil
}

// Remove deletes an upload that no admitted submission owns.
func (s *Store) Remove(id string) error {
	if u, err := uuid.Parse(id); err != nil || u.String() != id {
		return ErrNotFound
	}
	root, err := s.uploads(false)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return root.RemoveAll(id)
}

// Get returns the upload id, or ErrNotFound.
func (s *Store) Get(id string) (engine.Attachment, error) {
	f, a, err := s.Open(id)
	if err != nil {
		return engine.Attachment{}, err
	}
	return a, f.Close()
}

// Open returns the file of upload id and its description, or ErrNotFound. The caller closes the
// file.
func (s *Store) Open(id string) (*os.File, engine.Attachment, error) {
	// Only a UUID in its normal form names an upload, so an ID cannot leave the uploads directory.
	if u, err := uuid.Parse(id); err != nil || u.String() != id {
		return nil, engine.Attachment{}, ErrNotFound
	}
	root, err := s.uploads(false)
	if err != nil {
		// Also no uploads directory yet, or a symlink that leads out of the agent directory.
		return nil, engine.Attachment{}, fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	defer func() { _ = root.Close() }()
	entries, err := fs.ReadDir(root.FS(), id)
	if err != nil {
		// Also a symlink to a directory outside: the root refuses it.
		return nil, engine.Attachment{}, fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	if len(entries) != 1 || !entries[0].Type().IsRegular() {
		return nil, engine.Attachment{}, ErrNotFound
	}
	name := entries[0].Name()
	f, err := root.Open(path.Join(id, name))
	if err != nil {
		return nil, engine.Attachment{}, err
	}
	a, err := describe(f, id, name)
	if err != nil {
		return nil, engine.Attachment{}, errors.Join(err, f.Close())
	}
	return f, a, nil
}

// describe returns the attachment of f. The type comes from the content, as browsers detect it.
func describe(f *os.File, id, name string) (engine.Attachment, error) {
	info, err := f.Stat()
	if err != nil {
		return engine.Attachment{}, err
	}
	head := make([]byte, 512)
	n, err := f.ReadAt(head, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return engine.Attachment{}, err
	}
	return engine.Attachment{
		ID: id, Name: name, Size: info.Size(), MimeType: http.DetectContentType(head[:n]),
		Path: path.Join("uploads", id, name),
	}, nil
}
