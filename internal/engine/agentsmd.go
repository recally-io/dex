package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	home string
	// confine is the real project directory; empty means no limit (the agent level).
	confine  string
	seen     map[string]bool
	out      strings.Builder
	warnings []string
}

// readAgentsMD reads path and the files that it imports with `@path` tokens. A relative import
// starts at the directory of the real file, so a symlinked file works for its author. The text of
// each imported file follows the text of the file that first names it. A file is read once, which
// also ends import cycles.
func readAgentsMD(path, home, confine string) agentsMD {
	resolved, ok := regularFile(path)
	if !ok {
		return agentsMD{}
	}
	r := &agentsMDReader{home: home, confine: confine, seen: map[string]bool{}}
	if !r.allowed(resolved) {
		r.warnings = append(r.warnings, path+": the file is outside the project directory and dex skips it")
		return agentsMD{warnings: r.warnings}
	}
	r.load(resolved, 0)
	return agentsMD{text: strings.TrimSpace(r.out.String()), path: resolved, warnings: r.warnings}
}

func (r *agentsMDReader) allowed(resolved string) bool {
	if r.confine == "" {
		return true
	}
	rel, err := filepath.Rel(r.confine, resolved)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (r *agentsMDReader) load(resolved string, depth int) {
	r.seen[resolved] = true
	data, err := os.ReadFile(resolved) //nolint:gosec // A regular file that an AGENTS.md names.
	if err != nil {
		r.warnings = append(r.warnings, resolved+": cannot read the file")
		return
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if depth > 0 {
		r.out.WriteString("\n\nImported file " + resolved + ":\n\n")
	}
	r.out.WriteString(text)
	for _, token := range importTokens(text) {
		target, ok := r.resolve(filepath.Dir(resolved), token)
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

// resolve returns the real path of the regular file that token names.
func (r *agentsMDReader) resolve(dir, token string) (string, bool) {
	path := token
	switch {
	case strings.HasPrefix(token, "~/"):
		if r.home == "" {
			return "", false
		}
		path = filepath.Join(r.home, token[2:])
	case !filepath.IsAbs(token):
		path = filepath.Join(dir, token)
	}
	return regularFile(path)
}

func regularFile(path string) (string, bool) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", false
	}
	stat, err := os.Stat(resolved)
	return resolved, err == nil && stat.Mode().IsRegular()
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

// instructions returns the model instructions of an agent: the dex AGENTS.md, the agent
// AGENTS.md, the agent's own instructions, then the AGENTS.md in cwd. The most specific text comes
// last. Without any AGENTS.md the result is own. An empty cwd means the agent workspace.
func (a *agentExtensions) instructions(cwd, own string) string {
	files := a.agentsMD(cwd)
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

// agentsMD reads AGENTS.md at the dex level, the agent level, and the project (cwd), in this order.
// The last entry is always the project. Equal dex and agent directories give one file. dex never
// reads the user home. The dex and agent owners write their files, so their imports have no path
// limit. A project import stays inside the project directory.
func (a *agentExtensions) agentsMD(cwd string) []agentsMD {
	if cwd == "" {
		cwd = a.workspace
	}
	var out []agentsMD
	for _, l := range a.distinct() {
		md := readAgentsMD(filepath.Join(l.Dir, "AGENTS.md"), a.home, "")
		md.scope = l.Scope
		out = append(out, md)
	}
	project := readAgentsMD(filepath.Join(cwd, "AGENTS.md"), a.home, canonicalExtensionPath(cwd))
	project.scope = "project"
	return append(out, project)
}
