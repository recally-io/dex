package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/MichaelKinsy/PiG/durable"
	durableenv "github.com/MichaelKinsy/PiG/durable/env"
)

func (a *botExtensions) skillImportTool() *durable.ToolRegistration {
	return &durable.ToolRegistration{
		Name:        "skill_import",
		Description: "Copy one skill directory from this environment into the dex or bot store. The destination must not exist.",
		Parameters: skillObject([]string{"path", "level"}, map[string]any{
			"path": skillString, "name": skillString, "level": map[string]any{"type": "string", "enum": []string{"dex", "agent"}},
		}),
		StructuredOutputSchema: skillPathSchema(),
		Execute: func(ctx context.Context, raw any, api durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
			args, err := durable.FromJsonValue[struct {
				Path  string `json:"path"`
				Level string `json:"level"`
				Name  string `json:"name"`
			}](raw)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			env := api.Env()
			if env == nil {
				return durable.ToolExecutionResult{}, errors.New("skill_import needs a session environment")
			}
			absolute, err := env.AbsolutePath(ctx, args.Path)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			name := args.Name
			if name == "" {
				name = path.Base(filepath.ToSlash(absolute))
			}
			scope, dir, rel, err := a.skillPath(args.Level+"/"+name, true)
			if err != nil || strings.Contains(rel, "/") {
				return durable.ToolExecutionResult{}, errors.New("select dex or bot and a single skill directory name")
			}
			start, err := env.CanonicalPath(ctx, absolute)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			stage, err := os.MkdirTemp(filepath.Dir(dir), ".skill-import-")
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			defer func() { _ = os.RemoveAll(stage) }()
			warnings, err := copyImportedSkill(ctx, env, start, stage, name)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			err = func() error {
				skillWrites.Lock()
				defer skillWrites.Unlock()
				dest := filepath.Join(dir, rel)
				if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
					if err != nil {
						return err
					}
					return fmt.Errorf("skill %s/%s already exists", scope, name)
				}
				warningDir := filepath.Join(filepath.Dir(dir), ".skill-import-warnings")
				if len(warnings) > 0 {
					if err := os.MkdirAll(warningDir, 0o700); err != nil {
						return err
					}
					if err := os.WriteFile(filepath.Join(warningDir, name), []byte(strings.Join(warnings, "\n")), 0o600); err != nil {
						return err
					}
				} else if err := os.Remove(filepath.Join(warningDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
				return os.Rename(stage, dest)
			}()
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			return a.syncSkillResult(ctx, api, scope, dir, rel)
		},
	}
}

func copyImportedSkill(ctx context.Context, env durableenv.ExecutionEnv, start, dest, name string) ([]string, error) {
	type importedFile struct{ source, rel string }
	var files []importedFile
	var warnings []string
	active := map[string]bool{}
	var walk func(string, string) error
	walk = func(dir, rel string) error {
		if active[dir] {
			warnings = append(warnings, rel+": skipped symlink cycle")
			return nil
		}
		active[dir] = true
		defer delete(active, dir)
		entries, err := env.ListDir(ctx, dir)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(dest, filepath.FromSlash(strings.TrimPrefix(rel, name))), 0o700); err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Name == ".git" {
				continue
			}
			if !validSkillRel(name+"/"+entry.Name) || strings.Contains(entry.Name, "/") {
				return fmt.Errorf("invalid imported skill entry %q", entry.Name)
			}
			childRel := rel + "/" + entry.Name
			if entry.Kind == durableenv.FileKindSymlink {
				target, err := env.CanonicalPath(ctx, entry.Path)
				inside, ok := strings.CutPrefix(target, strings.TrimSuffix(start, "/")+"/")
				if err != nil || (!ok && target != start) || (ok && !validSkillRel(name+"/"+inside)) {
					warnings = append(warnings, childRel+": skipped symlink outside the skill or to an unavailable target")
					continue
				}
				entry, err = env.FileInfo(ctx, target)
				if err != nil {
					return err
				}
			}
			switch entry.Kind {
			case durableenv.FileKindDirectory:
				if err := walk(entry.Path, childRel); err != nil {
					return err
				}
			case durableenv.FileKindFile:
				files = append(files, importedFile{entry.Path, strings.TrimPrefix(childRel, name+"/")})
			case durableenv.FileKindSymlink:
				return fmt.Errorf("imported symlink changed during copy: %s", childRel)
			}
		}
		return nil
	}
	if err := walk(start, name); err != nil {
		return nil, err
	}
	for batch := range slices.Chunk(files, 100) {
		var script strings.Builder
		script.WriteString("set -eu\n")
		for _, file := range batch {
			fmt.Fprintf(&script, "stat -c '%%a' %[1]s 2>/dev/null || stat -f '%%Lp' %[1]s\n", singleQuoteSkillPath(file.source))
		}
		var modes strings.Builder
		res, err := env.Exec(ctx, script.String(), &durableenv.ShellExecOptions{OnOutput: func(_ context.Context, text string, info durableenv.ShellOutputInfo) {
			if info.Stream == durableenv.ShellStdout {
				modes.WriteString(text)
			}
		}})
		if err != nil {
			return nil, err
		}
		modeLines := strings.Fields(modes.String())
		if res.ExitCode != 0 || len(modeLines) != len(batch) {
			return nil, errors.New("cannot read imported skill file modes")
		}
		for i, file := range batch {
			mode, err := strconv.ParseUint(modeLines[i], 8, 32)
			if err != nil {
				return nil, err
			}
			data, err := env.ReadBinaryFile(ctx, file.source)
			if err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(dest, filepath.FromSlash(file.rel)), data, os.FileMode(mode).Perm()); err != nil {
				return nil, err
			}
			if err := os.Chmod(filepath.Join(dest, filepath.FromSlash(file.rel)), os.FileMode(mode).Perm()); err != nil {
				return nil, err
			}
		}
	}
	return warnings, nil
}

func singleQuoteSkillPath(p string) string { return "'" + strings.ReplaceAll(p, "'", "'\"'\"'") + "'" }
