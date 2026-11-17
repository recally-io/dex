package execenv

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	maxSSHIncludeDepth = 8
	maxSSHIncludeFiles = 64
)

// SSHHostEntry describes one concrete Host alias discovered from ~/.ssh/config.
type SSHHostEntry struct {
	Alias        string `json:"alias"`
	HostName     string `json:"hostName,omitempty"`
	User         string `json:"user,omitempty"`
	Port         int    `json:"port,omitzero"`
	IdentityFile string `json:"identityFile,omitempty"`
	ProxyJump    string `json:"proxyJump,omitempty"`
	Source       string `json:"source,omitempty"`
}

// SSHConfigDiscovery holds concrete host aliases from ~/.ssh/config and candidate private key paths under ~/.ssh/.
type SSHConfigDiscovery struct {
	Hosts         []SSHHostEntry `json:"hosts"`
	IdentityFiles []string       `json:"identityFiles"`
}

type sshConfigBlock struct {
	patterns     []string
	source       string
	hostName     string
	user         string
	port         int
	identityFile string
	proxyJump    string
}

var ignoredGitForgeHosts = map[string]struct{}{
	"github.com":              {},
	"gist.github.com":         {},
	"ssh.github.com":          {},
	"gitlab.com":              {},
	"bitbucket.org":           {},
	"ssh.dev.azure.com":       {},
	"vs-ssh.visualstudio.com": {},
	"codeberg.org":            {},
	"git.sr.ht":               {},
}

// DiscoverSSHConfig performs read-only discovery of concrete SSH host aliases and private key paths in home/.ssh.
// It never opens or reads private key file contents.
func DiscoverSSHConfig(home string) (SSHConfigDiscovery, error) {
	out := SSHConfigDiscovery{
		Hosts:         []SSHHostEntry{},
		IdentityFiles: []string{},
	}
	resolvedHome := strings.TrimSpace(home)
	if resolvedHome == "" {
		var err error
		resolvedHome, err = os.UserHomeDir()
		if err != nil {
			return out, nil //nolint:nilerr // missing home directory reports empty SSH discovery.
		}
	}
	resolvedHome = filepath.Clean(resolvedHome)
	sshDir := filepath.Join(resolvedHome, ".ssh")
	info, err := os.Stat(sshDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}
		return out, err
	}
	if !info.IsDir() {
		return out, nil
	}

	configPath := filepath.Join(sshDir, "config")
	if _, statErr := os.Stat(configPath); statErr == nil {
		visited := map[string]struct{}{}
		var blocks []sshConfigBlock
		parseSSHConfigFile(configPath, resolvedHome, sshDir, 0, visited, &blocks)
		out.Hosts = resolveDiscoveredSSHHosts(blocks, resolvedHome)
	}
	out.IdentityFiles = discoverSSHIdentityFiles(sshDir)
	return out, nil
}

func parseSSHConfigFile(filePath, home, sshDir string, depth int, visited map[string]struct{}, blocks *[]sshConfigBlock) {
	if depth > maxSSHIncludeDepth || len(visited) >= maxSSHIncludeFiles {
		return
	}
	canonical := filePath
	if realPath, err := filepath.EvalSymlinks(filePath); err == nil {
		canonical = realPath
	}
	if _, seen := visited[canonical]; seen {
		return
	}
	raw, err := os.ReadFile(filePath) //nolint:gosec // G304: reads user SSH config files only, never private keys.
	if err != nil {
		return
	}
	visited[canonical] = struct{}{}

	sourceLabel := abbreviateHomePath(filePath, home)
	current := &sshConfigBlock{
		patterns: []string{"*"},
		source:   sourceLabel,
	}
	hasDirectives := false
	inMatch := false

	flushCurrent := func() {
		if !inMatch && hasDirectives {
			*blocks = append(*blocks, *current)
		}
	}

	for rawLine := range strings.SplitSeq(string(raw), "\n") {
		tokens := tokenizeSSHConfigLine(rawLine)
		if len(tokens) == 0 {
			continue
		}
		key := strings.ToLower(tokens[0])
		args := tokens[1:]

		switch key {
		case "host":
			flushCurrent()
			inMatch = false
			hasDirectives = len(args) > 0
			current = &sshConfigBlock{
				patterns: slices.Clone(args),
				source:   sourceLabel,
			}
		case "match":
			flushCurrent()
			inMatch = true
			hasDirectives = false
		case "include":
			if inMatch {
				continue
			}
			flushCurrent()
			hasDirectives = false
			current = &sshConfigBlock{
				patterns: []string{"*"},
				source:   sourceLabel,
			}
			for _, pat := range args {
				for _, incPath := range expandSSHIncludePattern(pat, home, sshDir) {
					parseSSHConfigFile(incPath, home, sshDir, depth+1, visited, blocks)
				}
			}
		case "hostname":
			if !inMatch && len(args) > 0 && current.hostName == "" {
				current.hostName = args[0]
				hasDirectives = true
			}
		case "user":
			if !inMatch && len(args) > 0 && current.user == "" {
				current.user = args[0]
				hasDirectives = true
			}
		case "port":
			if !inMatch && len(args) > 0 && current.port == 0 {
				if p, convErr := strconv.Atoi(args[0]); convErr == nil && p >= 1 && p <= 65535 {
					current.port = p
					hasDirectives = true
				}
			}
		case "identityfile":
			if !inMatch && len(args) > 0 && current.identityFile == "" {
				current.identityFile = args[0]
				hasDirectives = true
			}
		case "proxyjump":
			if !inMatch && len(args) > 0 && current.proxyJump == "" {
				current.proxyJump = strings.Join(args, ",")
				hasDirectives = true
			}
		}
	}
	flushCurrent()
}

func tokenizeSSHConfigLine(line string) []string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return nil
	}

	var tokens []string
	var cur strings.Builder
	inQuote := false
	tokenStarted := false
	keyDone := false

	flush := func() {
		if tokenStarted {
			tokens = append(tokens, cur.String())
			cur.Reset()
			tokenStarted = false
			keyDone = true
		}
	}

	for i := 0; i < len(trimmed); i++ {
		ch := trimmed[i]
		if inQuote {
			if ch == '"' {
				inQuote = false
			} else {
				cur.WriteByte(ch)
			}
			continue
		}
		switch {
		case ch == '"':
			inQuote = true
			tokenStarted = true
		case ch == '#' && !tokenStarted:
			return tokens
		case ch == ' ' || ch == '\t' || ch == '\r':
			flush()
		case ch == '=' && !keyDone:
			flush()
		default:
			cur.WriteByte(ch)
			tokenStarted = true
		}
	}
	flush()
	return tokens
}

func expandSSHIncludePattern(pattern, home, sshDir string) []string {
	p := strings.TrimSpace(pattern)
	if p == "" {
		return nil
	}
	if p == "~" {
		p = home
	} else if rest, ok := strings.CutPrefix(p, "~/"); ok {
		p = filepath.Join(home, filepath.FromSlash(rest))
	} else if !filepath.IsAbs(p) {
		p = filepath.Join(sshDir, filepath.FromSlash(p))
	}
	matches, err := filepath.Glob(p)
	if err != nil {
		return nil
	}
	slices.Sort(matches)
	return matches
}

func resolveDiscoveredSSHHosts(blocks []sshConfigBlock, home string) []SSHHostEntry {
	type aliasOrigin struct {
		alias  string
		source string
	}
	var concrete []aliasOrigin
	seen := map[string]struct{}{}
	for _, b := range blocks {
		for _, pat := range b.patterns {
			if !isConcreteSSHAlias(pat) {
				continue
			}
			if _, ok := seen[pat]; ok {
				continue
			}
			seen[pat] = struct{}{}
			concrete = append(concrete, aliasOrigin{alias: pat, source: b.source})
		}
	}

	entries := make([]SSHHostEntry, 0, len(concrete))
	for _, item := range concrete {
		entry := SSHHostEntry{
			Alias:  item.alias,
			Source: item.source,
		}
		for _, b := range blocks {
			if !sshPatternsMatch(b.patterns, item.alias) {
				continue
			}
			if entry.HostName == "" && b.hostName != "" {
				entry.HostName = strings.ReplaceAll(b.hostName, "%h", item.alias)
			}
			if entry.User == "" && b.user != "" {
				entry.User = b.user
			}
			if entry.Port == 0 && b.port > 0 {
				entry.Port = b.port
			}
			if entry.IdentityFile == "" && b.identityFile != "" && !strings.EqualFold(b.identityFile, "none") {
				entry.IdentityFile = abbreviateHomePath(b.identityFile, home)
			}
			if entry.ProxyJump == "" && b.proxyJump != "" && !strings.EqualFold(b.proxyJump, "none") {
				entry.ProxyJump = b.proxyJump
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

func isConcreteSSHAlias(pat string) bool {
	if pat == "" || strings.HasPrefix(pat, "-") || strings.HasPrefix(pat, "!") {
		return false
	}
	if strings.ContainsAny(pat, "*? \t\r\n") {
		return false
	}
	_, ignored := ignoredGitForgeHosts[strings.ToLower(pat)]
	return !ignored
}

func sshPatternsMatch(patterns []string, alias string) bool {
	positive := false
	for _, pat := range patterns {
		if pat == "" {
			continue
		}
		if neg, ok := strings.CutPrefix(pat, "!"); ok {
			if matched, err := path.Match(neg, alias); err == nil && matched {
				return false
			}
			continue
		}
		if matched, err := path.Match(pat, alias); err == nil && matched {
			positive = true
		}
	}
	return positive
}

func abbreviateHomePath(p, home string) string {
	clean := filepath.Clean(p)
	if home == "" {
		return p
	}
	if clean == home {
		return "~"
	}
	if rel, ok := strings.CutPrefix(clean, home+string(filepath.Separator)); ok {
		return "~/" + filepath.ToSlash(rel)
	}
	return p
}

func discoverSSHIdentityFiles(sshDir string) []string {
	entries, err := os.ReadDir(sshDir)
	if err != nil {
		return []string{}
	}
	out := []string{}
	for _, entry := range entries {
		name := entry.Name()
		if !isCandidateIdentityFilename(name) {
			continue
		}
		info, statErr := os.Stat(filepath.Join(sshDir, name))
		if statErr != nil || !info.Mode().IsRegular() {
			continue
		}
		out = append(out, "~/.ssh/"+name)
	}
	slices.Sort(out)
	return out
}

func isCandidateIdentityFilename(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") {
		return false
	}
	lower := strings.ToLower(name)
	for _, prefix := range []string{"config", "known_hosts", "authorized_keys", "agent", "control", "cm-"} {
		if strings.HasPrefix(lower, prefix) {
			return false
		}
	}
	switch lower {
	case "environment", "rc", "id_rsa.pub", "id_ed25519.pub", "id_ecdsa.pub", "id_dsa.pub":
		return false
	}
	for _, ext := range []string{".pub", ".old", ".bak", ".sock", ".log", ".json", ".toml", ".plist", ".conf", ".md", ".txt"} {
		if strings.HasSuffix(lower, ext) {
			return false
		}
	}
	return true
}
