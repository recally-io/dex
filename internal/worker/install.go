package worker

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Install writes the gzipped worker binary to dir as an executable and returns its path. The file
// name holds a hash of the content, so a new dex build installs a new file and an existing file is
// reused.
//
// Old worker files stay in dir; delete them on start if the cache grows.
func Install(dir string, gz []byte) (string, error) {
	sum := sha256.Sum256(gz)
	path := filepath.Join(dir, "worker-"+hex.EncodeToString(sum[:8]))
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// Write to a temporary file and rename it, so a crash never leaves a partial binary at path.
	tmp, err := os.CreateTemp(dir, "worker-*.tmp")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // fails harmlessly after the rename
	if err := unzip(tmp, gz); err != nil {
		return "", errors.Join(fmt.Errorf("unpack worker: %w", err), tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o700); err != nil { //nolint:gosec // an executable needs the x bit
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}

func unzip(dst io.Writer, gz []byte) error {
	r, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, r); err != nil { //nolint:gosec // the archive is embedded at build time
		return err
	}
	return r.Close()
}
