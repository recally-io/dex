package engine

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/mcp"
	"go.yaml.in/yaml/v3"
)

type Scope = string
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
	Name      string   `json:"name"`
	Scope     string   `json:"scope"`
	Overrides bool     `json:"overrides"`
	Transport string   `json:"transport,omitempty"`
	State     string   `json:"state"`
	Tools     []string `json:"tools"`
	Error     string   `json:"error,omitempty"`
}
type EnvKeyInfo struct {
	Key      string `json:"key"`
	Scope    string `json:"scope"`
	Shadowed bool   `json:"shadowed"`
}
type ExtensionInfo struct {
	Levels   []ExtensionLevel `json:"levels"`
	Skills   []SkillInfo      `json:"skills"`
	Servers  []McpServerInfo  `json:"servers"`
	Env      []EnvKeyInfo     `json:"env"`
	Warnings []string         `json:"warnings"`
	Errors   []string         `json:"errors"`
}
type EnvEditError struct{ Message string }

func (e *EnvEditError) Error() string { return e.Message }
func (e *EnvEditError) Unwrap() error { return ErrInvalidInput }

type agentExtensions struct {
	levels              []ExtensionLevel
	workspace, agentDir string
	baseEnv             map[string]string
	mu                  sync.Mutex
	loaded              *loadedExtensions
}
type loadedExtensions struct {
	Extension *durable.Extension
	Env       map[string]string
	HasTools  bool
	Servers   []McpServerInfo
	clients   []*mcp.Client
	mu        sync.Mutex
}

func (l *loadedExtensions) ServerInfo() []McpServerInfo {
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
}
type serverEntry struct {
	info   McpServerInfo
	config serverConfig
}
type extensionScan struct {
	info    ExtensionInfo
	env     map[string]string
	servers []serverEntry
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var serverName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
var toolName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var envLine = regexp.MustCompile(`^(?:export +)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(?:'([^']*)'|"([^"]*)"|([^'"]*?))(?:\s+#.*)?$`)
var headerVar = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// A shared lock keeps edits to shared dex files from losing another agent's changes.
var envWrites sync.Mutex

func newAgentExtensions(home, dataDir, agentID string, baseEnv map[string]string) *agentExtensions {
	agentDir := filepath.Join(dataDir, "agents", agentID)
	a := &agentExtensions{baseEnv: maps.Clone(baseEnv), agentDir: agentDir, workspace: filepath.Join(agentDir, "workspace")}
	for _, l := range []ExtensionLevel{{"home", home}, {"dex", dataDir}, {"agent", agentDir}} {
		l.Dir = canonicalExtensionPath(filepath.Join(l.Dir, ".agents"))
		a.levels = append(a.levels, l)
	}
	return a
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
func (a *agentExtensions) distinct() []ExtensionLevel {
	out := []ExtensionLevel{}
	for i, l := range a.levels {
		if !slices.ContainsFunc(a.levels[i+1:], func(next ExtensionLevel) bool { return next.Dir == l.Dir }) {
			out = append(out, l)
		}
	}
	return out
}
func (a *agentExtensions) skills(errs *[]string) []SkillInfo {
	byName := map[string]SkillInfo{}
	for _, l := range a.distinct() {
		dir := filepath.Join(l.Dir, "skills")
		entries, err := os.ReadDir(dir)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				*errs = append(*errs, dir+": cannot read skills")
			}
			continue
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			stat, err := os.Stat(path)
			if err != nil || !stat.IsDir() {
				continue
			}
			skill := SkillInfo{Name: entry.Name(), Scope: l.Scope, Path: filepath.Join(path, "SKILL.md")}
			_, skill.Overrides = byName[skill.Name]
			data, err := os.ReadFile(skill.Path)
			if err != nil {
				skill.Error = "cannot read SKILL.md"
			} else {
				skill.Description, skill.Error = parseSkill(string(data), skill.Name)
			}
			byName[skill.Name] = skill
		}
	}
	out := []SkillInfo{}
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		out = append(out, byName[name])
	}
	return out
}
func parseSkill(text, name string) (string, string) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return "", "SKILL.md must start with YAML front matter between --- lines"
	}
	front, rest, ok := strings.Cut(text[4:], "\n---")
	if !ok || (rest != "" && !strings.HasPrefix(rest, "\n")) {
		return "", "SKILL.md must start with YAML front matter between --- lines"
	}
	var meta map[string]any
	if err := yaml.Unmarshal([]byte(front), &meta); err != nil {
		return "", "the front matter is not valid YAML"
	}
	description, ok := meta["description"].(string)
	if !ok || strings.TrimSpace(description) == "" || utf8.RuneCountInString(description) > 1024 {
		return "", "the front matter needs a description of 1 to 1024 characters"
	}
	if declared, present := meta["name"]; present {
		value, ok := declared.(string)
		if !ok {
			return "", "the front matter needs a name that is text"
		}
		if value != name {
			return "", "the name is not the directory name"
		}
	}
	return strings.Join(strings.Fields(description), " "), ""
}
func readExtensionEnv(path string) (map[string]string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // Fixed extension paths come from the agent configuration.
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, &privateError{message: path + ": cannot read environment file", cause: err}
	}
	vars := map[string]string{}
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		match := envLine.FindStringSubmatch(line)
		if match == nil || strings.ContainsRune(line, 0) {
			return nil, &EnvEditError{Message: fmt.Sprintf("%s: line %d is not a valid KEY=VALUE assignment", path, i+1)}
		}
		value := match[2]
		if value == "" {
			value = match[3]
		}
		if value == "" {
			value = strings.TrimRight(match[4], " \t")
		}
		vars[match[1]] = value
	}
	return vars, nil
}
func readMCP(path string) (map[string]serverConfig, error) {
	data, err := os.ReadFile(path) //nolint:gosec // Fixed extension paths come from the agent configuration.
	if errors.Is(err, os.ErrNotExist) {
		return map[string]serverConfig{}, nil
	}
	if err != nil {
		return nil, &privateError{message: path + ": cannot read MCP file", cause: err}
	}
	var envelope struct {
		Servers *map[string]serverConfig `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.Servers == nil {
		return nil, fmt.Errorf("%s: the MCP file is not valid", path)
	}
	for name, c := range *envelope.Servers {
		if !serverName.MatchString(name) || (!c.Disabled && ((c.Type == "http" && c.URL == "") || (c.Type != "http" && (c.Command == "" || c.Type != "" && c.Type != "stdio")))) {
			return nil, fmt.Errorf("%s: the MCP server declaration is not valid", path)
		}
	}
	return *envelope.Servers, nil
}
func (a *agentExtensions) scan() extensionScan {
	s := extensionScan{info: ExtensionInfo{Levels: a.levels, Skills: []SkillInfo{}, Servers: []McpServerInfo{}, Env: []EnvKeyInfo{}, Warnings: []string{}, Errors: []string{}}, env: maps.Clone(a.baseEnv)}
	if s.env == nil {
		s.env = map[string]string{}
	}
	s.info.Skills = a.skills(&s.info.Errors)
	servers := map[string]serverEntry{}
	valid, validEnv := true, true
	for _, l := range a.distinct() {
		next, err := readMCP(filepath.Join(l.Dir, "mcp.json"))
		if err != nil {
			valid = false
			s.info.Errors = append(s.info.Errors, err.Error())
		}
		for name, c := range next {
			_, override := servers[name]
			transport := ""
			if c.URL != "" {
				transport = "http"
			} else if c.Command != "" {
				transport = "stdio"
			}
			state := "idle"
			if c.Disabled {
				state = "disabled"
			}
			servers[name] = serverEntry{McpServerInfo{Name: name, Scope: l.Scope, Overrides: override, Transport: transport, State: state, Tools: []string{}}, c}
		}
		vars, err := readExtensionEnv(filepath.Join(l.Dir, ".env"))
		if err != nil {
			valid = false
			validEnv = false
			s.info.Errors = append(s.info.Errors, err.Error())
		}
		for _, key := range slices.Sorted(maps.Keys(vars)) {
			for i := range s.info.Env {
				if s.info.Env[i].Key == key {
					s.info.Env[i].Shadowed = true
				}
			}
			s.info.Env = append(s.info.Env, EnvKeyInfo{Key: key, Scope: l.Scope})
		}
		maps.Copy(s.env, vars)
	}
	if !validEnv {
		s.env = maps.Clone(a.baseEnv)
	}
	if valid {
		for _, name := range slices.Sorted(maps.Keys(servers)) {
			s.servers = append(s.servers, servers[name])
			s.info.Servers = append(s.info.Servers, servers[name].info)
		}
	}
	for _, name := range []string{"skills", "mcp.json"} {
		path := filepath.Join(a.agentDir, name)
		if _, err := os.Lstat(path); err == nil {
			s.info.Warnings = append(s.info.Warnings, path+": dex no longer reads this; move it to "+filepath.Join(a.agentDir, ".agents", name))
		}
	}
	return s
}
func (a *agentExtensions) Info(ctx context.Context) (ExtensionInfo, error) {
	if ctx.Err() != nil {
		return ExtensionInfo{}, context.Cause(ctx)
	}
	s := a.scan()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.loaded != nil {
		a.loaded.mu.Lock()
		defer a.loaded.mu.Unlock()
		s.info.Servers = slices.Clone(a.loaded.Servers)
	}
	return s.info, nil
}

// Validate reads configuration without changing the loaded environment or MCP connections.
func (a *agentExtensions) Validate(ctx context.Context) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	scan := a.scan()
	if len(scan.info.Errors) > 0 {
		return &EnvEditError{Message: strings.Join(scan.info.Errors, "\n")}
	}
	return nil
}
func (a *agentExtensions) Load(ctx context.Context) (*loadedExtensions, error) {
	s := a.scan()
	loaded := &loadedExtensions{Env: s.env, Servers: []McpServerInfo{}, Extension: &durable.Extension{Name: "extensions"}}
	guide := extensionGuide(a.levels)
	loaded.Extension.Sections = []*durable.PromptSection{{Key: "extension-guide", Render: func(context.Context, durable.PromptInput) (*string, error) { return new(guide), nil }}, {Key: "skills", Render: func(context.Context, durable.PromptInput) (*string, error) {
		errs := []string{}
		return skillsText(a.skills(&errs)), nil
	}}}
	names := map[string]bool{}
	for _, entry := range s.servers {
		if ctx.Err() != nil {
			_ = loaded.Close(ctx)
			return nil, context.Cause(ctx)
		}
		info := entry.info
		index := len(loaded.Servers)
		loaded.Servers = append(loaded.Servers, info)
		if entry.config.Disabled {
			continue
		}
		client, tools, err := a.connect(ctx, entry, s.env)
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
	loaded.HasTools = len(loaded.Extension.Tools) > 0
	a.mu.Lock()
	a.loaded = loaded
	a.mu.Unlock()
	return loaded, nil
}
func (a *agentExtensions) connect(ctx context.Context, entry serverEntry, env map[string]string) (*mcp.Client, []*durable.ToolRegistration, error) {
	c := entry.config
	client := mcp.NewClient(mcp.ClientOptions{Name: "dex", Version: "1", RequestTimeoutMs: 30000})
	var transport mcp.Transport
	if c.Type == "http" {
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
		t, err := mcp.NewStreamableHTTPTransport(mcp.StreamableHTTPTransportOptions{URL: c.URL, Headers: headers})
		if err != nil {
			return nil, nil, &privateError{message: "invalid MCP URL", cause: err}
		}
		transport = t
	} else {
		vars := maps.Clone(env)
		if vars == nil {
			vars = map[string]string{}
		}
		maps.Copy(vars, c.Env)
		cwd := c.Cwd
		if !filepath.IsAbs(cwd) {
			cwd = filepath.Join(a.workspace, cwd)
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
		description := tool.Description
		if description == "" {
			description = tool.Name
		}
		tools = append(tools, &durable.ToolRegistration{Name: name, Description: description, Parameters: schema, Deferred: true, Execute: func(ctx context.Context, args any, _ durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
			result, err := client.CallTool(ctx, tool.Name, args, mcp.RequestOptions{TimeoutMs: 600000})
			if err != nil {
				return durable.ToolExecutionResult{}, &privateError{message: "MCP tool call failed", cause: err}
			}
			output := []ai.ToolResultMessageContent{}
			for _, block := range mcp.ToLLMContent(*result) {
				if block.Type == "image" {
					output = append(output, ai.ImageContent{Data: block.Data, MimeType: block.MimeType})
				} else {
					output = append(output, ai.TextContent{Text: block.Text})
				}
			}
			return durable.ToolExecutionResult{Output: output, IsError: result.IsError}, nil
		}})
	}
	return client, tools, nil
}
func mcpFailure(err error) string {
	if e, ok := errors.AsType[*mcp.McpHttpError](err); ok {
		return fmt.Sprintf("the server answered with HTTP status %d", e.Status)
	}
	if err.Error() == "missing header environment key" {
		return "a header environment key is missing"
	}
	return "the connection failed"
}
func (a *agentExtensions) EditEnv(ctx context.Context, scope, key string, value *string) (ExtensionInfo, error) {
	if scope != "agent" && scope != "dex" {
		return ExtensionInfo{}, &EnvEditError{Message: "only agent and dex environment files are writable"}
	}
	if !envName.MatchString(key) {
		return ExtensionInfo{}, &EnvEditError{Message: "the environment key is not valid"}
	}
	assignment := ""
	if value != nil {
		quoted, err := quoteExtensionEnv(*value)
		if err != nil {
			return ExtensionInfo{}, err
		}
		assignment = key + "=" + quoted
	}
	var path string
	for _, l := range a.levels {
		if l.Scope == scope {
			path = filepath.Join(l.Dir, ".env")
		}
	}
	envWrites.Lock()
	err := writeExtensionEnv(path, key, assignment, value != nil)
	envWrites.Unlock()
	if err != nil {
		return ExtensionInfo{}, err
	}
	return a.Info(ctx)
}
func quoteExtensionEnv(value string) (string, error) {
	if strings.ContainsAny(value, "\n\r\x00") {
		return "", &EnvEditError{Message: "the value must not contain LF, CR, or NUL"}
	}
	if strings.Contains(value, "'") && strings.Contains(value, "\"") {
		return "", &EnvEditError{Message: "the value must not contain both single and double quotes"}
	}
	if strings.Contains(value, "'") {
		return "\"" + value + "\"", nil
	}
	if strings.ContainsAny(value, "\"#") || strings.TrimSpace(value) != value {
		return "'" + value + "'", nil
	}
	return value, nil
}
func writeExtensionEnv(path, key, assignment string, set bool) error {
	if _, err := readExtensionEnv(path); err != nil {
		return err
	}
	data, err := os.ReadFile(path) //nolint:gosec // Fixed extension paths come from the agent configuration.
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return &privateError{message: "cannot read environment file", cause: err}
	}
	var out strings.Builder
	found := false
	for _, line := range strings.SplitAfter(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		match := envLine.FindStringSubmatch(trimmed)
		if match == nil || match[1] != key {
			out.WriteString(line)
			continue
		}
		if !found && set {
			out.WriteString(assignment)
			if strings.HasSuffix(line, "\r\n") {
				out.WriteString("\r\n")
			} else if strings.HasSuffix(line, "\n") {
				out.WriteByte('\n')
			}
		}
		found = true
	}
	if !found && set {
		if out.Len() > 0 && !strings.HasSuffix(out.String(), "\n") {
			out.WriteByte('\n')
		}
		out.WriteString(assignment + "\n")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".env-*.tmp")
	if err != nil {
		return &privateError{message: "cannot create environment file", cause: err}
	}
	defer os.Remove(file.Name())
	_, err = file.WriteString(out.String())
	err = errors.Join(err, file.Close())
	if err == nil {
		err = os.Rename(file.Name(), path)
	}
	if err != nil {
		return &privateError{message: "cannot write environment file", cause: err}
	}
	return nil
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
func extensionGuide(levels []ExtensionLevel) string {
	lines := []string{"Extension levels for this agent, from wide to narrow. Each can hold skills/, mcp.json, and .env:"}
	for _, l := range levels {
		lines = append(lines, "- "+l.Scope+": "+l.Dir+"/")
	}
	lines = append(lines,
		"Narrower levels win. A skill or MCP server with the same name replaces the wider entry as a whole. Environment values merge per key.",
		"",
		"Skills: use <level>/skills/<name>/SKILL.md. Start with YAML front matter fields name and description. Skill edits apply on the next request. Use the skills list when it is present.",
		`MCP: use <level>/mcp.json with a mcpServers object. For stdio, use command, args, env, and cwd. For HTTP, use "type": "http", url, and headers. {"disabled": true} turns off a wider server with the same name.`,
		"",
		".env: values are in the tool environment. Use $KEY in bash and ${KEY} in mcp.json headers. Never print, echo, or write a value into chat, memory, or files. The user sets values on the Secrets page.",
		"mcp.json and .env changes apply after the user presses Reload in the Extensions tab. You cannot reload. Ask the user to press Reload when needed.")
	return strings.Join(lines, "\n")
}
