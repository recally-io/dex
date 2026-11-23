package execenv

import (
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"uuid"

	durableenv "github.com/MichaelKinsy/PiG/durable/env"
)

const skillManifestName = ".manifest.json"

type skillFile struct {
	SHA256 string      `json:"sha256"`
	Mode   os.FileMode `json:"mode"`
}

type skillManifest struct {
	Files map[string]skillFile `json:"files"`
	Dirs  []string             `json:"directories"`
}

type cachedSkillHash struct {
	size  int64
	mtime time.Time
	hash  string
}

// SkillSnapshot caches hashes for the next scan of one control-host store.
// The caller serializes scans, changes, and reads of the snapshot.
type SkillSnapshot struct {
	dir      string
	manifest skillManifest
	sources  map[string]string
	hashes   map[string]cachedSkillHash
	warnings []string
}

func (s *SkillSnapshot) Warnings() []string { return s.warnings }
func (s *SkillSnapshot) Files() []string    { return slices.Sorted(maps.Keys(s.sources)) }
func (s *SkillSnapshot) Read(rel string) ([]byte, error) {
	p, ok := s.sources[rel]
	if !ok {
		return nil, os.ErrNotExist
	}
	name, _, _ := strings.Cut(rel, "/")
	base := filepath.Join(s.dir, name)
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	skill, err := root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = skill.Close() }()
	resolved, err := filepath.Rel(base, p)
	if err != nil {
		return nil, err
	}
	f, err := skill.Open(resolved)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	if cached, ok := s.hashes[p]; ok {
		info, err := f.Stat()
		if err != nil {
			return nil, err
		}
		if cached.size != info.Size() || !cached.mtime.Equal(info.ModTime()) {
			return nil, fmt.Errorf("skill changed during sync: %s", rel)
		}
	}
	return data, nil
}

func (s *SkillSnapshot) Scan(dir string) error {
	next := SkillSnapshot{manifest: skillManifest{Files: map[string]skillFile{}, Dirs: []string{}}, sources: map[string]string{}, hashes: map[string]cachedSkillHash{}}
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		*s = next
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("skill store %s must be a directory, not a symlink", dir)
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	next.dir = dir
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == ".git" || entry.Name() == skillManifestName {
			continue
		}
		base := filepath.Join(dir, entry.Name())
		var walk func(string, string) error
		active := map[string]bool{}
		walk = func(rel, p string) error {
			info, err := os.Lstat(p)
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := filepath.EvalSymlinks(p)
				inside, relErr := filepath.Rel(base, target)
				if err != nil || relErr != nil || !filepath.IsLocal(inside) || slices.Contains(strings.Split(filepath.ToSlash(inside), "/"), ".git") {
					next.warnings = append(next.warnings, rel+": skipped symlink outside the skill or to an unavailable target")
					return nil
				}
				p = target
				info, err = os.Stat(p)
				if err != nil {
					return err
				}
			}
			if info.IsDir() {
				if active[p] {
					next.warnings = append(next.warnings, rel+": skipped symlink cycle")
					return nil
				}
				active[p] = true
				defer delete(active, p)
				next.manifest.Dirs = append(next.manifest.Dirs, rel)
				children, err := os.ReadDir(p)
				if err != nil {
					return err
				}
				for _, child := range children {
					if child.Name() != ".git" {
						if err := walk(rel+"/"+child.Name(), filepath.Join(p, child.Name())); err != nil {
							return err
						}
					}
				}
				return nil
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			next.sources[rel] = p
			cached, ok := next.hashes[p]
			if !ok {
				cached, ok = s.hashes[p]
			}
			if !ok || cached.size != info.Size() || !cached.mtime.Equal(info.ModTime()) {
				data, err := next.Read(rel)
				if err != nil {
					return err
				}
				cached = cachedSkillHash{info.Size(), info.ModTime(), fmt.Sprintf("%x", sha256.Sum256(data))}
			}
			next.hashes[p] = cached
			next.manifest.Files[rel] = skillFile{cached.hash, info.Mode().Perm()}
			return nil
		}
		if err := walk(entry.Name(), base); err != nil {
			return err
		}
	}
	*s = next
	return nil
}

func (s SkillSnapshot) Stamp() string {
	data, _ := json.Marshal(s.manifest, json.Deterministic(true))
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// SyncSkills mirrors a stand-alone level. The engine retains a SkillSnapshot across syncs.
func SyncSkills(ctx context.Context, env durableenv.ExecutionEnv, levelDir, target string) error {
	var snapshot SkillSnapshot
	if err := snapshot.Scan(filepath.Join(levelDir, "skills")); err != nil {
		return err
	}
	return snapshot.Sync(ctx, env, target)
}

func skillShellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

// Sync uploads changed files to staging first, then applies modes and installs the manifest
// in one shell call. Unchanged files are never opened for writing.
func (s *SkillSnapshot) Sync(ctx context.Context, env durableenv.ExecutionEnv, target string) error {
	if target == "" {
		return errors.New("remote skills directory is required")
	}
	parent := path.Dir(target)
	for _, dir := range []string{path.Dir(parent), parent, target} {
		if _, err := checkMirrorEntry(ctx, env, dir, durableenv.FileKindDirectory); err != nil {
			return err
		}
	}
	manifestPath := JoinTargetPath(target, skillManifestName)
	exists, err := checkMirrorEntry(ctx, env, manifestPath, durableenv.FileKindFile)
	if err != nil {
		return err
	}
	var old skillManifest
	if exists {
		data, err := env.ReadBinaryFile(ctx, manifestPath)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &old); err != nil {
			return fmt.Errorf("read skill manifest: %w", err)
		}
	}
	valid := func(rel string) bool {
		return rel != "." && filepath.IsLocal(filepath.FromSlash(rel)) && path.Clean(rel) == rel && !strings.Contains(rel, "\\") && !slices.Contains(strings.Split(rel, "/"), ".git") && rel != skillManifestName
	}
	for rel := range old.Files {
		if !valid(rel) {
			return fmt.Errorf("invalid skill manifest path %q", rel)
		}
	}
	for _, rel := range old.Dirs {
		if !valid(rel) {
			return fmt.Errorf("invalid skill manifest directory %q", rel)
		}
	}
	for rel := range s.manifest.Files {
		if !valid(rel) {
			return fmt.Errorf("invalid skill path %q", rel)
		}
	}
	for _, rel := range s.manifest.Dirs {
		if !valid(rel) {
			return fmt.Errorf("invalid skill directory %q", rel)
		}
	}
	if maps.Equal(old.Files, s.manifest.Files) && slices.Equal(old.Dirs, s.manifest.Dirs) {
		return nil
	}

	stage := JoinTargetPath(parent, ".stage-"+uuid.New().String())
	if err := env.CreateDir(ctx, stage, &durableenv.CreateDirOptions{Recursive: new(true)}); err != nil {
		return err
	}
	defer func() {
		_ = env.Remove(context.WithoutCancel(ctx), stage, &durableenv.RemoveOptions{Recursive: true, Force: true})
	}()
	q := skillShellQuote
	remote := func(rel string) string { return q(JoinTargetPath(target, rel)) }
	var sh strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&sh, format+"\n", args...) }
	line("set -eu")
	line("[ ! -L %s ] && [ ! -L %s ] && [ ! -L %s ] || exit 1", q(path.Dir(parent)), q(parent), q(target))
	line("mkdir -p %s; chmod u+w %s", q(target), q(target))
	// Track only the directories this sync unlocked, even if applying it fails.
	line("set -- %s", q(target))
	line(`trap 'status=$?; for dir do [ ! -d "$dir" ] || chmod a-w "$dir" || status=1; done; exit "$status"' EXIT`)
	currentDirs := map[string]bool{}
	for _, rel := range s.manifest.Dirs {
		currentDirs[rel] = true
	}
	removedSkills := map[string]bool{}
	for _, rel := range old.Dirs {
		if !strings.Contains(rel, "/") && !currentDirs[rel] {
			removedSkills[rel] = true
			line("if [ -d %[1]s ] && [ ! -L %[1]s ]; then find %[1]s -type d -exec chmod u+w {} +; fi; rm -rf %[1]s", remote(rel))
		}
	}
	opened := map[string]bool{}
	var openDir func(string)
	openDir = func(rel string) {
		if rel == "." || opened[rel] {
			return
		}
		openDir(path.Dir(rel))
		opened[rel] = true
		line(`[ ! -L %[1]s ]; if [ -d %[1]s ]; then chmod u+w %[1]s; set -- "$@" %[1]s; fi`, remote(rel))
	}
	for _, rel := range slices.Sorted(maps.Keys(old.Files)) {
		if _, ok := s.manifest.Files[rel]; ok {
			continue
		}
		name, _, _ := strings.Cut(rel, "/")
		if removedSkills[name] {
			continue
		}
		openDir(path.Dir(rel))
		line("rm -f %s", remote(rel))
	}
	for _, rel := range slices.Backward(old.Dirs) {
		name, _, _ := strings.Cut(rel, "/")
		if currentDirs[rel] || removedSkills[name] {
			continue
		}
		openDir(path.Dir(rel))
		line("rmdir %s 2>/dev/null || :", remote(rel))
	}
	oldDirs := map[string]bool{}
	for _, rel := range old.Dirs {
		oldDirs[rel] = true
	}
	for _, rel := range s.manifest.Dirs {
		if oldDirs[rel] {
			continue
		}
		openDir(path.Dir(rel))
		line(`[ ! -L %[1]s ]; mkdir -p %[1]s; set -- "$@" %[1]s`, remote(rel))
		opened[rel] = true
	}
	for i, rel := range s.Files() {
		file := s.manifest.Files[rel]
		if old.Files[rel] == file {
			continue
		}
		original, err := s.Read(rel)
		if err != nil {
			return err
		}
		staged := JoinTargetPath(stage, fmt.Sprint(i))
		if err := env.WriteFile(ctx, staged, original); err != nil {
			return err
		}
		openDir(path.Dir(rel))
		line("[ ! -d %[3]s ] && [ ! -L %[3]s ] || exit 1; chmod %04[1]o %[2]s; mv -f %[2]s %[3]s", file.Mode&^0o222, q(staged), remote(rel))
	}
	data, err := json.Marshal(s.manifest, json.Deterministic(true))
	if err != nil {
		return err
	}
	stagedManifest := JoinTargetPath(stage, skillManifestName)
	if err := env.WriteFile(ctx, stagedManifest, data); err != nil {
		return err
	}
	line("chmod 0444 %[1]s; [ ! -d %[2]s ] && [ ! -L %[2]s ] || exit 1; mv -f %[1]s %[2]s", q(stagedManifest), q(manifestPath))
	script := JoinTargetPath(stage, "apply.sh")
	if err := env.WriteFile(ctx, script, sh.String()); err != nil {
		return err
	}
	return runSkillShell(ctx, env, "sh "+q(script))
}

func checkMirrorEntry(ctx context.Context, env durableenv.ExecutionEnv, name string, kind durableenv.FileKind) (bool, error) {
	info, err := env.FileInfo(ctx, name)
	if failure, ok := errors.AsType[*durableenv.FileError](err); ok && failure.Code == durableenv.FileErrorNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Kind != kind {
		return false, fmt.Errorf("unexpected file type in skill mirror: %s", name)
	}
	return true, nil
}

func runSkillShell(ctx context.Context, env durableenv.ExecutionEnv, command string) error {
	var output strings.Builder
	res, err := env.Exec(ctx, command, &durableenv.ShellExecOptions{OnOutput: func(_ context.Context, text string, _ durableenv.ShellOutputInfo) { output.WriteString(text) }})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("apply skill manifest (exit %d): %s", res.ExitCode, strings.TrimSpace(output.String()))
	}
	return nil
}
