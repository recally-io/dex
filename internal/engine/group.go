package engine

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
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

// GroupResources opens only an audience directory. The caller authorizes access and closes the root.
func (a *Agents) GroupResources(agentID, scope string) (*os.Root, error) {
	if !agentIDPattern.MatchString(agentID) {
		return nil, ErrInvalidInput
	}
	dir, err := a.groupDirectory(Ref{AgentID: agentID, Scope: scope})
	if err != nil {
		return nil, err
	}
	return openGroupResources(dir)
}

func openGroupResources(dir string) (*os.Root, error) {
	groupRoot, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer groupRoot.Close()
	if err := groupRoot.Mkdir("resources", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := groupRoot.Lstat("resources")
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("group resource directory must not be a symlink")
	}
	return groupRoot.OpenRoot("resources")
}

func (a *Agents) openGroup(ctx context.Context, ref Ref) (_ *agentResources, resultErr error) {
	if err := a.Catalog.DiscoveryReady(); err != nil {
		return nil, err
	}
	dir, err := a.groupDirectory(ref)
	if err != nil {
		return nil, err
	}
	root, err := openGroupResources(dir)
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			_ = root.Close()
		}
	}()
	store, err := storage.OpenNodeSqliteStorage(filepath.Join(dir, "state.sqlite"), storage.NodeSqliteStorageOptions{})
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			_ = store.Close(context.WithoutCancel(ctx))
		}
	}()
	var h harness.Harness
	extension := groupExtension(root)
	if a.config.Tasks.GroupSession != nil {
		extension.Tools = append(extension.Tools, a.groupSessionTool(ref))
	}
	if a.config.Tasks.DeliverGroupFile != nil {
		extension.Tools = append(extension.Tools, a.groupDeliveryTool(ref, root))
	}
	// Recovery can start a provider request before business receipt reconciliation.
	if a.config.Tasks.AuthorizeGroup != nil {
		extension.Hooks = append(extension.Hooks, harness.Hook(harness.GenerationTask, &harness.GenerationHooks{BeforeRequest: func(ctx context.Context, _ harness.GenerationRequest, api harness.HookApi) (*harness.GenerationRequest, error) {
			if err := a.authorizeGroupRun(ctx, ref, store, api, api.ConversationId()); err != nil {
				// PiG reports ordinary hook errors and continues. Its abort mark cancels this invocation.
				_, abortErr := h.AbortTask(ctx, api.TaskId())
				return nil, errors.Join(err, abortErr)
			}
			return nil, nil
		}}))
		for _, tool := range extension.Tools {
			execute := tool.Execute
			tool.Execute = func(ctx context.Context, raw any, api durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
				if err := a.authorizeGroupRun(ctx, ref, store, api, api.ConversationId()); err != nil {
					return durable.ToolExecutionResult{}, err
				}
				source, err := taskSource(ctx, api)
				if err != nil {
					return durable.ToolExecutionResult{}, err
				}
				caller := TaskContext{Scope: ref.Scope, AgentID: ref.AgentID, ConversationID: int64(api.ConversationId()), SourceRequestID: source, Key: fmt.Sprintf("%s/%s/%d", ref.AgentID, ref.Scope, api.TaskId())}
				if err := a.config.Tasks.AuthorizeGroup(ctx, caller); err != nil {
					return durable.ToolExecutionResult{}, err
				}
				return execute(ctx, raw, api)
			}
		}
	}
	registry := harness.CreateRegistry()
	if err := registry.Install(extension); err != nil {
		return nil, err
	}
	h, err = harness.OpenHarness(ctx, store, harness.HarnessOptions{Models: a.Catalog.Models, Registry: registry, Settings: func() *harness.HarnessSettings {
		return &harness.HarnessSettings{Extensions: []*durable.Extension{extension}, AfterFailedRun: durable.AfterFailedRunContinue, SteeringMode: durable.QueueAll}
	}})
	if err != nil {
		return nil, err
	}
	return &agentResources{harness: h, store: store, groupRoot: root, groupExtension: extension}, nil
}

// authorizeGroupRun includes steered actors, not only the first input that owns a tool operation.
func (a *Agents) authorizeGroupRun(ctx context.Context, ref Ref, store durable.Storage, reader durable.DocumentReader, conversationID durable.ConversationId) error {
	live, err := durable.Snapshot(ctx, reader, harness.LiveDoc, conversationID)
	if err != nil {
		return err
	}
	if live == nil || live.Run == nil || len(live.Run.Inputs) == 0 {
		return errors.New("group execution has no bound input")
	}
	for _, id := range live.Run.Inputs {
		source, err := store.Submission(ctx, id)
		if err != nil {
			return err
		}
		if source == nil || source.ConversationId != conversationID || source.RequestId == nil {
			return errors.New("group execution has no bound source")
		}
		caller := TaskContext{Scope: ref.Scope, AgentID: ref.AgentID, ConversationID: int64(conversationID), SourceRequestID: *source.RequestId}
		if err := a.config.Tasks.AuthorizeGroup(ctx, caller); err != nil {
			return err
		}
	}
	return nil
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
		if !slices.Contains([]string{"group_resource", "group_note", "group_deliver", "group_session"}, call.Name) {
			return &harness.BeforeToolResult{Block: new("tool is not allowed in this group audience")}, nil
		}
		return nil, nil
	}})}}
	extension.Tools = []*durable.ToolRegistration{{Name: "group_resource", Description: "Read one explicitly published group/topic resource: UTF-8 text up to 64 KiB or an image up to 20 MiB. Private files, traversal and symlinks are not available.", Replay: durable.ReplaySafe, Parameters: map[string]any{"type": "object", "required": []string{"path"}, "additionalProperties": false, "properties": map[string]any{"path": map[string]any{"type": "string", "minLength": 1}}}, Execute: func(ctx context.Context, raw any, _ durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
		args, err := durable.FromJsonValue[struct {
			Path string `json:"path"`
		}](raw)
		if err != nil {
			return durable.ToolExecutionResult{}, err
		}
		content, err := groupResourceContent(root, args.Path)
		if err != nil {
			return durable.ToolExecutionResult{}, err
		}
		return durable.ToolExecutionResult{Output: content}, ctx.Err()
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

func groupResourceContent(root *os.Root, path string) ([]ai.ToolResultMessageContent, error) {
	file, err := openGroupResource(root, path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	header := make([]byte, 512)
	n, err := file.ReadAt(header, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	mime := http.DetectContentType(header[:n])
	image := slices.Contains([]string{"image/png", "image/jpeg", "image/gif", "image/webp"}, mime)
	limit := int64(65536)
	if image {
		limit = 20 << 20
	}
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit || !image && !utf8.Valid(body) {
		return nil, errors.New("group resource exceeds its text or image limit")
	}
	if image {
		return []ai.ToolResultMessageContent{ai.ImageContent{Data: base64.StdEncoding.EncodeToString(body), MimeType: mime}}, nil
	}
	return []ai.ToolResultMessageContent{ai.TextContent{Text: string(body)}}, nil
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
