package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
	storage "github.com/MichaelKinsy/PiG/durable/storage/sqlite/node"
)

var telegramGroupScopePattern = regexp.MustCompile(`^tg_([1-9][0-9]*)_(-[1-9][0-9]*)_(0|[1-9][0-9]*)$`)

// TelegramGroupScope names a resource audience. Sender-specific sessions can share this audience.
func TelegramGroupScope(botID string, chatID, topicID int64) (string, error) {
	scope := fmt.Sprintf("tg_%s_%d_%d", botID, chatID, topicID)
	if err := validateGroupScope(scope); err != nil {
		return "", err
	}
	return scope, nil
}

func validateGroupScope(scope string) error {
	parts := telegramGroupScopePattern.FindStringSubmatch(scope)
	if parts == nil {
		return fmt.Errorf("%w: invalid Telegram group scope", ErrInvalidInput)
	}
	for _, part := range parts[1:] {
		if _, err := strconv.ParseInt(part, 10, 64); err != nil {
			return fmt.Errorf("%w: invalid Telegram group identity", ErrInvalidInput)
		}
	}
	return nil
}

func resourceKey(ref Ref) string {
	if ref.Scope == "" {
		return ref.AgentID
	}
	return ref.AgentID + "/" + ref.Scope
}

func (a *Agents) executionDir(ref Ref) string {
	dir := filepath.Join(a.config.DataDir, "agents", ref.AgentID)
	if ref.Scope != "" {
		dir = filepath.Join(dir, "telegram-groups", ref.Scope)
	}
	return dir
}

// groupDirectory rejects symlinked scope components before SQLite or resource access opens.
func (a *Agents) groupDirectory(ref Ref) (string, error) {
	if err := validateGroupScope(ref.Scope); err != nil {
		return "", err
	}
	if err := os.MkdirAll(a.config.DataDir, 0700); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(a.config.DataDir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	relative := filepath.Join("agents", ref.AgentID, "telegram-groups", ref.Scope)
	current := ""
	for part := range strings.SplitSeq(filepath.ToSlash(relative), "/") {
		current = filepath.Join(current, part)
		info, err := root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := root.Mkdir(current, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return "", err
			}
			info, err = root.Lstat(current)
		}
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("group scope directory must not be a symlink")
		}
	}
	if info, err := root.Lstat(filepath.Join(relative, "state.sqlite")); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return "", errors.New("group execution storage must be a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return filepath.Join(root.Name(), relative), nil
}

func (a *Agents) openGroup(ctx context.Context, ref Ref) (_ *agentResources, resultErr error) {
	if err := a.Catalog.DiscoveryReady(); err != nil {
		return nil, err
	}
	dir, err := a.groupDirectory(ref)
	if err != nil {
		return nil, err
	}
	groupRoot, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	if err := groupRoot.Mkdir("resources", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		_ = groupRoot.Close()
		return nil, err
	}
	info, err := groupRoot.Lstat("resources")
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		_ = groupRoot.Close()
		return nil, errors.New("group resource directory must not be a symlink")
	}
	root, err := groupRoot.OpenRoot("resources")
	_ = groupRoot.Close()
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			_ = root.Close()
		}
	}()
	extension := groupExtension(root)
	registry := harness.CreateRegistry()
	if err := registry.Install(extension); err != nil {
		return nil, err
	}
	store, err := storage.OpenNodeSqliteStorage(filepath.Join(dir, "state.sqlite"), storage.NodeSqliteStorageOptions{})
	if err != nil {
		return nil, err
	}
	h, err := harness.OpenHarness(ctx, store, harness.HarnessOptions{Models: a.Catalog.Models, Registry: registry, Settings: func() *harness.HarnessSettings {
		return &harness.HarnessSettings{Extensions: []*durable.Extension{extension}, AfterFailedRun: durable.AfterFailedRunContinue, SteeringMode: durable.QueueAll}
	}})
	if err != nil {
		_ = store.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	return &agentResources{harness: h, store: store, groupRoot: root, groupExtension: extension}, nil
}

// changeFor keeps group configuration outside the private resource loader.
func (a *Agents) changeFor(ref Ref, options Options) harness.AgentChange {
	if ref.Scope == "" {
		return a.agentChange(ref.AgentID, options)
	}
	options.Instructions = ""
	options.Cwd = ""
	change := agentChange(options)
	a.mu.Lock()
	extension := a.resources[resourceKey(ref)].groupExtension
	a.mu.Unlock()
	change.Extensions = harness.SetTo(harness.ExtensionChange{List: []*durable.Extension{extension}})
	change.Tools = harness.SetTo(harness.ToolChange{List: extension.Tools})
	change.ModelTools = harness.SetTo(harness.ToolChange{List: extension.Tools})
	return change
}

func groupExtension(root *os.Root) *durable.Extension {
	rules := "This conversation is a Telegram group audience. Use only this conversation and explicitly published group resources. Private bot instructions, memory, projects, skills, environment and MCP are not available. A group sender is not an owner or administrator."
	extension := &durable.Extension{Name: "telegram-group", Sections: []*durable.PromptSection{harness.Section("group-audience", func(context.Context, durable.PromptInput) (*string, error) { return &rules, nil }, harness.SectionOptions{})}, Hooks: []durable.HookRegistration{harness.Hook(harness.ToolTask, &harness.ToolHooks{BeforeTool: func(_ context.Context, call harness.ToolHookCall, _ harness.HookApi) (*harness.BeforeToolResult, error) {
		if !slices.Contains([]string{"group_resource", "group_note"}, call.Name) {
			return &harness.BeforeToolResult{Block: new("tool is not allowed in this group audience")}, nil
		}
		return nil, nil
	}})}}
	extension.Tools = []*durable.ToolRegistration{{Name: "group_resource", Description: "Read one text file explicitly published in this group/topic resource directory. Private files, traversal and symlinks are not available.", Replay: durable.ReplaySafe, Parameters: map[string]any{"type": "object", "required": []string{"path"}, "additionalProperties": false, "properties": map[string]any{"path": map[string]any{"type": "string", "minLength": 1}}}, Execute: func(ctx context.Context, raw any, _ durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
		args, err := durable.FromJsonValue[struct {
			Path string `json:"path"`
		}](raw)
		if err != nil {
			return durable.ToolExecutionResult{}, err
		}
		body, err := readGroupResource(root, args.Path)
		if err != nil {
			return durable.ToolExecutionResult{}, err
		}
		return durable.ToolExecutionResult{Output: []ai.ToolResultMessageContent{ai.TextContent{Text: string(body)}}}, ctx.Err()
	}}, {Name: "group_note", Description: "Append a short note to this group/topic's isolated notes. This does not write private memory.", Replay: durable.ReplaySafe, Parameters: map[string]any{"type": "object", "required": []string{"text"}, "additionalProperties": false, "properties": map[string]any{"text": map[string]any{"type": "string", "minLength": 1, "maxLength": 4000}}}, Execute: func(ctx context.Context, raw any, api durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
		args, err := durable.FromJsonValue[struct {
			Text string `json:"text"`
		}](raw)
		if err != nil {
			return durable.ToolExecutionResult{}, err
		}
		// A native operation owns one immutable note file, so replay cannot append twice.
		path := fmt.Sprintf("note-%d.txt", api.TaskId())
		if err := writeGroupNote(root, path, args.Text); err != nil {
			return durable.ToolExecutionResult{}, err
		}
		return durable.ToolExecutionResult{Output: []ai.ToolResultMessageContent{ai.TextContent{Text: path}}}, ctx.Err()
	}}}
	return extension
}

func openGroupResource(root *os.Root, path string) (*os.File, error) {
	if !filepath.IsLocal(path) || strings.Contains(path, "\\") {
		return nil, errors.New("group resource path is outside this audience")
	}
	current := ""
	for part := range strings.SplitSeq(filepath.ToSlash(path), "/") {
		if part == ".." || part == "." || part == "" {
			return nil, errors.New("group resource path must not traverse directories")
		}
		current = filepath.Join(current, part)
		info, err := root.Lstat(current)
		if err != nil {
			return nil, errors.New("group resource was not published in this audience")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("group resource symlinks are not allowed")
		}
	}
	file, err := root.Open(path)
	if err != nil {
		return nil, errors.New("group resource is outside this audience")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.New("group resource must be a regular file")
	}
	return file, nil
}

func readGroupResource(root *os.Root, path string) ([]byte, error) {
	file, err := openGroupResource(root, path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil {
		return nil, err
	}
	if len(body) > 65536 || !utf8.Valid(body) {
		return nil, errors.New("group text resources have a 64 KiB UTF-8 limit")
	}
	return body, nil
}

func (r *agentResources) closeExtras(ctx context.Context) error {
	if r.loaded != nil {
		return r.loaded.Close(ctx)
	}
	return r.groupRoot.Close()
}

func (a *Agents) inputDirectory(ref Ref) string {
	if ref.Scope != "" {
		return filepath.Join(a.executionDir(ref), "resources")
	}
	return filepath.Join(a.executionDir(ref), "workspace")
}

func writeGroupNote(root *os.Root, path, text string) error {
	if existing, err := readGroupResource(root, path); err == nil {
		if string(existing) != text {
			return errors.New("group note operation conflicts with an existing resource")
		}
		return nil
	}
	temporary := "." + path + ".pending"
	if err := root.Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(temporary) }()
	_, writeErr := io.WriteString(file, text)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if err := root.Link(temporary, path); err != nil {
		return err
	}
	return nil
}

func (a *Agents) groupAttachments(ref Ref, attachments []Attachment) error {
	if ref.Scope == "" {
		return nil
	}
	a.mu.Lock()
	root := a.resources[resourceKey(ref)].groupRoot
	a.mu.Unlock()
	for _, attachment := range attachments {
		file, err := openGroupResource(root, attachment.Path)
		if err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
	}
	return nil
}
