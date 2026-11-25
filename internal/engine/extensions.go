package engine

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
	durableenv "github.com/MichaelKinsy/PiG/durable/env"
	"github.com/MichaelKinsy/PiG/mcp"
	"github.com/recally-io/dex/internal/execenv"
	"github.com/recally-io/dex/internal/vault"
	"go.yaml.in/yaml/v3"
)

type ExtensionLevel struct {
	Scope string `json:"scope"`
	Dir   string `json:"dir"`
}
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Scope       string `json:"scope"`
	Path        string `json:"path"`
	Overrides   bool   `json:"overrides"`
	Error       string `json:"error,omitempty"`
}
type McpServerInfo struct {
	Name            string              `json:"name"`
	Scope           string              `json:"scope"`
	Overrides       bool                `json:"overrides"`
	Transport       string              `json:"transport,omitempty"`
	State           string              `json:"state"`
	Tools           []string            `json:"tools"`
	Error           string              `json:"error,omitempty"`
	OAuth           string              `json:"oauth,omitempty"`
	VaultReferences *mcpVaultReferences `json:"vaultReferences,omitempty"`
}
type ExtensionInfo struct {
	Levels   []ExtensionLevel `json:"levels"`
	Skills   []SkillInfo      `json:"skills"`
	Servers  []McpServerInfo  `json:"servers"`
	Warnings []string         `json:"warnings"`
	Errors   []string         `json:"errors"`
	// ReloadPending reports changed files or settings that wait for all agents to be idle.
	ReloadPending bool `json:"reloadPending"`
}
type extensionValidationError struct{ Message string }

func (e *extensionValidationError) Error() string { return e.Message }
func (e *extensionValidationError) Unwrap() error { return ErrInvalidInput }

type agentExtensions struct {
	levels              []ExtensionLevel
	workspace, agentDir string
	agentID             string
	mcp                 *MCPStore
	vault               *vault.Store
	// home only expands ~/ in AGENTS.md imports; it is not an extension level.
	home            string
	env             *toolEnv
	execHandle      execenv.Handle
	stores          map[string]*skillStore
	onSkillChange   func(scope string)
	skillsMu        sync.Mutex
	skillStamp      string
	skillConnection any
	// sessionPrompts requires the harness to supply the bound target, never a default fallback.
	sessionPrompts     bool
	defaultUnavailable bool
}

// sessionPromptEnv keeps the skill mirror and the prompt's file client on the
// same lease even if another request replaces an expired sandbox.
type sessionPromptEnv struct {
	durableenv.ExecutionEnv
	files        *agentExtensions
	agentID      string
	conversation durable.ConversationId
}

type loadedExtensions struct {
	Extension   *durable.Extension
	Env         map[string]string
	ExplicitEnv map[string]string
	Vault       vault.Snapshot
	Servers     []McpServerInfo
	clients     []*mcp.Client
	mu          sync.Mutex
	// stamp is the file and settings stamp before the load; a different stamp means stale.
	stamp string
}

func (l *loadedExtensions) serverInfo() []McpServerInfo {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.Servers)
}
func (l *loadedExtensions) Close(context.Context) error {
	var err error
	for _, c := range l.clients {
		err = errors.Join(err, c.Close())
	}
	return err
}

type serverConfig struct {
	Type     string            `json:"type"`
	Command  string            `json:"command"`
	Args     []string          `json:"args"`
	Env      map[string]string `json:"env"`
	Cwd      string            `json:"cwd"`
	URL      string            `json:"url"`
	Headers  map[string]string `json:"headers"`
	Disabled bool              `json:"disabled"`
	OAuth    *mcpOAuthConfig   `json:"oauth,omitempty"`
	OAuthID  string            `json:"oauthId,omitempty"`
}
type serverEntry struct {
	info    McpServerInfo
	config  serverConfig
	scopeID string
}
type extensionScan struct {
	info    ExtensionInfo
	env     map[string]string
	servers []serverEntry
}

var serverName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
var toolName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var headerVar = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func newAgentExtensions(home, dataDir, agentID string, handle execenv.Handle, env *toolEnv) *agentExtensions {
	agentDir := filepath.Join(dataDir, "agents", agentID)
	workspace := filepath.Join(agentDir, "workspace")
	if handle != nil {
		workspace = handle.Workspace()
	}
	a := &agentExtensions{env: env, home: home, agentID: agentID, agentDir: agentDir, vault: vault.New(dataDir), workspace: workspace, execHandle: handle, stores: map[string]*skillStore{}}
	for _, l := range []ExtensionLevel{{"dex", dataDir}, {"agent", agentDir}} {
		l.Dir = canonicalExtensionPath(filepath.Join(l.Dir, ".agents"))
		a.levels = append(a.levels, l)
		a.stores[l.Scope] = &skillStore{}
	}
	return a
}

func (a *agentExtensions) forHandle(h execenv.Handle) *agentExtensions {
	return &agentExtensions{levels: a.levels, agentID: a.agentID, agentDir: a.agentDir, mcp: a.mcp, vault: a.vault, home: a.home, env: a.env, workspace: h.Workspace(), execHandle: h, stores: a.stores}
}

func (a *agentExtensions) isRemote() bool {
	return a.execHandle != nil && a.execHandle.Kind() != execenv.KindLocal
}

func (a *agentExtensions) remoteSkillsRoot() string {
	return execenv.JoinTargetPath(a.workspace, ".dex", "skills")
}
func canonicalExtensionPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	parent := filepath.Dir(path)
	if parent == path {
		return path
	}
	return filepath.Join(canonicalExtensionPath(parent), filepath.Base(path))
}
func (a *agentExtensions) distinct() []ExtensionLevel { return distinctLevels(a.levels) }

// distinctLevels keeps the narrowest of levels with the same directory.
func distinctLevels(levels []ExtensionLevel) []ExtensionLevel {
	out := []ExtensionLevel{}
	for i, l := range levels {
		if !slices.ContainsFunc(levels[i+1:], func(next ExtensionLevel) bool { return next.Dir == l.Dir }) {
			out = append(out, l)
		}
	}
	return out
}

// skillsContext reads skills from the project's cwd/.agents/skills and, when
// available, from the dex and agent stores.
func (a *agentExtensions) skillsContext(ctx context.Context, remoteEnv durableenv.ExecutionEnv, cwd string, errs *[]string, includeManaged bool) []SkillInfo {
	if cwd == "" {
		cwd = a.workspace
	}
	projectDir := canonicalExtensionPath(filepath.Join(cwd, ".agents"))
	if a.isRemote() {
		projectDir = execenv.JoinTargetPath(cwd, ".agents")
		if remoteEnv != nil {
			if canon, err := remoteEnv.CanonicalPath(ctx, projectDir); err == nil {
				projectDir = canon
			}
		}
	}
	levels := []ExtensionLevel{{"project", projectDir}}
	byName := map[string]SkillInfo{}
	if includeManaged {
		managed, warnings := a.managedSkills()
		*errs = append(*errs, warnings...)
		for _, skill := range managed {
			byName[skill.Name] = skill
		}
	}
	for _, l := range distinctLevels(levels) {
		var dir string
		var found []SkillInfo
		if l.Scope == "project" && a.isRemote() {
			env := remoteEnv
			if env == nil {
				var err error
				env, err = a.execHandle.Env(ctx, cwd, nil)
				if err != nil {
					*errs = append(*errs, l.Dir+": cannot open execution environment for skills")
					continue
				}
			}
			dir = execenv.JoinTargetPath(l.Dir, "skills")
			info, err := execenv.StatFollow(ctx, env, dir)
			if err != nil {
				if fe, ok := errors.AsType[*durableenv.FileError](err); !ok || fe.Code != durableenv.FileErrorNotFound {
					*errs = append(*errs, dir+": cannot read skills")
				}
				continue
			}
			if info.Kind != durableenv.FileKindDirectory {
				continue
			}
			found = findSkillsRemote(ctx, env, dir, map[string]bool{}, errs)
		} else {
			dir = filepath.Join(l.Dir, "skills")
			if _, err := os.Stat(dir); err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					*errs = append(*errs, dir+": cannot read skills")
				}
				continue
			}
			found = findSkills(dir, map[string]bool{}, errs)
		}
		level := map[string]bool{}
		for _, skill := range found {
			if level[skill.Name] {
				*errs = append(*errs, skill.Path+": another skill in "+dir+" has the name "+skill.Name)
				continue
			}
			level[skill.Name] = true
			skill.Scope = l.Scope
			_, skill.Overrides = byName[skill.Name]
			byName[skill.Name] = skill
		}
	}
	out := []SkillInfo{}
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		out = append(out, byName[name])
	}
	return out
}

func findSkillsRemote(ctx context.Context, env durableenv.ExecutionEnv, dir string, seen map[string]bool, errs *[]string) []SkillInfo {
	resolved, err := env.CanonicalPath(ctx, dir)
	if err != nil || seen[resolved] {
		return nil
	}
	seen[resolved] = true
	skillPath := execenv.JoinTargetPath(dir, "SKILL.md")
	if info, err := execenv.StatFollow(ctx, env, skillPath); err == nil && info.Kind == durableenv.FileKindFile {
		name := filepath.Base(dir)
		if strings.HasPrefix(dir, "/") {
			name = path.Base(dir)
		}
		skill := SkillInfo{Name: name, Path: skillPath}
		data, readErr := env.ReadTextFile(ctx, skillPath)
		if readErr != nil {
			skill.Error = "cannot read SKILL.md"
		} else {
			skill.Name, skill.Description, skill.Error = parseSkill(data, skill.Name)
		}
		return []SkillInfo{skill}
	}
	entries, err := env.ListDir(ctx, dir)
	if err != nil {
		*errs = append(*errs, dir+": cannot read skills")
		return nil
	}
	var out []SkillInfo
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name, ".") || entry.Name == "node_modules" {
			continue
		}
		isDir := entry.Kind == durableenv.FileKindDirectory
		if entry.Kind == durableenv.FileKindSymlink {
			target, statErr := execenv.StatFollow(ctx, env, entry.Path)
			isDir = statErr == nil && target.Kind == durableenv.FileKindDirectory
		}
		if isDir {
			out = append(out, findSkillsRemote(ctx, env, entry.Path, seen, errs)...)
		}
	}
	return out
}

// findSkills walks dir as Pi's skill loader does. A directory with a SKILL.md file is one skill,
// and the walk does not go into it. Hidden directories and node_modules are skipped. seen holds
// real paths, so a symlinked directory is read once and a link cycle ends.
func findSkills(dir string, seen map[string]bool, errs *[]string) []SkillInfo {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || seen[resolved] {
		return nil
	}
	seen[resolved] = true
	path := filepath.Join(dir, "SKILL.md")
	if stat, err := os.Stat(path); err == nil && stat.Mode().IsRegular() {
		skill := SkillInfo{Name: filepath.Base(dir), Path: path}
		data, err := os.ReadFile(path) //nolint:gosec // A SKILL.md under an extension level, found by the walk.
		if err != nil {
			skill.Error = "cannot read SKILL.md"
		} else {
			skill.Name, skill.Description, skill.Error = parseSkill(string(data), skill.Name)
		}
		return []SkillInfo{skill}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		*errs = append(*errs, dir+": cannot read skills")
		return nil
	}
	var out []SkillInfo
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") || entry.Name() == "node_modules" {
			continue
		}
		child := filepath.Join(dir, entry.Name())
		if stat, err := os.Stat(child); err == nil && stat.IsDir() {
			out = append(out, findSkills(child, seen, errs)...)
		}
	}
	return out
}

// parseSkill returns the name, the description and an error. The front matter name wins over
// the directory name, so a nested skill keeps its public name.
func parseSkill(text, dirName string) (string, string, string) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return dirName, "", "SKILL.md must start with YAML front matter between --- lines"
	}
	front, rest, ok := strings.Cut(text[4:], "\n---")
	if !ok || (rest != "" && !strings.HasPrefix(rest, "\n")) {
		return dirName, "", "SKILL.md must start with YAML front matter between --- lines"
	}
	var meta map[string]any
	if err := yaml.Unmarshal([]byte(front), &meta); err != nil {
		return dirName, "", "the front matter is not valid YAML"
	}
	name := dirName
	if declared, present := meta["name"]; present {
		value, ok := declared.(string)
		if !ok {
			return dirName, "", "the front matter needs a name that is text"
		}
		if value = strings.TrimSpace(value); value != "" {
			name = value
		}
	}
	description, ok := meta["description"].(string)
	if !ok || strings.TrimSpace(description) == "" || utf8.RuneCountInString(description) > 1024 {
		return name, "", "the front matter needs a description of 1 to 1024 characters"
	}
	return name, strings.Join(strings.Fields(description), " "), ""
}

// stamp includes the revisions of the two MCP declaration levels.
func (a *agentExtensions) stamp() string {
	return a.mcp.stamp(a.agentID) + a.env.stamp() + fileStamp(slices.Values(a.vault.Paths(a.agentID)))
}
func (a *agentExtensions) scan(ctx context.Context, base map[string]string, remoteEnv durableenv.ExecutionEnv) extensionScan {
	s := extensionScan{
		info: ExtensionInfo{Levels: a.levels, Skills: []SkillInfo{}, Servers: []McpServerInfo{}, Warnings: []string{}, Errors: []string{}},
		env:  maps.Clone(base),
	}
	if s.env == nil {
		s.env = map[string]string{}
	}
	if !a.defaultUnavailable {
		s.info.Skills = a.skillsContext(ctx, remoteEnv, a.workspace, &s.info.Warnings, true)
	}
	servers, mcpErr := a.mcp.list(ctx, a.agentID)
	if mcpErr != nil {
		s.info.Errors = append(s.info.Errors, "cannot load MCP declarations")
	}
	if mcpErr == nil {
		s.servers = servers
		for _, entry := range servers {
			entry.info.OAuth = entry.oauthStatus(a.vault)
			s.info.Servers = append(s.info.Servers, entry.info)
		}
	}
	s.info.Warnings = append(s.info.Warnings, a.agentsMDContext(ctx, "").warnings...)
	path := filepath.Join(a.agentDir, "skills")
	if _, err := os.Lstat(path); err == nil {
		s.info.Warnings = append(s.info.Warnings, path+": dex no longer reads this; move it to "+filepath.Join(a.agentDir, ".agents", "skills"))
	}
	return s
}
func (a *agentExtensions) Info(ctx context.Context) (ExtensionInfo, error) {
	if ctx.Err() != nil {
		return ExtensionInfo{}, context.Cause(ctx)
	}
	var remoteEnv durableenv.ExecutionEnv
	if a.isRemote() {
		var err error
		remoteEnv, err = a.execHandle.Env(ctx, a.workspace, nil)
		if err != nil {
			return ExtensionInfo{}, err
		}
	}
	base, op, warning := a.env.snapshot()
	s := a.scan(ctx, base, remoteEnv)
	if warning != "" {
		s.info.Warnings = append(s.info.Warnings, warning)
	}
	op.apply(s.env)
	if op.Mode != "" && findOp(s.env) == "" {
		s.info.Warnings = append(s.info.Warnings, "1Password is set up, but the op CLI is not in the PATH of the tool environment; install it or add its directory to PATH in a shell rc file")
	}
	return s.info, nil
}

// Validate reads configuration without changing the loaded environment or MCP connections.
func (a *agentExtensions) Validate(ctx context.Context) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if _, err := a.vault.Load(a.agentID); err != nil {
		return fmt.Errorf("%w: cannot open vault: %w", ErrInvalidInput, err)
	}
	base, _, _ := a.env.snapshot()
	scan := a.scan(ctx, base, nil)
	if len(scan.info.Errors) > 0 {
		return &extensionValidationError{Message: strings.Join(scan.info.Errors, "\n")}
	}
	return nil
}
func (a *agentExtensions) Load(ctx context.Context) (*loadedExtensions, error) {
	// Take the stamp first, so that a change during the load makes the agent stale.
	stamp := a.stamp()
	var remoteEnv durableenv.ExecutionEnv
	if a.isRemote() {
		var err error
		remoteEnv, err = a.execHandle.Env(ctx, a.workspace, nil)
		if err != nil {
			return nil, err
		}
	}
	secrets, err := a.vault.Load(a.agentID)
	if err != nil {
		return nil, fmt.Errorf("load vault: %w", err)
	}
	base, op := a.env.load(ctx)
	s := a.scan(ctx, base, remoteEnv)
	op.apply(s.env)
	explicit := map[string]string{}
	op.apply(explicit)
	// Keep the status slice stable before any connection can call OnClose.
	loaded := &loadedExtensions{Env: s.env, ExplicitEnv: explicit, Vault: secrets, Servers: s.info.Servers, Extension: &durable.Extension{Name: "extensions"}, stamp: stamp}
	opPath := findOp(s.env)
	guide := extensionGuide(a.levels, secretsGuide(op, opPath))
	loaded.Extension.Sections = []*durable.PromptSection{{Key: "extension-guide", Render: func(context.Context, durable.PromptInput) (*string, error) { return new(guide), nil }}, {Key: "skills", Render: func(renderCtx context.Context, input durable.PromptInput) (*string, error) {
		target := a
		if a.sessionPrompts {
			bound, ok := input.Env.(*sessionPromptEnv)
			if !ok {
				return nil, nil
			}
			target = bound.files
		}
		cwd := target.workspace
		if input.Env != nil {
			cwd = input.Env.Cwd()
		}
		synced := target.trySyncSkills(renderCtx, input.Env, true)
		errs := []string{}
		return skillsText(target.skillsContext(renderCtx, input.Env, cwd, &errs, synced)), nil
	}}}
	loaded.Extension.Sections = append(loaded.Extension.Sections, &durable.PromptSection{Key: "project", Render: func(_ context.Context, input durable.PromptInput) (*string, error) {
		if a.sessionPrompts && input.Env == nil {
			return nil, nil
		}
		cwd := a.workspace
		if input.Env != nil {
			cwd = input.Env.Cwd()
		}
		return new(fmt.Sprintf("Current project directory: %q. Main and Chat sessions can call set_project to change or clear the project directory.", cwd)), nil
	}})
	names := map[string]bool{}
	for index, entry := range s.servers {
		if ctx.Err() != nil {
			_ = loaded.Close(ctx)
			return nil, context.Cause(ctx)
		}
		if entry.config.Disabled {
			continue
		}
		client, tools, err := a.connect(ctx, entry, s.env, secrets)
		if err != nil {
			loaded.Servers[index].State = "failed"
			loaded.Servers[index].Error = mcpFailure(err)
			continue
		}
		loaded.clients = append(loaded.clients, client)
		loaded.Servers[index].State = "connected"
		lost := []string{}
		for _, tool := range tools {
			loaded.Servers[index].Tools = append(loaded.Servers[index].Tools, strings.TrimPrefix(tool.Name, "mcp__"+entry.info.Name+"__"))
			if names[tool.Name] {
				lost = append(lost, tool.Name)
				continue
			}
			names[tool.Name] = true
			loaded.Extension.Tools = append(loaded.Extension.Tools, tool)
		}
		slices.Sort(loaded.Servers[index].Tools)
		if len(lost) > 0 {
			loaded.Servers[index].Error = "tools with a name that another tool uses: " + strings.Join(lost, ", ")
		}
		client.OnClose(func() {
			loaded.mu.Lock()
			defer loaded.mu.Unlock()
			loaded.Servers[index].State = "failed"
			loaded.Servers[index].Error = "the server closed the connection; reload to connect again"
		})
	}
	return loaded, nil
}
func (a *agentExtensions) connect(ctx context.Context, entry serverEntry, env map[string]string, secrets vault.Snapshot) (*mcp.Client, []*durable.ToolRegistration, error) {
	c := entry.config
	client := mcp.NewClient(mcp.ClientOptions{Name: "dex", Version: "1", RequestTimeoutMs: 30000})
	var transport mcp.Transport
	var auth *mcpOAuthConnection
	switch {
	case c.OAuth != nil:
		if err := validateMCPOAuth(c); err != nil {
			return nil, nil, err
		}
		auth = &mcpOAuthConnection{entry: entry, vault: a.vault, store: a.mcp}
		t, err := mcp.NewStreamableHTTPTransport(mcp.StreamableHTTPTransportOptions{URL: c.URL, Fetch: auth})
		if err != nil {
			return nil, nil, &privateError{message: "invalid MCP URL", cause: err}
		}
		transport = t
	case c.Type == "http":
		headers := maps.Clone(c.Headers)
		missing := false
		for key, value := range headers {
			headers[key] = headerVar.ReplaceAllStringFunc(value, func(match string) string {
				name := match[2 : len(match)-1]
				v, ok := env[name]
				if !ok {
					missing = true
				}
				return v
			})
		}
		if missing {
			return nil, nil, fmt.Errorf("missing header environment key")
		}
		headers, err := secrets.Resolve(headers, true)
		if err != nil {
			return nil, nil, err
		}
		t, err := mcp.NewStreamableHTTPTransport(mcp.StreamableHTTPTransportOptions{URL: c.URL, Headers: headers})
		if err != nil {
			return nil, nil, &privateError{message: "invalid MCP URL", cause: err}
		}
		transport = t
	default:
		vars := maps.Clone(env)
		if vars == nil {
			vars = map[string]string{}
		}
		declared, err := secrets.Resolve(c.Env, true)
		if err != nil {
			return nil, nil, err
		}
		maps.Copy(vars, declared)
		cwd := c.Cwd
		if !filepath.IsAbs(cwd) {
			workspace := filepath.Join(a.agentDir, "workspace")
			if err := os.MkdirAll(workspace, 0o700); err != nil {
				return nil, nil, &privateError{message: "cannot create the control workspace", cause: err}
			}
			cwd = filepath.Join(workspace, cwd)
		}
		transport = mcp.NewStdioTransport(mcp.StdioTransportOptions{Command: c.Command, Args: c.Args, Cwd: cwd, Env: vars, InheritEnv: new(false), Stderr: "pipe"})
	}
	if _, err := client.Connect(ctx, transport); err != nil {
		_ = client.Close()
		return nil, nil, err
	}
	listed, err := client.ListTools(ctx, mcp.RequestOptions{})
	if err != nil {
		_ = client.Close()
		return nil, nil, err
	}
	tools := []*durable.ToolRegistration{}
	for _, tool := range listed {
		if auth != nil && auth.mask(tool.Name) != tool.Name {
			continue
		}
		name := "mcp__" + entry.info.Name + "__" + tool.Name
		if !toolName.MatchString(name) {
			continue
		}
		schema := map[string]any{}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			_ = client.Close()
			return nil, nil, &privateError{message: "invalid MCP tool schema", cause: err}
		}
		if schema == nil {
			schema = map[string]any{}
		}
		schema["type"] = "object"
		if schema["properties"] == nil {
			schema["properties"] = map[string]any{}
		}
		// Some servers send "required": null, and providers reject the whole request.
		if schema["required"] == nil {
			delete(schema, "required")
		}
		description := tool.Description
		if auth != nil {
			description = auth.mask(description)
			schema = auth.maskJSON(schema).(map[string]any) //nolint:forcetypeassert // The mask preserves JSON value types.
		}
		if description == "" {
			description = tool.Name
		}
		tools = append(tools, &durable.ToolRegistration{Name: name, Description: description, Parameters: schema, Deferred: true, Execute: func(ctx context.Context, args any, _ durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
			result, err := client.CallTool(ctx, tool.Name, args, mcp.RequestOptions{TimeoutMs: 600000})
			if err != nil {
				if errors.Is(err, errMCPAuthorize) || errors.Is(err, errMCPReauthorize) {
					return durable.ToolExecutionResult{}, errMCPReauthorize
				}
				return durable.ToolExecutionResult{}, &privateError{message: "MCP tool call failed", cause: err}
			}
			output := []ai.ToolResultMessageContent{}
			for _, block := range mcp.ToLLMContent(*result) {
				if block.Type == "image" {
					if auth == nil || (auth.mask(block.Data) == block.Data && auth.mask(block.MimeType) == block.MimeType) {
						output = append(output, ai.ImageContent{Data: block.Data, MimeType: block.MimeType})
					}
				} else {
					if auth != nil {
						block.Text = auth.mask(block.Text)
					}
					output = append(output, ai.TextContent{Text: block.Text})
				}
			}
			return durable.ToolExecutionResult{Output: output, IsError: result.IsError}, nil
		}})
	}
	return client, tools, nil
}
func mcpFailure(err error) string {
	if errors.Is(err, errMCPAuthorize) {
		return errMCPAuthorize.Error()
	}
	if errors.Is(err, errMCPReauthorize) {
		return errMCPReauthorize.Error()
	}
	if e, ok := errors.AsType[*vault.MissingError](err); ok {
		return e.Error()
	}
	if e, ok := errors.AsType[*mcp.McpHttpError](err); ok {
		return fmt.Sprintf("the server answered with HTTP status %d", e.Status)
	}
	if err.Error() == "missing header environment key" {
		return "a header environment key is missing"
	}
	return "the connection failed"
}
func skillsText(skills []SkillInfo) *string {
	lines := []string{"Skills are instructions for special tasks. Read the matching SKILL.md before the task. Relative paths start at the directory of SKILL.md.", ""}
	for _, s := range skills {
		if s.Error == "" {
			lines = append(lines, "- "+s.Name+": "+s.Description+" ("+s.Path+")")
		}
	}
	if len(lines) == 2 {
		return nil
	}
	return new(strings.Join(lines, "\n"))
}
func extensionGuide(levels []ExtensionLevel, secrets string) string {
	lines := []string{"Extension levels for this bot, from wide to narrow. Each can hold skills/:"}
	for _, l := range levels {
		lines = append(lines, "- "+l.Scope+": "+l.Dir+"/")
	}
	lines = append(lines,
		"Narrower levels win. A skill or MCP server with the same name replaces the wider entry as a whole.",
		"",
		"Skills: dex and agent skills live on the control host. Change them only with skill_write, skill_edit, skill_delete, or skill_import. Use skill_list and skill_read to inspect them. Skill file paths use <dex|agent>/<name>/<file>. To install a skill, run the installer in a temporary directory, then use skill_import on the skill directory.",
		"Start SKILL.md with the YAML front matter fields name and description. The name defaults to the directory name. Project skills in <cwd>/.agents/skills override the other levels. Use the normal file tools for project skills.",
		"Run a managed skill's scripts from its directory in the session environment. Use the returned path from a skill tool, or the path in the skills list. Bash sets DEX_SKILL_CACHE to <workspace>/.dex/cache/skills. Put runtime files in $DEX_SKILL_CACHE/<name> and create that directory with mkdir -p. Remote skill copies are read-only.",
		"Keep secrets in the vault. Use secret_list to find names and pass vault:NAME references in the env argument of bash, process_start, or a monitor_set check. Ask the user to add missing values in Settings. Do not put secrets in skill files. dex commits skill history; do not run Git in the stores.",
		`MCP: use mcp_server_list and mcp_server_get to inspect declarations. Call mcp_server_set to replace one declaration at the dex or agent level. Call mcp_server_remove to delete it. For stdio, use command, args, env, and cwd. For HTTP, use "type": "http", url, and headers. {"disabled": true} turns off a wider server with the same name. Changes apply after the agent finishes its current work.`,
		`To use a JSON snippet, call mcp_server_set for each server. Ask the user to move secrets into the vault first: a pasted snippet is stored in the transcript. Use vault:NAME or ${vault:NAME} references in env and headers. Plain values are stored as plain text in control.sqlite.`,
		`For browser OAuth, set type to http and add oauth: {}. If the provider requires a registered client, set oauth.clientId and use vault:NAME in oauth.clientSecret. Ask the owner to authorize the connection in Tools. Never ask for OAuth tokens or codes.`,
		"",
		"The tool environment starts from the user's login shell environment.",
		secrets,
		"Skill and MCP tool writes and vault, 1Password, and execution environment settings changes request a reload. Each affected bot reloads when it is idle.",
		"Manual skill file and shell rc edits do not request a reload. They apply when dex restarts or another change triggers a reload. A user can also request a reload from the bot's Tools page.")
	return strings.Join(lines, "\n")
}
