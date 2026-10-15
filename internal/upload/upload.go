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
	"path/filepath"
	"strings"
	"unicode"
	"uuid"

	"github.com/vaayne/dex/internal/worker"
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

// Store keeps the uploads of one agent workspace.
type Store struct {
	dir string // <workspace>/uploads
}

// New returns the Store of the agent workspace workspace.
func New(workspace string) *Store {
	return &Store{dir: filepath.Join(workspace, "uploads")}
}

// Save stores the file name with the content of r as a new upload.
func (s *Store) Save(name string, r io.Reader) (worker.Attachment, error) {
	if err := checkName(name); err != nil {
		return worker.Attachment{}, err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return worker.Attachment{}, err
	}
	// The file is complete before its directory exists, so Get never sees a part of a file.
	tmp, err := os.CreateTemp(s.dir, ".upload-*")
	if err != nil {
		return worker.Attachment{}, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	size, err := io.Copy(tmp, io.LimitReader(r, MaxSize+1))
	if err = errors.Join(err, tmp.Close()); err != nil {
		return worker.Attachment{}, err
	}
	if size > MaxSize {
		return worker.Attachment{}, ErrTooLarge
	}
	id := uuid.NewV7().String()
	if err := os.Mkdir(filepath.Join(s.dir, id), 0o700); err != nil {
		return worker.Attachment{}, err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(s.dir, id, name)); err != nil {
		return worker.Attachment{}, err
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

// Get returns the upload id, or ErrNotFound.
func (s *Store) Get(id string) (worker.Attachment, error) {
	f, a, err := s.Open(id)
	if err != nil {
		return worker.Attachment{}, err
	}
	return a, f.Close()
}

// Open returns the file of upload id and its description, or ErrNotFound. The caller closes the
// file.
func (s *Store) Open(id string) (*os.File, worker.Attachment, error) {
	// Only a UUID in its normal form names an upload, so an ID cannot leave the uploads directory.
	if u, err := uuid.Parse(id); err != nil || u.String() != id {
		return nil, worker.Attachment{}, ErrNotFound
	}
	// The agent's tools can write in the workspace. The root keeps a symlink that they put in the
	// uploads directory from opening a file outside it.
	root, err := os.OpenRoot(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, worker.Attachment{}, ErrNotFound
	}
	if err != nil {
		return nil, worker.Attachment{}, err
	}
	defer func() { _ = root.Close() }()
	entries, err := fs.ReadDir(root.FS(), id)
	if err != nil {
		// Also a symlink to a directory outside: the root refuses it.
		return nil, worker.Attachment{}, fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	if len(entries) != 1 || !entries[0].Type().IsRegular() {
		return nil, worker.Attachment{}, ErrNotFound
	}
	name := entries[0].Name()
	f, err := root.Open(path.Join(id, name))
	if err != nil {
		return nil, worker.Attachment{}, err
	}
	a, err := describe(f, id, name)
	if err != nil {
		return nil, worker.Attachment{}, errors.Join(err, f.Close())
	}
	return f, a, nil
}

// describe returns the attachment of f. The type comes from the content, as browsers detect it.
func describe(f *os.File, id, name string) (worker.Attachment, error) {
	info, err := f.Stat()
	if err != nil {
		return worker.Attachment{}, err
	}
	head := make([]byte, 512)
	n, err := f.ReadAt(head, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return worker.Attachment{}, err
	}
	return worker.Attachment{
		ID: id, Name: name, Size: info.Size(), MimeType: http.DetectContentType(head[:n]),
		Path: path.Join("uploads", id, name),
	}, nil
}
