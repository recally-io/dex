package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const maxAssetTimeout = 5 * time.Minute

// DownloadReleaseAsset streams a named asset from the release for tag to out.
func DownloadReleaseAsset(ctx context.Context, base, tag, assetName string, out io.Writer) error {
	u := NewForCheck(tag, base)
	u.client = releaseClient(maxAssetTimeout)
	u.ensureOptionalToken(ctx, false)
	r, err := u.selectRelease(ctx, Options{Tag: tag})
	if err != nil {
		return err
	}
	var assetURL string
	for _, a := range r.Assets {
		if a.Name == assetName {
			assetURL = a.URL
			break
		}
	}
	if assetURL == "" {
		return fmt.Errorf("missing release asset %s for %s", assetName, tag)
	}
	limit := int64(maxAsset)
	if assetName == "checksums.txt" {
		limit = 8 << 20
	}
	return u.download(ctx, assetURL, "application/octet-stream", limit, out, nil)
}

// VerifyChecksum checks that data matches the SHA-256 entry for fileName in checksums.
func VerifyChecksum(checksums []byte, data io.Reader, fileName string) error {
	expected := ""
	for line := range strings.SplitSeq(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == fileName {
			expected = fields[0]
		}
	}
	digest, err := hex.DecodeString(expected)
	if err != nil || len(digest) != sha256.Size {
		return fmt.Errorf("missing or invalid checksum for %s", fileName)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, data); err != nil {
		return err
	}
	if !bytes.Equal(digest, hash.Sum(nil)) {
		return errors.New("release checksum mismatch")
	}
	return nil
}

// ExtractBinary extracts the single regular file named binaryName from a .tar.gz release archive.
func ExtractBinary(archive []byte, binaryName string, out io.Writer) error {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("release gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(io.LimitReader(gz, maxAsset+1))
	found := false
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("release tar: %w", err)
		}
		if h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > maxAsset {
			return fmt.Errorf("archive entry is not a regular file within the size limit: %q", h.Name)
		}
		if h.Name == binaryName {
			if found {
				return fmt.Errorf("duplicate %s archive entry", binaryName)
			}
			found = true
			if _, err := io.CopyN(out, tr, h.Size); err != nil {
				return err
			}
		}
	}
	if !found {
		return fmt.Errorf("archive has no regular file named %s", binaryName)
	}
	return nil
}

// StageExecutable extracts and verifies the executable without changing the installed file.
func StageExecutable(ctx context.Context, executable, binaryName, tag string, archive []byte) (string, error) {
	file, err := os.CreateTemp(filepath.Dir(executable), ".dex-update-*")
	if err != nil {
		return "", fmt.Errorf("executable directory is not writable: %w", err)
	}
	stage := file.Name()
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(stage)
		}
	}()
	if err := ExtractBinary(archive, binaryName, file); err != nil {
		return "", err
	}
	if err := file.Chmod(0755); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	verify, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(verify, stage, "version").Output() //nolint:gosec // Run the verified release executable.
	if err != nil {
		return "", fmt.Errorf("staged executable did not run: %w", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 || fields[0] != binaryName || fields[1] != tag {
		return "", errors.New("staged executable reported an unexpected version")
	}
	keep = true
	return stage, nil
}

// ReplaceExecutable keeps executable.previous via hardlink and atomically replaces executable with stage.
func ReplaceExecutable(executable, stage string) error {
	backup := stage + ".previous"
	if err := os.Link(executable, backup); err != nil {
		return fmt.Errorf("retain previous executable: %w", err)
	}
	defer os.Remove(backup)
	if err := os.Rename(backup, executable+".previous"); err != nil {
		return err
	}
	return os.Rename(stage, executable)
}

// WritableExecutable checks whether executable is user-writable and not managed by a system package manager.
func WritableExecutable(executable string) (string, string) {
	if executable == "" {
		return "", "not-writable"
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", "not-writable"
	}
	if isPackageManaged(resolved) {
		return resolved, "package"
	}
	f, err := os.CreateTemp(filepath.Dir(resolved), ".dex-writable-*")
	if err != nil {
		return resolved, "not-writable"
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return resolved, ""
}
