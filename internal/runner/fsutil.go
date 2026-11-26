package runner

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	durableenv "github.com/MichaelKinsy/PiG/durable/env"
)

const (
	maxDirectoryEntries = 500
	readWorkspaceChunk  = 512 * 1024
)

// DirectoryEntry describes one visible subdirectory inside a browsed directory.
type DirectoryEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Git  bool   `json:"git"`
}

// DirectoryListing holds the subdirectories and navigation metadata for a path.
type DirectoryListing struct {
	Path      string           `json:"path"`
	Parent    string           `json:"parent"`
	Home      string           `json:"home"`
	Entries   []DirectoryEntry `json:"entries"`
	Truncated bool             `json:"truncated"`
}

func remoteBotWorkspace(base, sep, botID string) string {
	return joinRemoteSegments(base, sep, botID, "workspace")
}

func createRemoteBotWorkspace(ctx context.Context, h lease) error {
	workspace := h.Workspace()
	env, err := h.Env(ctx, workspace, nil)
	if err != nil {
		return err
	}
	return env.CreateDir(ctx, workspace, &durableenv.CreateDirOptions{Recursive: new(true)})
}

// StageUpload copies a regular local file at relPath inside localWorkspace into remoteWorkspace.
func StageUpload(ctx context.Context, env durableenv.ExecutionEnv, localWorkspace, remoteWorkspace, relPath string) error {
	cleanSlash, err := validateRelativePath(relPath)
	if err != nil {
		return err
	}
	if localWorkspace == "" || remoteWorkspace == "" {
		return errors.New("workspace is required to stage upload")
	}
	root, err := os.OpenRoot(localWorkspace)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	localRel := filepath.FromSlash(cleanSlash)
	info, err := root.Lstat(localRel)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("upload %q must be a regular non-symlink file", cleanSlash)
	}
	data, err := root.ReadFile(localRel)
	if err != nil {
		return err
	}

	target, err := env.JoinPath(ctx, append([]string{remoteWorkspace}, strings.Split(cleanSlash, "/")...))
	if err != nil {
		return err
	}
	if existing, err := env.FileInfo(ctx, target); err == nil && existing.Kind == durableenv.FileKindSymlink {
		return fmt.Errorf("refusing to overwrite symlink %q", cleanSlash)
	}
	if parent := path.Dir(cleanSlash); parent != "." {
		remoteParent, err := env.JoinPath(ctx, append([]string{remoteWorkspace}, strings.Split(parent, "/")...))
		if err != nil {
			return err
		}
		if err := env.CreateDir(ctx, remoteParent, &durableenv.CreateDirOptions{Recursive: new(true)}); err != nil {
			return err
		}
	}
	return env.WriteFile(ctx, target, data)
}

// ReadWorkspaceFile reads up to maxBytes of a regular, non-symlink file confined inside the workspace of h.
func ReadWorkspaceFile(ctx context.Context, h Handle, relPath string, maxBytes int64) ([]byte, error) {
	env, err := h.Env(ctx, h.Workspace(), nil)
	if err != nil {
		return nil, err
	}
	return readWorkspaceFile(ctx, env, h.Workspace(), relPath, maxBytes)
}

func readWorkspaceFile(ctx context.Context, env durableenv.ExecutionEnv, workspace, relPath string, maxBytes int64) ([]byte, error) {
	if maxBytes < 0 {
		return nil, errors.New("maxBytes must be non-negative")
	}
	cleanSlash, err := validateRelativePath(relPath)
	if err != nil {
		return nil, err
	}
	if workspace == "" {
		return nil, errors.New("workspace is required")
	}
	canonWS, err := env.CanonicalPath(ctx, workspace)
	if err != nil {
		return nil, err
	}
	target, err := env.JoinPath(ctx, append([]string{workspace}, strings.Split(cleanSlash, "/")...))
	if err != nil {
		return nil, err
	}
	if parent := path.Dir(cleanSlash); parent != "." {
		parentTarget, err := env.JoinPath(ctx, append([]string{workspace}, strings.Split(parent, "/")...))
		if err != nil {
			return nil, err
		}
		canonParent, err := env.CanonicalPath(ctx, parentTarget)
		if err != nil {
			return nil, err
		}
		if !isPathWithin(canonWS, canonParent) {
			return nil, fmt.Errorf("path %q escapes workspace", cleanSlash)
		}
	}
	reader, err := env.OpenBinaryReader(ctx, target, &durableenv.OpenBinaryReaderOptions{NoFollow: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close(context.WithoutCancel(ctx)) }()

	info, err := reader.Info(ctx)
	if err != nil {
		return nil, err
	}
	if info.Kind != durableenv.FileKindFile || info.Size > maxBytes {
		return nil, fmt.Errorf("deliver a regular file of at most %d bytes", maxBytes)
	}

	var out []byte
	var offset int64
	for offset <= maxBytes {
		chunk, err := reader.Read(ctx, offset, min(readWorkspaceChunk, maxBytes-offset+1))
		if err != nil {
			return nil, err
		}
		if len(chunk) == 0 {
			break
		}
		out = append(out, chunk...)
		offset += int64(len(chunk))
		if int64(len(out)) > maxBytes {
			return nil, fmt.Errorf("deliver a regular file of at most %d bytes", maxBytes)
		}
	}
	return out, nil
}

// ResolveProjectPath validates and normalizes a project directory inside the environment of h.
// Empty input returns an empty string.
func ResolveProjectPath(ctx context.Context, h Handle, raw string) (string, error) {
	env, err := h.Env(ctx, h.Workspace(), nil)
	if err != nil {
		return "", err
	}
	return resolveProjectPath(ctx, env, h.Home(), raw)
}

func resolveProjectPath(ctx context.Context, env durableenv.ExecutionEnv, home, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if raw == "~" || strings.HasPrefix(raw, "~/") {
		if home == "" {
			return "", fmt.Errorf("%w: home directory is unavailable", ErrInvalidPath)
		}
		if raw == "~" {
			raw = home
		} else {
			joined, err := env.JoinPath(ctx, []string{home, strings.TrimPrefix(raw, "~/")})
			if err != nil {
				return "", fmt.Errorf("%w: directory is unavailable", ErrInvalidPath)
			}
			raw = joined
		}
	}
	if !IsAbsPath(raw) {
		return "", fmt.Errorf("%w: path must be absolute", ErrInvalidPath)
	}
	clean := cleanAbsPath(raw)
	info, err := StatFollow(ctx, env, clean)
	if err != nil {
		return "", fmt.Errorf("%w: directory is unavailable", ErrInvalidPath)
	}
	if info.Kind != durableenv.FileKindDirectory {
		return "", fmt.Errorf("%w: path must be a directory", ErrInvalidPath)
	}
	return clean, nil
}

// ListDirectories lists sorted, non-hidden subdirectories inside path (defaulting to the home of h), capped at 500 entries.
func ListDirectories(ctx context.Context, h Handle, path *string) (DirectoryListing, error) {
	env, err := h.Env(ctx, h.Workspace(), nil)
	if err != nil {
		return DirectoryListing{}, err
	}
	return listDirectories(ctx, env, h.Home(), path)
}

func listDirectories(ctx context.Context, env durableenv.ExecutionEnv, home string, path *string) (DirectoryListing, error) {
	if home == "" {
		return DirectoryListing{}, fmt.Errorf("%w: home directory is unavailable", ErrInvalidPath)
	}
	selected := home
	if path != nil {
		selected = *path
		if strings.TrimSpace(selected) == "" {
			return DirectoryListing{}, fmt.Errorf("%w: path must be absolute", ErrInvalidPath)
		}
	}
	selected, err := resolveProjectPath(ctx, env, home, selected)
	if err != nil {
		return DirectoryListing{}, err
	}
	listed, err := env.ListDir(ctx, selected)
	if err != nil {
		return DirectoryListing{}, fmt.Errorf("%w: directory cannot be read", ErrInvalidPath)
	}

	entries := []DirectoryEntry{}
	for _, entry := range listed {
		if strings.HasPrefix(entry.Name, ".") {
			continue
		}
		isDir := entry.Kind == durableenv.FileKindDirectory
		if entry.Kind == durableenv.FileKindSymlink {
			target, statErr := StatFollow(ctx, env, entry.Path)
			isDir = statErr == nil && target.Kind == durableenv.FileKindDirectory
		}
		if isDir {
			entries = append(entries, DirectoryEntry{Name: entry.Name, Path: entry.Path})
		}
	}

	slices.SortFunc(entries, func(a, b DirectoryEntry) int {
		return cmp.Or(strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), strings.Compare(a.Name, b.Name))
	})
	truncated := len(entries) > maxDirectoryEntries
	if truncated {
		entries = entries[:maxDirectoryEntries]
	}
	for i := range entries {
		gitPath, err := env.JoinPath(ctx, []string{entries[i].Path, ".git"})
		if err != nil {
			continue
		}
		gitInfo, err := StatFollow(ctx, env, gitPath)
		entries[i].Git = err == nil && (gitInfo.Kind == durableenv.FileKindDirectory || gitInfo.Kind == durableenv.FileKindFile)
	}

	parent := parentAbsPath(selected)
	if parent == selected {
		parent = ""
	}
	return DirectoryListing{
		Path:      selected,
		Parent:    parent,
		Home:      cleanAbsPath(home),
		Entries:   entries,
		Truncated: truncated,
	}, nil
}

// StatFollow returns FileInfo for target, resolving a symbolic link to its target.
func StatFollow(ctx context.Context, env durableenv.ExecutionEnv, target string) (durableenv.FileInfo, error) {
	info, err := env.FileInfo(ctx, target)
	if err != nil || info.Kind != durableenv.FileKindSymlink {
		return info, err
	}
	resolved, err := env.CanonicalPath(ctx, target)
	if err != nil {
		return durableenv.FileInfo{}, err
	}
	return env.FileInfo(ctx, resolved)
}

// IsAbsPath reports whether p is a POSIX, UNC, or Windows drive absolute path.
func IsAbsPath(p string) bool {
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) {
		return true
	}
	return len(p) >= 3 && ((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z')) && p[1] == ':' && (p[2] == '/' || p[2] == '\\')
}

// JoinTargetPath joins path segments matching base's POSIX or host separator convention.
func JoinTargetPath(base string, parts ...string) string {
	all := append([]string{base}, parts...)
	if strings.HasPrefix(base, "/") {
		return path.Join(all...)
	}
	return filepath.Join(all...)
}

func validateRelativePath(relPath string) (string, error) {
	trimmed := strings.TrimSpace(relPath)
	if trimmed == "" || IsAbsPath(trimmed) {
		return "", errors.New("path must be workspace-relative")
	}
	clean := path.Clean(filepath.ToSlash(trimmed))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") || !filepath.IsLocal(filepath.FromSlash(clean)) {
		return "", fmt.Errorf("path %q must stay within workspace", relPath)
	}
	return clean, nil
}

func isPathWithin(base, target string) bool {
	if target == base {
		return true
	}
	if strings.HasSuffix(base, "/") || strings.HasSuffix(base, `\`) {
		return strings.HasPrefix(target, base)
	}
	return strings.HasPrefix(target, base+"/") || strings.HasPrefix(target, base+`\`)
}

func cleanAbsPath(p string) string {
	if strings.HasPrefix(p, "/") {
		return path.Clean(p)
	}
	return filepath.Clean(p)
}

func parentAbsPath(p string) string {
	if strings.HasPrefix(p, "/") {
		return path.Dir(p)
	}
	return filepath.Dir(p)
}
