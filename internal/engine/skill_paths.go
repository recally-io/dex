package engine

import (
	"context"
	"errors"
	"path"
	"path/filepath"
	"strings"

	"github.com/MichaelKinsy/PiG/durable"
	durableenv "github.com/MichaelKinsy/PiG/durable/env"
)

func withinSkillPath(base, target string) bool {
	return target == base || strings.HasPrefix(target, base+"/") || strings.HasPrefix(target, base+`\`)
}

func (a *botExtensions) protectedSkillPath(p string) bool {
	for _, level := range a.levels {
		if withinSkillPath(filepath.Join(level.Dir, "skills"), p) {
			return true
		}
	}
	return a.isRemote() && withinSkillPath(a.remoteSkillsRoot(), p)
}

type skillProtectedEnv struct {
	*sessionPromptEnv
}

// AbsolutePath receives the path after the native write/edit tool normalizes it.
func (e skillProtectedEnv) AbsolutePath(ctx context.Context, raw string) (string, error) {
	abs, err := e.sessionPromptEnv.AbsolutePath(ctx, raw)
	if err != nil {
		return "", err
	}
	refuse := errors.New("managed skills are read-only to write and edit; use skill_* tools")
	if e.files.protectedSkillPath(abs) {
		return "", refuse
	}
	// Resolve the closest existing ancestor so an alias cannot create a file in
	// the store, even when the final file or its intermediate parents are new.
	for p := abs; ; {
		canonical, err := e.CanonicalPath(ctx, p)
		if err == nil {
			if e.files.protectedSkillPath(canonical) {
				return "", refuse
			}
			if e.files.isRemote() {
				if mirror, err := e.CanonicalPath(ctx, e.files.remoteSkillsRoot()); err == nil && withinSkillPath(mirror, canonical) {
					return "", refuse
				}
			}
			break
		}
		if failure, ok := errors.AsType[*durableenv.FileError](err); !ok || failure.Code != durableenv.FileErrorNotFound {
			return "", err
		}
		if info, err := e.FileInfo(ctx, p); err == nil && info.Kind == durableenv.FileKindSymlink {
			return "", errors.New("cannot resolve write target symlink")
		}
		parent := filepath.Dir(p)
		if strings.HasPrefix(p, "/") {
			parent = path.Dir(p)
		}
		if parent == p {
			break
		}
		p = parent
	}
	return abs, nil
}

func protectSkillTool(tool *durable.ToolRegistration) {
	execute := tool.Execute
	tool.Execute = func(ctx context.Context, args any, api durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
		if bound, ok := api.Env().(*sessionPromptEnv); ok {
			api = memoryToolAPI{ToolExecutionApi: api, env: skillProtectedEnv{bound}}
		}
		return execute(ctx, args, api)
	}
}
