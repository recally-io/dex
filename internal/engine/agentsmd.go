package engine

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	durableenv "github.com/MichaelKinsy/PiG/durable/env"
	envnode "github.com/MichaelKinsy/PiG/durable/env/node"
	"github.com/recally-io/dex/internal/execenv"
)

// agentsMDDepth is how many import levels below an AGENTS.md dex follows.
const agentsMDDepth = 5

// agentsMD is the text of one AGENTS.md with its imports.
type agentsMD struct {
	// text is empty when the file is missing.
	text string
	// path is the real path of the file.
	path     string
	scope    string
	warnings []string
}

type agentsMDReader struct {
	ctx  context.Context
	env  durableenv.ExecutionEnv
	home string
	// confine is the real project directory; empty means no limit (the agent level).
	confine  string
	seen     map[string]bool
	out      strings.Builder
	warnings []string
}

func readAgentsMDLocal(ctx context.Context, targetPath, home, confine string) agentsMD {
	env := envnode.NewNodeExecutionEnv(envnode.NodeExecutionEnvOptions{Cwd: filepath.Dir(targetPath)})
	return readAgentsMDEnv(ctx, env, targetPath, home, confine)
}

func readAgentsMDEnv(ctx context.Context, env durableenv.ExecutionEnv, targetPath, home, confine string) agentsMD {
	r := &agentsMDReader{ctx: ctx, env: env, home: home, confine: confine, seen: map[string]bool{}}
	resolved, ok := r.regularFile(targetPath)
	if !ok {
		return agentsMD{}
	}
	if !r.allowed(resolved) {
		r.warnings = append(r.warnings, targetPath+": the file is outside the project directory and dex skips it")
		return agentsMD{warnings: r.warnings}
	}
	r.load(resolved, 0)
	return agentsMD{text: strings.TrimSpace(r.out.String()), path: resolved, warnings: r.warnings}
}

func (r *agentsMDReader) regularFile(target string) (string, bool) {
	resolved, err := r.env.CanonicalPath(r.ctx, target)
	if err != nil {
		return "", false
	}
	info, err := r.env.FileInfo(r.ctx, resolved)
	return resolved, err == nil && info.Kind == durableenv.FileKindFile
}

func (r *agentsMDReader) allowed(resolved string) bool {
	if r.confine == "" {
		return true
	}
	if strings.HasPrefix(r.confine, "/") && strings.HasPrefix(resolved, "/") {
		cleanConfine := path.Clean(r.confine)
		cleanResolved := path.Clean(resolved)
		if cleanConfine == "/" {
			return true
		}
		return cleanResolved == cleanConfine || strings.HasPrefix(cleanResolved, cleanConfine+"/")
	}
	rel, err := filepath.Rel(r.confine, resolved)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (r *agentsMDReader) load(resolved string, depth int) {
	r.seen[resolved] = true
	data, err := r.env.ReadBinaryFile(r.ctx, resolved)
	if err != nil {
		r.warnings = append(r.warnings, resolved+": cannot read the file")
		return
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if depth > 0 {
		r.out.WriteString("\n\nImported file " + resolved + ":\n\n")
	}
	r.out.WriteString(text)
	parentDir := filepath.Dir(resolved)
	if strings.HasPrefix(resolved, "/") {
		parentDir = path.Dir(resolved)
	}
	for _, token := range importTokens(text) {
		target, ok := r.resolve(parentDir, token)
		switch {
		case !ok:
			if strings.Contains(token, "/") || strings.HasSuffix(token, ".md") {
				r.warnings = append(r.warnings, fmt.Sprintf("%s: the import @%s is not a file", resolved, token))
			}
		case r.seen[target]:
		case !r.allowed(target):
			r.warnings = append(r.warnings, fmt.Sprintf("%s: the import @%s is outside the project directory and dex skips it", resolved, token))
		case depth >= agentsMDDepth:
			r.warnings = append(r.warnings, fmt.Sprintf("%s: the import @%s is deeper than %d levels and dex skips it", resolved, token, agentsMDDepth))
		default:
			r.load(target, depth+1)
		}
	}
}

func (r *agentsMDReader) resolve(dir, token string) (string, bool) {
	resolved := token
	switch {
	case strings.HasPrefix(token, "~/"):
		if r.home == "" {
			return "", false
		}
		resolved = execenv.JoinTargetPath(r.home, filepath.ToSlash(token[2:]))
	case !execenv.IsAbsPath(token):
		resolved = execenv.JoinTargetPath(dir, filepath.ToSlash(token))
	}
	return r.regularFile(resolved)
}

// importTokens returns the paths of the `@path` tokens in text, in order. A token starts a line
// or follows whitespace, and runs to the next whitespace. Fenced code blocks and inline code
// spans hold no tokens.
func importTokens(text string) []string {
	var tokens []string
	fence := ""
	for line := range strings.Lines(text) {
		line = strings.TrimRight(line, "\n")
		if trimmed := strings.TrimLeft(line, " "); len(line)-len(trimmed) <= 3 {
			if mark := fenceMark(trimmed); mark != "" {
				closes := fence != "" && mark[0] == fence[0] && len(mark) >= len(fence) && strings.TrimSpace(trimmed[len(mark):]) == ""
				switch {
				case fence == "":
					fence = mark
				case closes:
					fence = ""
				}
				continue
			}
		}
		if fence != "" {
			continue
		}
		masked := maskCodeSpans(line)
		for i := 0; i < len(masked); i++ {
			if masked[i] != '@' || (i > 0 && !isSpace(masked[i-1])) {
				continue
			}
			j := i + 1
			for j < len(masked) && !isSpace(masked[j]) {
				j++
			}
			// A token that touches a code span is not a plain token.
			if j > i+1 && masked[i+1:j] == line[i+1:j] {
				tokens = append(tokens, line[i+1:j])
			}
			i = j
		}
	}
	return tokens
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' }

// fenceMark returns the run of three or more backticks or tildes that starts s.
func fenceMark(s string) string {
	if s == "" || (s[0] != '`' && s[0] != '~') {
		return ""
	}
	n := 0
	for n < len(s) && s[n] == s[0] {
		n++
	}
	if n < 3 {
		return ""
	}
	return s[:n]
}

// maskCodeSpans replaces each inline code span with x, which is not whitespace, so no token
// starts right after a span. The result has the length of line.
func maskCodeSpans(line string) string {
	b := []byte(line)
	for i := 0; i < len(b); {
		if b[i] != '`' {
			i++
			continue
		}
		j := i
		for j < len(b) && b[j] == '`' {
			j++
		}
		end := -1
		for k := j; k < len(b); {
			if b[k] != '`' {
				k++
				continue
			}
			m := k
			for m < len(b) && b[m] == '`' {
				m++
			}
			if m-k == j-i {
				end = m
				break
			}
			k = m
		}
		if end < 0 {
			i = j
			continue
		}
		for k := i; k < end; k++ {
			b[k] = 'x'
		}
		i = end
	}
	return string(b)
}

func (a *agentExtensions) instructionsContext(ctx context.Context, cwd, own string) string {
	files := a.agentsMDContext(ctx, cwd)
	var parts []string
	for _, md := range files[:len(files)-1] {
		if md.text != "" {
			parts = append(parts, "AGENTS.md of the "+md.scope+" level ("+md.path+"):\n\n"+md.text)
		}
	}
	if own != "" {
		parts = append(parts, own)
	}
	if project := files[len(files)-1]; project.text != "" {
		parts = append(parts, "AGENTS.md of the project ("+project.path+"):\n\n"+project.text)
	}
	return strings.Join(parts, "\n\n---\n\n")
}

// agentsMDContext reads AGENTS.md at the dex level, the agent level, and the project (cwd), in this order.
func (a *agentExtensions) agentsMDContext(ctx context.Context, cwd string) []agentsMD {
	if cwd == "" {
		cwd = a.workspace
	}
	var out []agentsMD
	for _, l := range a.distinct() {
		md := readAgentsMDLocal(ctx, filepath.Join(l.Dir, "AGENTS.md"), a.home, "")
		md.scope = l.Scope
		out = append(out, md)
	}
	var project agentsMD
	if a.isRemote() {
		if env, err := a.execHandle.Env(ctx, cwd, nil); err == nil {
			if canonCwd, err := env.CanonicalPath(ctx, cwd); err == nil {
				remoteHome, _ := a.execHandle.Home(ctx)
				project = readAgentsMDEnv(ctx, env, execenv.JoinTargetPath(cwd, "AGENTS.md"), remoteHome, canonCwd)
			}
		}
	} else {
		project = readAgentsMDLocal(ctx, filepath.Join(cwd, "AGENTS.md"), a.home, canonicalExtensionPath(cwd))
	}
	project.scope = "project"
	return append(out, project)
}
