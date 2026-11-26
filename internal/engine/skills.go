package engine

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
	durableenv "github.com/MichaelKinsy/PiG/durable/env"
	envnode "github.com/MichaelKinsy/PiG/durable/env/node"
	"github.com/MichaelKinsy/PiG/durable/tools"
	"github.com/recally-io/dex/internal/runner"
)

// The dex level is shared by all bots. Serialize host mutations and scans, but
// do not hold this lock while transferring files to a session environment.
var skillWrites sync.Mutex

type skillStore struct {
	repo     *memoryRepo
	snapshot runner.SkillSnapshot
}

func (a *Bots) skillStore(level ExtensionLevel) *skillStore {
	dir := filepath.Join(level.Dir, "skills")
	skillWrites.Lock()
	defer skillWrites.Unlock()
	if store := a.skillStores[dir]; store != nil {
		return store
	}
	repo := newMemoryRepo(dir, a.config.BaseEnv)
	repo.kind = "skills"
	store := &skillStore{repo: repo}
	a.skillStores[dir] = store
	return store
}

func (a *botExtensions) snapshots() (map[string]runner.SkillSnapshot, error) {
	skillWrites.Lock()
	defer skillWrites.Unlock()
	out := make(map[string]runner.SkillSnapshot, len(a.levels))
	for _, level := range a.distinct() {
		snapshot := &a.stores[level.Scope].snapshot
		if err := snapshot.Scan(filepath.Join(level.Dir, "skills")); err != nil {
			return nil, err
		}
		out[level.Scope] = *snapshot
	}
	return out, nil
}

func (a *botExtensions) syncRemoteSkills(ctx context.Context, env durableenv.ExecutionEnv) error {
	return a.syncSkills(ctx, env, true)
}

// trySyncSkills keeps unrelated tools available when the skill mirror cannot sync.
// Mutating skill tools use syncRemoteSkills directly to report a failed transfer.
func (a *botExtensions) trySyncSkills(ctx context.Context, env durableenv.ExecutionEnv, refresh bool) bool {
	if err := a.syncSkills(ctx, env, refresh); err != nil {
		if ctx.Err() == nil {
			slog.Warn("sync remote skills", "workspace", a.workspace, "error", err)
		}
		return false
	}
	return true
}

func (a *botExtensions) syncSkills(ctx context.Context, env durableenv.ExecutionEnv, refresh bool) error {
	if !a.isRemote() {
		return nil
	}
	a.skillsMu.Lock()
	defer a.skillsMu.Unlock()
	connection, err := runner.SkillConnection(ctx, a.execHandle)
	if err != nil {
		return err
	}
	if !refresh && a.skillStamp != "" && a.skillConnection == connection {
		return nil
	}
	snapshots, err := a.snapshots()
	if err != nil {
		return err
	}
	stamp := snapshots["dex"].Stamp() + snapshots["agent"].Stamp()
	if a.skillStamp == stamp && a.skillConnection == connection {
		return nil
	}
	if env == nil {
		env, err = a.execHandle.Env(ctx, a.workspace, nil)
		if err != nil {
			return err
		}
	}
	for _, scope := range slices.Sorted(maps.Keys(snapshots)) {
		snapshot := snapshots[scope]
		if err := snapshot.Sync(ctx, env, runner.JoinTargetPath(a.remoteSkillsRoot(), scope)); err != nil {
			return err
		}
	}
	a.skillStamp, a.skillConnection = stamp, connection
	return nil
}

func (a *botExtensions) openSkills(ctx context.Context) error {
	skillWrites.Lock()
	defer skillWrites.Unlock()
	for _, level := range a.distinct() {
		repo := a.stores[level.Scope].repo
		if info, err := os.Lstat(repo.Dir); err == nil && !info.IsDir() {
			return fmt.Errorf("skill store %s must be a directory, not a symlink", repo.Dir)
		}
		if err := repo.Open(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (a *Bots) commitSkills(ctx context.Context, botID, source string) error {
	files := a.files(botID, nil)
	skillWrites.Lock()
	defer skillWrites.Unlock()
	var errs []error
	for _, level := range files.distinct() {
		_, err := files.stores[level.Scope].repo.Commit(ctx, "Update skills from "+source)
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// managedSkills lists both host levels, including skills that a narrower level overrides.
func (a *botExtensions) managedSkills() ([]SkillInfo, []string) {
	snapshots, err := a.snapshots()
	out, warnings := []SkillInfo{}, []string{}
	if err != nil {
		return out, []string{err.Error()}
	}
	seen := map[string]bool{}
	for _, level := range a.distinct() {
		snapshot := snapshots[level.Scope]
		for _, warning := range snapshot.Warnings() {
			warnings = append(warnings, level.Scope+"/"+warning)
		}
		dirs, names := map[string]bool{}, map[string]bool{}
		base := filepath.Join(level.Dir, "skills")
		for _, rel := range snapshot.Files() {
			if path.Base(rel) != "SKILL.md" {
				continue
			}
			dir := path.Dir(rel)
			skip := false
			for parent := dir; parent != "."; parent = path.Dir(parent) {
				if dirs[parent] || strings.HasPrefix(path.Base(parent), ".") || path.Base(parent) == "node_modules" {
					skip = true
					break
				}
			}
			if skip {
				continue
			}
			dirs[dir] = true
			data, err := snapshot.Read(rel)
			name, desc, warning := parseSkill(string(data), path.Base(dir))
			if err != nil {
				warning = "cannot read SKILL.md"
			}
			if names[name] {
				warnings = append(warnings, level.Scope+"/"+rel+": duplicate skill name "+name)
				continue
			}
			names[name] = true
			out = append(out, SkillInfo{Name: name, Description: desc, Scope: level.Scope, Path: a.skillSessionPath(base, level.Scope, rel), Overrides: seen[name], Error: warning})
		}
		for name := range names {
			seen[name] = true
		}
		entries, _ := os.ReadDir(filepath.Join(level.Dir, ".skill-import-warnings"))
		for _, entry := range entries {
			if !entry.Type().IsRegular() {
				continue
			}
			if _, err := os.Stat(filepath.Join(base, entry.Name())); err != nil {
				continue
			}
			data, err := os.ReadFile(filepath.Join(level.Dir, ".skill-import-warnings", entry.Name()))
			if err == nil {
				for warning := range strings.SplitSeq(strings.TrimSuffix(string(data), "\n"), "\n") {
					warnings = append(warnings, level.Scope+"/"+warning)
				}
			}
		}
	}
	return out, warnings
}

func (a *botExtensions) skillSessionPath(base, scope, rel string) string {
	if a.isRemote() {
		return runner.JoinTargetPath(a.remoteSkillsRoot(), scope, rel)
	}
	return filepath.Join(base, filepath.FromSlash(rel))
}

func validSkillRel(rel string) bool {
	return rel != "" && rel != "." && path.Clean(rel) == rel && !strings.ContainsAny(rel, "\\\x00") && filepath.IsLocal(filepath.FromSlash(rel)) && !slices.Contains(strings.Split(rel, "/"), ".git") && strings.SplitN(rel, "/", 2)[0] != ".manifest.json"
}

func (a *botExtensions) skillPath(raw string, whole bool) (scope, dir, rel string, err error) {
	scope, rel, _ = strings.Cut(raw, "/")
	store := a.stores[scope]
	if store == nil || !validSkillRel(rel) || (!whole && !strings.Contains(rel, "/")) {
		return "", "", "", errors.New("use a skill path of the form <dex|bot>/<name>/<file>")
	}
	return scope, store.repo.Dir, rel, nil
}

var skillString = map[string]any{"type": "string", "minLength": 1}

func skillObject(required []string, properties map[string]any) map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": properties}
}

func skillPathSchema() map[string]any {
	return skillObject([]string{"path"}, map[string]any{"path": skillString})
}

func skillText(text string) durable.ToolExecutionResult {
	return durable.ToolExecutionResult{Output: []ai.ToolResultMessageContent{ai.TextContent{Text: text}}}
}

func (a *botExtensions) skillExtension() *durable.Extension {
	list := &durable.ToolRegistration{
		Name:        "skill_list",
		Description: "List dex and bot skills, including overrides and warnings.",
		Parameters:  skillObject([]string{}, map[string]any{}),
		Replay:      durable.ReplaySafe,
		Execute: func(_ context.Context, _ any, api durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
			target := a
			if bound, ok := api.Env().(*sessionPromptEnv); ok {
				target = bound.files
			}
			skills, warnings := target.managedSkills()
			data, err := json.Marshal(map[string]any{"skills": skills, "warnings": warnings})
			return skillText(string(data)), err
		},
	}
	write := tools.CreateWriteTool()
	write.Parameters = maps.Clone(write.Parameters)
	properties, _ := write.Parameters["properties"].(map[string]any)
	props := maps.Clone(properties)
	props["mode"] = map[string]any{"type": "string", "pattern": "^0?[0-7]{3}$", "description": "Optional octal mode, for example 0755 for an executable script."}
	write.Parameters["properties"] = props
	return &durable.Extension{Name: "skills", Tools: []*durable.ToolRegistration{
		list,
		a.wrapSkillFileTool(tools.CreateReadTool(nil), "skill_read", "Read a control-host skill file at <dex|bot>/<name>/<file>."),
		a.wrapSkillFileTool(write, "skill_write", "Write a control-host skill file and sync it to this environment. Use <dex|bot>/<name>/<file>."),
		a.wrapSkillFileTool(tools.CreateEditTool(), "skill_edit", "Edit a control-host skill file with exact oldText replacements and sync it to this environment."),
		a.wrapSkillFileTool(nil, "skill_delete", "Delete <dex|bot>/<name>/<file> or a whole skill at <dex|bot>/<name>."),
		a.skillImportTool(),
	}}
}

func (a *botExtensions) wrapSkillFileTool(base *durable.ToolRegistration, name, description string) *durable.ToolRegistration {
	schema := ai.ToolSchema{Parameters: skillPathSchema()}
	if base != nil {
		schema = base.ToolSchema
	}
	schema.Name, schema.Description = name, description
	t := &durable.ToolRegistration{ToolSchema: schema}
	read := name == "skill_read"
	if read {
		t.Replay = durable.ReplaySafe
	} else {
		t.StructuredOutputSchema = skillPathSchema()
	}
	t.Execute = func(ctx context.Context, raw any, api durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
		args, err := durable.FromJsonValue[map[string]any](raw)
		if err != nil {
			return durable.ToolExecutionResult{}, err
		}
		rawPath, _ := args["path"].(string)
		scope, dir, rel, err := a.skillPath(rawPath, name == "skill_delete")
		if err != nil {
			return durable.ToolExecutionResult{}, err
		}
		result, err := func() (durable.ToolExecutionResult, error) {
			skillWrites.Lock()
			defer skillWrites.Unlock()
			if name == "skill_delete" && !strings.Contains(rel, "/") {
				return durable.ToolExecutionResult{}, os.RemoveAll(filepath.Join(dir, rel))
			}
			if _, err := confineRegularFile(dir, rel, "skills", name == "skill_write"); err != nil {
				return durable.ToolExecutionResult{}, err
			}
			if name == "skill_delete" {
				return durable.ToolExecutionResult{}, os.Remove(filepath.Join(dir, filepath.FromSlash(rel)))
			}
			args["path"] = rel
			mode, _ := args["mode"].(string)
			delete(args, "mode")
			var perm uint64
			if mode != "" {
				perm, err = strconv.ParseUint(mode, 8, 9)
				if err != nil {
					return durable.ToolExecutionResult{}, errors.New("invalid octal skill mode")
				}
			}
			absolute := filepath.Join(dir, filepath.FromSlash(rel))
			env := envnode.NewNodeExecutionEnv(envnode.NodeExecutionEnvOptions{Cwd: dir})
			result, err := base.Execute(ctx, args, memoryToolAPI{ToolExecutionApi: api, env: skillFileEnv{env, absolute}})
			if err == nil && !isToolError(result) && mode != "" {
				err = os.Chmod(absolute, os.FileMode(perm))
			}
			return result, err
		}()
		if err != nil || read || isToolError(result) {
			return result, err
		}
		return a.syncSkillResult(ctx, api, scope, dir, rel)
	}
	return t
}

type skillFileEnv struct {
	durableenv.ExecutionEnv
	absolute string
}

// The native file tools normalize input paths. Always use the exact path that dex validated.
func (e skillFileEnv) AbsolutePath(context.Context, string) (string, error) { return e.absolute, nil }

func isToolError(result durable.ToolExecutionResult) bool {
	return result.IsError != nil && *result.IsError
}

func (a *botExtensions) syncSkillResult(ctx context.Context, api durable.ToolExecutionApi, scope, dir, rel string) (durable.ToolExecutionResult, error) {
	if a.onSkillChange != nil {
		a.onSkillChange(scope)
	}
	bound, ok := api.Env().(*sessionPromptEnv)
	if !ok {
		return durable.ToolExecutionResult{}, errors.New("skill changed on the control host, but the session environment is unavailable")
	}
	if err := bound.files.syncRemoteSkills(ctx, bound); err != nil {
		return durable.ToolExecutionResult{}, fmt.Errorf("skill changed on the control host, but sync failed: %w", err)
	}
	p := bound.files.skillSessionPath(dir, scope, rel)
	result := skillText(p)
	result.StructuredOutput, result.HasStructuredOutput = map[string]any{"path": p}, true
	return result, nil
}
