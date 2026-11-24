package engine

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/chord"
	"github.com/MichaelKinsy/PiG/codemode"
	"github.com/MichaelKinsy/PiG/durable"
	durablecode "github.com/MichaelKinsy/PiG/durable/codemode"
	durableenv "github.com/MichaelKinsy/PiG/durable/env"
	"github.com/MichaelKinsy/PiG/durable/harness"
	storage "github.com/MichaelKinsy/PiG/durable/storage/sqlite/node"
	"github.com/recally-io/dex/internal/execenv"
	"github.com/recally-io/dex/internal/vault"
)

var configureReporter sync.Once

var agentIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type agentOpening struct {
	done   chan struct{}
	cancel context.CancelFunc
}

type agentResources struct {
	harness        harness.Harness
	store          durable.Storage
	loaded         *loadedExtensions
	execStamp      string
	envs           *environments
	groupRoot      *os.Root
	groupExtension *durable.Extension
}

type activityRecord struct {
	lastActivityAt int64
	toolStarted    map[string]int64
	runningTools   []RunningTool
}

// Activity is the in-memory activity snapshot for already-open conversations.
// It never opens a Harness or execution storage.
func (a *Agents) Activity(refs []Ref) map[Ref]Activity {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[Ref]Activity, len(refs))
	for _, ref := range refs {
		r := a.activity[ref]
		if r == nil || r.lastActivityAt == 0 {
			continue
		}
		activity := Activity{LastActivityAt: new(r.lastActivityAt), RunningTools: append([]RunningTool(nil), r.runningTools...)}
		for _, started := range r.toolStarted {
			if activity.ToolStartedAt == nil || started < *activity.ToolStartedAt {
				activity.ToolStartedAt = new(started)
			}
		}
		out[ref] = activity
	}
	return out
}

// Agents owns the native execution resources. Harnesses stay open until Close or Reload.
type Agents struct {
	Catalog         *Catalog
	config          Config
	memory          *memoryRepo
	skillStores     map[string]*skillStore
	codeSandbox     *codemode.Sandbox
	env             *toolEnv
	vault           *vault.Store
	mu              sync.Mutex
	resources       map[string]*agentResources
	activity        map[Ref]*activityRecord
	activityWatches map[Ref]*childActivityWatch // one watch shared by live submissions
	opening         map[string]*agentOpening
	recovering      map[string]map[durable.ConversationId]bool
	serial          map[Ref]chan struct{}
	paused, closed  bool
	// busy counts the operations and settlements of each resource key. Maintenance waits for
	// all keys, and a Reload waits only for the key of its agent. Work that uses no agent
	// resources counts under "".
	busy        map[string]int
	dirty       map[string]string // agent ID -> last change reason
	refused     map[string]string // agent ID -> configuration stamp that failed validation
	reloadWake  chan struct{}
	operations  sync.WaitGroup
	settlements sync.WaitGroup
	closeDone   chan struct{}
	closeErr    error
}

func New(config Config) (*Agents, error) {
	configureReporter.Do(func() {
		chord.SetUncaughtErrorReporter(func(err error) { slog.Error("uncaught execution listener error", "error", err) })
	})
	if config.DataDir == "" {
		return nil, fmt.Errorf("%w: data directory is required", ErrInvalidInput)
	}
	if config.Home == "" {
		var err error
		config.Home, err = os.UserHomeDir()
		if err != nil {
			return nil, err
		}
	}
	config.BaseEnv = maps.Clone(config.BaseEnv)
	if config.ExecEnv == nil {
		config.ExecEnv = execenv.NewLocalProvider(config.DataDir, "")
	}
	// Code mode uses PiG's embedded JavaScript sandbox. It loads on the first script.
	codeSandbox, err := codemode.NewSandbox(codemode.SandboxOptions{})
	if err != nil {
		return nil, err
	}
	warnIgnoredEnv(config.DataDir)
	agents := &Agents{vault: vault.New(config.DataDir), Catalog: NewCatalog(config.FakeModel), config: config, codeSandbox: codeSandbox, skillStores: map[string]*skillStore{}, memory: newMemoryRepo(filepath.Join(config.DataDir, "memory"), config.BaseEnv), env: newToolEnv(config.Home, config.BaseEnv, config.LoginEnv), resources: map[string]*agentResources{}, activity: map[Ref]*activityRecord{}, activityWatches: map[Ref]*childActivityWatch{}, recovering: map[string]map[durable.ConversationId]bool{}, opening: map[string]*agentOpening{}, busy: map[string]int{}, dirty: map[string]string{}, refused: map[string]string{}, reloadWake: make(chan struct{}, 1), serial: map[Ref]chan struct{}{}, closeDone: make(chan struct{})}
	agents.Catalog.onDiscoveryDone = agents.wakeReloads
	return agents, nil
}

// begin tracks one operation on the resources of key, the resource key of an agent or of a
// group scope.
func (a *Agents) begin(key string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return ErrClosed
	}
	if a.paused {
		return ErrMaintenance
	}
	a.trackLocked(key)
	return nil
}

// trackLocked counts one operation on key. The caller holds mu.
func (a *Agents) trackLocked(key string) {
	a.operations.Add(1)
	a.busy[key]++
}

func (a *Agents) end(key string) {
	a.mu.Lock()
	a.untrackLocked(key)
	a.mu.Unlock()
	a.operations.Done()
}

func (a *Agents) untrackLocked(key string) {
	if a.busy[key]--; a.busy[key] == 0 {
		delete(a.busy, key)
		if a.dirty[key] != "" {
			a.wakeReloads()
		}
	}
}

// settle runs fn in the background as settlement work of key. Maintenance and a Reload of key
// wait for it, because fn uses the Harness of key.
func (a *Agents) settle(key string, fn func()) {
	a.mu.Lock()
	a.busy[key]++
	a.mu.Unlock()
	a.settlements.Go(func() {
		defer func() { a.mu.Lock(); a.untrackLocked(key); a.mu.Unlock() }()
		fn()
	})
}

func (a *Agents) lock(ctx context.Context, ref Ref) (func(), error) {
	a.mu.Lock()
	gate := a.serial[ref]
	if gate == nil {
		gate = make(chan struct{}, 1)
		a.serial[ref] = gate
	}
	a.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case gate <- struct{}{}:
		return func() { <-gate }, nil
	}
}

func (a *Agents) Recover(ctx context.Context, refs []Ref) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.closed {
		return ErrClosed
	}
	if len(a.resources) > 0 || len(a.opening) > 0 {
		return errors.New("recovery must precede opening agents")
	}
	for _, ref := range refs {
		if ref.Scope != "" {
			if err := validateGroupScope(ref.Scope); err != nil {
				return err
			}
		}
		if !agentIDPattern.MatchString(ref.AgentID) || ref.ID <= 0 {
			return fmt.Errorf("%w: recovery reference", ErrInvalidInput)
		}
	}
	for _, ref := range refs {
		key := resourceKey(ref)
		if a.recovering[key] == nil {
			a.recovering[key] = map[durable.ConversationId]bool{}
		}
		a.recovering[key][durable.ConversationId(ref.ID)] = true
	}
	return nil
}

func (a *Agents) harness(ctx context.Context, agentID, scope string) (harness.Harness, error) {
	if !agentIDPattern.MatchString(agentID) {
		return nil, fmt.Errorf("%w: agent ID", ErrInvalidInput)
	}
	if scope != "" {
		if err := validateGroupScope(scope); err != nil {
			return nil, err
		}
		agentID = resourceKey(Ref{AgentID: agentID, Scope: scope})
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			return nil, ErrClosed
		}
		if opening := a.opening[agentID]; opening != nil {
			a.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-opening.done:
				continue
			}
		}
		if resource := a.resources[agentID]; resource != nil {
			a.mu.Unlock()
			return resource.harness, nil
		}
		if a.paused {
			a.mu.Unlock()
			return nil, ErrMaintenance
		}
		openCtx, cancel := context.WithCancel(ctx)
		opening := &agentOpening{done: make(chan struct{}), cancel: cancel}
		a.opening[agentID] = opening
		a.mu.Unlock()
		h, err := a.openAndPublish(openCtx, agentID, opening)
		return h, a.closedDuring(ctx, err)
	}
}

// closedDuring reports ErrClosed when Close cancelled an open that the caller's own context still
// allows. Close cancels every opening slot, so the open fails with a cancellation that the caller
// did not ask for.
func (a *Agents) closedDuring(ctx context.Context, err error) error {
	if !errors.Is(err, context.Canceled) || ctx.Err() != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return ErrClosed
	}
	return err
}

// The opening slot owns the target until recovery finishes and Resume is safe.
func (a *Agents) openAndPublish(ctx context.Context, agentID string, opening *agentOpening) (_ harness.Harness, resultErr error) {
	defer a.finishOpening(agentID, opening)
	resource, err := a.open(ctx, agentID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			cleanup := context.WithoutCancel(ctx)
			resultErr = errors.Join(resultErr, resource.harness.Close(cleanup), resource.closeExtras(cleanup))
		}
	}()
	isolated := map[durable.ConversationId]bool{}
	for {
		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			return nil, ErrClosed
		}
		if err := ctx.Err(); err != nil {
			a.mu.Unlock()
			return nil, err
		}
		pending := map[durable.ConversationId]bool{}
		for id := range a.recovering[agentID] {
			if !isolated[id] {
				pending[id] = true
			}
		}
		if len(pending) == 0 {
			delete(a.recovering, agentID)
			a.resources[agentID] = resource
			resource.harness.Resume()
			a.mu.Unlock()
			return resource.harness, nil
		}
		a.mu.Unlock()
		if err := isolateConversations(ctx, resource.harness, pending); err != nil {
			return nil, err
		}
		maps.Copy(isolated, pending)
	}
}

func (a *Agents) open(ctx context.Context, agentID string) (_ *agentResources, resultErr error) {
	if owner, scope, group := strings.Cut(agentID, "/"); group {
		return a.openGroup(ctx, Ref{AgentID: owner, Scope: scope})
	}
	if err := a.Catalog.discoveryReady(); err != nil {
		return nil, err
	}
	dir := filepath.Join(a.config.DataDir, "agents", agentID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := a.memory.Open(ctx); err != nil {
		return nil, err
	}
	execHandle, ownErr := a.config.ExecEnv.Open(ctx, agentID, "")
	var execStamp string
	if ownErr == nil {
		execStamp = execHandle.Stamp()
	} else {
		// Keep the Harness available to sessions that select a different environment.
		// Remember the stamp so a reconnect can reload the default and its MCP tools.
		execStamp, _ = a.config.ExecEnv.Stamp(ctx, agentID, "")
	}
	files := a.files(agentID, execHandle)
	files.defaultUnavailable = ownErr != nil
	envs := &environments{
		agentID: agentID, provider: a.config.ExecEnv, selected: a.sessionEnvironmentID, files: files,
		own: execHandle, ownFiles: files, ownErr: ownErr, retryStamp: execStamp, retryAfter: time.Now().Add(time.Second),
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, envs.close(context.WithoutCancel(ctx)))
		}
	}()
	if err := files.openSkills(ctx); err != nil {
		return nil, err
	}
	files.trySyncSkills(ctx, nil, true)
	session := func(ctx context.Context, id durable.ConversationId) (sessionEnv, error) {
		return envs.session(ctx, int64(id))
	}
	files.sessionPrompts = true
	loaded, err := files.Load(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, loaded.Close(context.WithoutCancel(ctx)))
		}
	}()
	envs.loaded = loaded
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	background := func(ctx context.Context, id durable.ConversationId) (backgroundEnv, error) {
		s, err := envs.session(ctx, int64(id))
		if err != nil {
			return backgroundEnv{}, err
		}
		outputDir := filepath.Join(dir, "background")
		if s.Inherit {
			outputDir = execenv.JoinTargetPath(s.Handle.Workspace(), ".dex", "background")
		}
		return backgroundEnv{EnvironmentID: s.ID, Env: s.Vars, InheritEnv: s.Inherit, Kind: s.Handle.Kind(), OutputDir: outputDir, Workspace: s.Handle.Workspace()}, nil
	}
	registry := harness.CreateRegistry()
	extensions := []*durable.Extension{compactionExtension(), checkExtension(agentID, a.config.Tasks.ReportCheck), toolSearchExtension(), codingTools(session, a.config.BashTimeout, loaded.Vault), routineExtension(agentID, a.config.Routines), sessionExtension(agentID, a.config.Tasks), sessionReadExtension(agentID, a.config.Tasks), monitorExtension(agentID, a.config.Tasks.Monitors, background, loaded.Vault), vaultExtension(agentID, loaded.Vault), workLoopExtension(), artifactExtension(agentID, session, a.config.Tasks.DeliverArtifact), a.memory.Extension(agentID, func(ctx context.Context, id durable.ConversationId) (string, error) {
		return a.source(ctx, Ref{AgentID: agentID, ID: int64(id)})
	}), files.skillExtension(), durablecode.CreateExtension(durablecode.ToolOptions{Sandbox: a.codeSandbox, Models: new(false)}), loaded.Extension, {Name: "mcp-server-admin", Tools: a.mcpServerTools(agentID, loaded)}}
	for _, extension := range extensions {
		if err := registry.Install(extension); err != nil {
			return nil, err
		}
	}
	if err := registry.Install(frontDeskExtension()); err != nil {
		return nil, err
	}
	if err := errors.Join(registry.Install(outboxExtension(agentID, a.config.Tasks.Outbox)), registry.Install(childOutboxExtension(agentID, a.config.Tasks.Outbox))); err != nil {
		return nil, err
	}
	if err := registry.Install(askUserExtension(agentID, a.config.Tasks.AskUser)); err != nil {
		return nil, err
	}
	if err := errors.Join(registry.Install(botReadExtension(agentID, a.config.Bots)), registry.Install(botManageExtension(agentID, a.config.Bots))); err != nil {
		return nil, err
	}
	if err := registry.Install(workTaskExtension(agentID, a.config.WorkTasks)); err != nil {
		return nil, err
	}
	if err := registry.Install(childExtension()); err != nil {
		return nil, err
	}
	if err := registry.Install(dreamMessageExtension(agentID, a.config.Tasks.ReadDreamMessage)); err != nil {
		return nil, err
	}
	store, err := storage.OpenNodeSqliteStorage(filepath.Join(dir, "state.sqlite"), storage.NodeSqliteStorageOptions{})
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, store.Close(context.WithoutCancel(ctx)))
		}
	}()
	if err := registry.Install(routineCheckInstructions()); err != nil {
		return nil, err
	}
	h, err := harness.OpenHarness(ctx, store, harness.HarnessOptions{
		Models: a.modelsFor(agentID), Registry: registry,
		Settings: func() *harness.HarnessSettings {
			return &harness.HarnessSettings{Extensions: extensions, AfterFailedRun: durable.AfterFailedRunContinue, SteeringMode: durable.QueueAll, FollowUpMode: durable.QueueAll}
		},
		Env: func(envCtx context.Context, target harness.EnvTarget) (durableenv.ExecutionEnv, error) {
			s, err := envs.session(envCtx, int64(target.ConversationId))
			if err != nil {
				return nil, err
			}
			cwd := ""
			if target.Cwd != nil {
				cwd = *target.Cwd
			}
			exec, err := s.executionEnv(envCtx, cwd)
			if err != nil {
				return nil, err
			}
			return &sessionPromptEnv{ExecutionEnv: exec, files: s.Files}, nil
		},
		OnReport: func(report durable.Report) {
			slog.Error("agent execution", "agent", agentID, "type", report.Kind, "error", report.Error)
		},
	})
	if err != nil {
		return nil, err
	}
	return &agentResources{harness: h, store: store, loaded: loaded, execStamp: execStamp, envs: envs}, nil
}

// Withdrawal and abort marks commit before the paused Harness can run saved work.
func isolateConversations(ctx context.Context, h harness.Harness, isolated map[durable.ConversationId]bool) error {
	for id := range isolated {
		conversation, err := h.Conversation(ctx, id)
		if err == nil && conversation != nil {
			err = withdrawQueuedInputs(ctx, conversation)
		}
		if err != nil {
			return err
		}
	}
	state, err := h.Inspect(ctx)
	if err != nil {
		return err
	}
	for _, task := range state.Tasks {
		if isolated[task.Record.ConversationId] {
			if _, err := h.AbortTask(ctx, task.Record.Id); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *Agents) conversation(ctx context.Context, ref Ref) (harness.Conversation, error) {
	if ref.ID <= 0 {
		return nil, fmt.Errorf("%w: conversation ID", ErrInvalidInput)
	}
	h, err := a.harness(ctx, ref.AgentID, ref.Scope)
	if err != nil {
		return nil, err
	}
	conversation, err := h.Conversation(ctx, durable.ConversationId(ref.ID))
	if err != nil {
		return nil, err
	}
	if conversation == nil {
		return nil, ErrNotFound
	}
	return conversation, nil
}

func agentChange(options Options) harness.AgentChange {
	change := harness.AgentChange{Model: harness.SetTo(durable.ModelRef{Provider: options.Model.Provider, ModelId: options.Model.ModelID}), ThinkingLevel: harness.SetTo(ai.ModelThinkingLevel(options.ThinkingLevel)), Instructions: harness.Cleared[string](), Cwd: harness.Cleared[string]()}
	if options.Cwd != "" {
		change.Cwd = harness.SetTo(options.Cwd)
	}
	if options.Instructions != "" {
		change.Instructions = harness.SetTo(options.Instructions)
	}
	return change
}

// agentChange adds the project AGENTS.md from the session's execution environment.
// It reads the file at each admission so an edit applies on the next input.
func (a *Agents) agentChange(ctx context.Context, ref Ref, options Options) harness.AgentChange {
	files := a.files(ref.AgentID, nil)
	if envs := a.environments(ref.AgentID); envs != nil {
		// An unavailable environment has no project AGENTS.md to read; its tools fail at the call.
		s, err := envs.session(ctx, ref.ID)
		if err != nil {
			return agentChange(options)
		}
		files = s.Files
	}
	options.Instructions = files.instructionsContext(ctx, options.Cwd, options.Instructions)
	return agentChange(options)
}

func (a *Agents) Create(ctx context.Context, agentID string, options Options) (Ref, error) {
	key := resourceKey(Ref{AgentID: agentID, Scope: options.Scope})
	if err := a.begin(key); err != nil {
		return Ref{}, err
	}
	defer a.end(key)
	h, err := a.harness(ctx, agentID, options.Scope)
	if err != nil {
		return Ref{}, err
	}
	change := a.changeFor(ctx, Ref{AgentID: agentID, Scope: options.Scope}, options)
	conversation, err := h.CreateConversation(ctx, harness.ConversationCreateOptions{Ownership: durable.ConversationOwnership{Kind: durable.ConversationOwnerless}, Agent: &change})
	if err != nil {
		return Ref{}, err
	}
	return Ref{AgentID: agentID, Scope: options.Scope, ID: int64(conversation.Id())}, nil
}

// Fork copies the committed history of ref into a new native conversation, which keeps the
// source's configuration until the next admission configures it. Without before, the copy ends
// at entryID, which must be an answer that waits for no tool result. With before, the copy ends
// just before entryID; ok is false when nothing comes before it. A non-nil move gets the
// environment that the copied history last ran in, or "" when that is the environment of ref. A
// non-empty Move.Text ends the copy as a context entry, so the transcript shows it and the model
// reads it before the next input.
func (a *Agents) Fork(ctx context.Context, ref Ref, entryID int64, before bool, move func(ranIn string) (Move, error)) (forked Ref, ok bool, err error) {
	if err := a.begin(resourceKey(ref)); err != nil {
		return Ref{}, false, err
	}
	defer a.end(resourceKey(ref))
	conversation, err := a.conversation(ctx, ref)
	if err != nil {
		return Ref{}, false, err
	}
	// Entries lists the history newest first, so the first entry up to an ID is that entry when
	// the conversation can see it.
	latest := func(id int64) (*durable.EntryRecord, error) {
		page, err := conversation.Entries(ctx, durable.EntryQuery{MaxEntryId: new(durable.EntryId(id))}, 1, nil)
		if err != nil || len(page.Items) == 0 {
			return nil, err
		}
		return &page.Items[0], nil
	}
	if entryID == 0 && !before {
		// Zero means the newest finished answer.
		var cursor durable.Cursor
	scan:
		for {
			page, err := conversation.Entries(ctx, durable.EntryQuery{}, 100, cursor)
			if err != nil {
				return Ref{}, false, err
			}
			for _, e := range page.Items {
				if finishedAnswer(e) {
					entryID = int64(e.Id)
					break scan
				}
			}
			if page.Next == nil {
				return Ref{}, false, fmt.Errorf("%w: the session has no finished answer to fork from", ErrInvalidInput)
			}
			cursor = *page.Next
		}
	}
	entry, err := latest(entryID)
	if err != nil {
		return Ref{}, false, err
	}
	if entry == nil || int64(entry.Id) != entryID {
		return Ref{}, false, fmt.Errorf("%w: entry %d is not in this chat", ErrInvalidInput, entryID)
	}
	at := entry.Id
	if before {
		previous, err := latest(entryID - 1)
		if err != nil || previous == nil {
			return Ref{}, false, err
		}
		at = previous.Id
	} else if !finishedAnswer(*entry) {
		return Ref{}, false, fmt.Errorf("%w: a fork must start from a finished answer", ErrInvalidInput)
	}
	var m Move
	if move != nil {
		ranIn, err := excludedMove(ctx, conversation, at)
		if err != nil {
			return Ref{}, false, err
		}
		if m, err = move(ranIn); err != nil {
			return Ref{}, false, err
		}
	}
	copied, err := conversation.Fork(ctx, at, harness.ConversationCreateOptions{Ownership: durable.ConversationOwnership{Kind: durable.ConversationOwnerless}})
	if err != nil {
		return Ref{}, false, err
	}
	if m.Text != "" {
		data, err := durable.ToJsonValue(environmentMove{From: m.From})
		if err != nil {
			return Ref{}, false, err
		}
		// The copy has no owner yet, so no input can come before the notice.
		_, err = copied.Commit(ctx, func(tx durable.Tx) (any, error) {
			return tx.AppendEntry(copied.Id(), durable.EntryDraft{Kind: environmentNoticeKind, Data: data, Model: []ai.Message{ai.UserMessage{Content: ai.UserText(m.Text), Timestamp: time.Now().UnixMilli()}}})
		})
		if err != nil {
			return Ref{}, false, err
		}
	}
	return Ref{AgentID: ref.AgentID, Scope: ref.Scope, ID: int64(copied.Id())}, true, nil
}

// Move is the environment change at a fork. From is the environment that the copied history last
// ran in. A non-empty Text is the notice that ends the copied history.
type Move struct {
	From, Text string
}

// environmentNoticeKind is the context entry that tells the user and the model that a fork runs
// in another execution environment than its source.
const environmentNoticeKind = "dex.environment"

// environmentMove is the data of an environment notice.
type environmentMove struct {
	From string `json:"from"`
}

// excludedMove returns the environment that the history up to at last ran in, when a later
// environment notice of the conversation is not in that history. The oldest such notice tells it.
// It returns "" when the history ran in the environment of the conversation.
func excludedMove(ctx context.Context, conversation harness.Conversation, at durable.EntryId) (string, error) {
	ranIn := ""
	var cursor durable.Cursor
	for {
		page, err := conversation.Entries(ctx, durable.EntryQuery{MinEntryId: new(at + 1)}, 100, cursor)
		if err != nil {
			return "", err
		}
		for _, e := range page.Items {
			if e.Kind != environmentNoticeKind {
				continue
			}
			notice, err := durable.FromJsonValue[environmentMove](e.Data)
			if err != nil {
				return "", fmt.Errorf("environment notice %d: %w", e.Id, err)
			}
			ranIn = notice.From
		}
		if page.Next == nil {
			return ranIn, nil
		}
		cursor = *page.Next
	}
}

// finishedAnswer reports an assistant message without tool calls. A fork after a tool call would
// copy a call whose result never comes.
func finishedAnswer(entry durable.EntryRecord) bool {
	if len(entry.Model) != 1 {
		return false
	}
	answer, ok := entry.Model[0].(ai.AssistantMessage)
	if !ok {
		return false
	}
	for _, block := range answer.Content {
		if _, ok := block.(ai.ToolCall); ok {
			return false
		}
	}
	return true
}

func (a *Agents) checkIdle(ctx context.Context, conversation harness.Conversation) error {
	_, err := conversation.Commit(ctx, func(tx durable.Tx) (any, error) {
		live, err := durable.TxDoc[harness.LiveState](tx, harness.LiveDoc, conversation.Id())
		if err != nil {
			return nil, err
		}
		inbox, err := durable.TxDoc[harness.InboxState](tx, harness.InboxDoc, conversation.Id())
		if err != nil {
			return nil, err
		}
		state, err := durable.DecodeDoc[harness.LiveState](live.Snapshot())
		if err != nil {
			return nil, err
		}
		queue, err := durable.DecodeDoc[harness.InboxState](inbox.Snapshot())
		if err != nil {
			return nil, err
		}
		if state.Run != nil || len(queue.Items) > 0 {
			return nil, ErrBusy
		}
		return nil, nil
	})
	return err
}

func (a *Agents) CheckIdle(ctx context.Context, ref Ref) error {
	if err := a.begin(resourceKey(ref)); err != nil {
		return err
	}
	defer a.end(resourceKey(ref))
	unlock, err := a.lock(ctx, ref)
	if err != nil {
		return err
	}
	defer unlock()
	conversation, err := a.conversation(ctx, ref)
	if err != nil {
		return err
	}
	return a.checkIdle(ctx, conversation)
}

// Reset starts a new context. A handoff becomes the first user message of that context; it does
// not start a run, and the next run reads it.
func (a *Agents) Reset(ctx context.Context, ref Ref, requestID, handoff string) error {
	if requestID == "" {
		return fmt.Errorf("%w: reset request ID", ErrInvalidInput)
	}
	if err := a.begin(resourceKey(ref)); err != nil {
		return err
	}
	defer a.end(resourceKey(ref))
	unlock, err := a.lock(ctx, ref)
	if err != nil {
		return err
	}
	defer unlock()
	conversation, err := a.conversation(ctx, ref)
	if err != nil {
		return err
	}
	var existing *durable.SubmissionRecord
	_, err = conversation.Commit(ctx, func(tx durable.Tx) (any, error) {
		var err error
		existing, err = tx.SubmissionByRequest(conversation.Id(), requestID)
		return nil, err
	})
	if err != nil {
		return err
	}
	if existing == nil {
		if err := a.checkIdle(ctx, conversation); err != nil {
			return err
		}
	}
	entry := durable.EntryDraft{Kind: durable.ResetEntry.Kind, HeadSelf: true}
	if handoff != "" {
		entry.Model = []ai.Message{ai.UserMessage{Content: ai.UserText(handoff), Timestamp: time.Now().UnixMilli()}}
	}
	submission, err := conversation.Submit(ctx, durable.SubmissionDraft{Type: durable.SubmissionTypeWrite, RequestId: &requestID, Entry: &entry})
	if err != nil {
		return err
	}
	_, err = submission.Wait(ctx)
	return err
}

// Continue starts a run on the current context without new input, for example after a failed or
// stopped run. A request ID that exists already starts nothing. It returns ErrBusy while a run is
// active. A context that ends in an answer gives a run that fails with nothing_to_answer.
func (a *Agents) Continue(ctx context.Context, ref Ref, requestID string, options Options) error {
	if requestID == "" {
		return fmt.Errorf("%w: request ID is required", ErrInvalidInput)
	}
	if options.Model.ModelID == "" {
		return ErrNoModel
	}
	if err := a.begin(resourceKey(ref)); err != nil {
		return err
	}
	defer a.end(resourceKey(ref))
	unlock, err := a.lock(ctx, ref)
	if err != nil {
		return err
	}
	defer unlock()
	conversation, err := a.conversation(ctx, ref)
	if err != nil {
		return err
	}
	// The user can choose another model after a model error; the continued run uses it.
	if err := conversation.Configure(ctx, a.changeFor(ctx, ref, options)); err != nil {
		return err
	}
	_, err = conversation.Submit(ctx, durable.SubmissionDraft{Type: durable.SubmissionTypeContinue, RequestId: &requestID})
	if _, ok := errors.AsType[*durable.ConversationBusy](err); ok {
		return errors.Join(ErrBusy, err)
	}
	return err
}

// Compact starts a manual compaction and returns its native task ID. It runs beside an active run;
// a summary made during a run joins the context at the run's next boundary. Instructions tell the
// summary what to keep.
func (a *Agents) Compact(ctx context.Context, ref Ref, instructions, requestID string, options Options) error {
	if options.Model.ModelID == "" {
		return ErrNoModel
	}
	if err := a.begin(resourceKey(ref)); err != nil {
		return err
	}
	defer a.end(resourceKey(ref))
	unlock, err := a.lock(ctx, ref)
	if err != nil {
		return err
	}
	defer unlock()
	conversation, err := a.conversation(ctx, ref)
	if err != nil {
		return err
	}
	// The summary uses the conversation model, so it must be the chat's current choice.
	if err := conversation.Configure(ctx, a.changeFor(ctx, ref, options)); err != nil {
		return err
	}
	h, err := a.harness(ctx, ref.AgentID, ref.Scope)
	if err != nil {
		return err
	}
	var id durable.TaskId
	if requestID == "" {
		var keep *string
		if instructions != "" {
			keep = &instructions
		}
		id, err = conversation.Compact(ctx, keep)
	} else {
		var admission compactAdmission
		admission, err = compactOnce(ctx, conversation, durable.ConversationId(ref.ID), requestID, instructions)
		id = admission.id
		if err == nil && !admission.created {
			return nil
		}
	}
	if err != nil {
		return err
	}
	a.settle(resourceKey(ref), func() {
		if err := noteCompaction(context.WithoutCancel(ctx), h, conversation, id); err != nil {
			slog.Error("note compaction outcome", "agent", ref.AgentID, "conversation", ref.ID, "task", id, "error", err)
		}
	})
	return nil
}

// compactionNoteKind is the transcript entry for a manual compaction that made no summary. A
// summary is its own pi.compaction entry, so a note is only written for the other outcomes.
const compactionNoteKind = "dex.compaction"

type compactionNote struct {
	// Outcome is "nothing" when the context had nothing to compact, otherwise "failed".
	Outcome string `json:"outcome"`
	Error   string `json:"error,omitempty"`
}

// noteCompaction waits for a manual compaction and records an outcome that leaves no summary.
// The note is a write submission, so during a run it joins at the next boundary. A process
// restart before the task ends loses the note; the compaction itself still completes.
func noteCompaction(ctx context.Context, h harness.Harness, conversation harness.Conversation, id durable.TaskId) error {
	settled, err := h.WaitForTask(ctx, id)
	if err != nil {
		return err
	}
	outcome := settled.State.Outcome
	if outcome == nil {
		return fmt.Errorf("compaction task %d settled without an outcome", id)
	}
	note := compactionNote{Outcome: "failed"}
	switch outcome.Status {
	case durable.OutcomeCompleted:
		if outcome.Result == nil {
			return fmt.Errorf("compaction task %d completed without a result", id)
		}
		result, err := durable.FromJsonValue[harness.CompactionResult](*outcome.Result)
		if err != nil {
			return err
		}
		if result.EntryId != nil {
			return nil
		}
		if result.SubmissionId != nil {
			// A busy chat places the summary at the next boundary. A reset or another summary placed
			// before it makes it stale, and nothing else would tell the user.
			placed, err := waitSubmission(ctx, h, *result.SubmissionId)
			if err != nil || placed.Status == durable.SubmissionDone {
				return err
			}
			reason := ""
			if placed.Reason != nil {
				reason = *placed.Reason
			}
			switch reason {
			case "aborted":
				note.Error = "Stopped before the summary was added. Run /compact again."
			case "stale":
				note.Error = "The context changed before the summary was ready, so the summary was not used."
			default:
				note.Error = "The summary was not added: " + reason
			}
			break
		}
		note.Outcome = "nothing"
	case durable.OutcomeAborted:
		note.Error = "Stopped."
	case durable.OutcomeFailed, durable.OutcomeFaulted, durable.OutcomeOrphaned:
		note.Error = string(outcome.Status)
		if outcome.Error != nil {
			note.Error = outcome.Error.Message
		}
	}
	data, err := durable.ToJsonValue(note)
	if err != nil {
		return err
	}
	requestID := fmt.Sprintf("dex.compaction:%d", id)
	_, err = conversation.Submit(ctx, durable.SubmissionDraft{Type: durable.SubmissionTypeWrite, RequestId: &requestID, Entry: &durable.EntryDraft{Kind: compactionNoteKind, Data: data}})
	return err
}

func waitSubmission(ctx context.Context, h harness.Harness, id durable.SubmissionId) (durable.SettledSubmissionRecord, error) {
	submission, err := h.Submission(ctx, id)
	if err != nil {
		return durable.SettledSubmissionRecord{}, err
	}
	return submission.Wait(ctx)
}

func (a *Agents) Abort(ctx context.Context, ref Ref) error {
	if ref.Scope != "" {
		if err := validateGroupScope(ref.Scope); err != nil {
			return err
		}
	}
	if !agentIDPattern.MatchString(ref.AgentID) || ref.ID <= 0 {
		return fmt.Errorf("%w: cancellation reference", ErrInvalidInput)
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return ErrClosed
	}
	// Stop may cancel open work while maintenance blocks new admission.
	key := resourceKey(ref)
	a.trackLocked(key)
	if a.resources[key] == nil || a.opening[key] != nil {
		if a.recovering[key] == nil {
			a.recovering[key] = map[durable.ConversationId]bool{}
		}
		// Opening must isolate stored work before Harness.Resume can start it.
		a.recovering[key][durable.ConversationId(ref.ID)] = true
	}
	a.mu.Unlock()
	defer a.end(key)
	unlock, err := a.lock(ctx, ref)
	if err != nil {
		return err
	}
	defer unlock()
	conversation, err := a.conversation(ctx, ref)
	if err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := withdrawQueuedInputs(bounded, conversation); err != nil {
		return err
	}
	if err := conversation.Abort(bounded, &durable.ConversationAbortOptions{Background: true}); err != nil {
		return err
	}
	return a.dropQueuedSummaries(bounded, ref, conversation)
}

// dropQueuedSummaries withdraws the summaries that wait for a boundary. Native abort keeps queued
// writes for the next run, so a summary made during the stopped run would wait for the next
// message and hold off maintenance until then. The compaction note reports the drop.
func (a *Agents) dropQueuedSummaries(ctx context.Context, ref Ref, conversation harness.Conversation) error {
	var queue *harness.InboxState
	_, err := conversation.Commit(ctx, func(tx durable.Tx) (any, error) {
		inbox, err := durable.TxDoc[harness.InboxState](tx, harness.InboxDoc, conversation.Id())
		if err != nil {
			return nil, err
		}
		queue, err = durable.DecodeDoc[harness.InboxState](inbox.Snapshot())
		return nil, err
	})
	if err != nil {
		return err
	}
	h, err := a.harness(ctx, ref.AgentID, ref.Scope)
	if err != nil {
		return err
	}
	id := conversation.Id()
	for _, item := range queue.Items {
		if item.Mode != harness.InboxWrite || item.Entry == nil {
			continue
		}
		if kind, _ := item.Entry.Get("kind"); kind != durable.CompactionEntry.Kind {
			continue
		}
		if _, err := h.AbortSubmission(ctx, item.Id, &id); err != nil {
			return err
		}
	}
	return nil
}

// Queue settlement and passive display entries commit together before recovery resumes.
func withdrawQueuedInputs(ctx context.Context, conversation harness.Conversation) error {
	_, err := conversation.Commit(ctx, func(tx durable.Tx) (any, error) {
		inbox, err := durable.TxDoc[harness.InboxState](tx, harness.InboxDoc, conversation.Id())
		if err != nil {
			return nil, err
		}
		queue, err := durable.DecodeDoc[harness.InboxState](inbox.Snapshot())
		if err != nil {
			return nil, err
		}
		if err := harness.WithdrawQueuedInputs(tx, conversation.Id()); err != nil {
			return nil, err
		}
		for _, item := range queue.Items {
			if item.Mode == harness.InboxWrite {
				continue
			}
			metadata := inputMetadata{Reason: "aborted"}
			if item.Entry != nil {
				data, _ := item.Entry.Get("data")
				encoded, err := json.Marshal(data)
				if err != nil {
					return nil, err
				}
				if err := json.Unmarshal(encoded, &metadata); err != nil {
					return nil, err
				}
				metadata.Reason = "aborted"
			}
			data, err := durable.ToJsonValue(metadata)
			if err != nil {
				return nil, err
			}
			if _, err := tx.AppendEntry(conversation.Id(), durable.EntryDraft{Kind: "dex.withdrawn", Data: data}); err != nil {
				return nil, err
			}
		}
		return nil, nil
	})
	return err
}

func (a *Agents) Quiesce(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return ErrClosed
	}
	if a.paused {
		return ErrMaintenance
	}
	if err := a.idleLocked(ctx); err != nil {
		return err
	}
	a.paused = true
	return nil
}

// The caller holds mu so the idle check and resource registration stay atomic.
// Inspect reads local storage; use a tracked preflight if those reads delay other agents.
func (a *Agents) idleLocked(ctx context.Context) error {
	if len(a.busy) > 0 {
		return ErrBusy
	}
	for _, resource := range a.resources {
		state, err := resource.harness.Inspect(ctx)
		if err != nil {
			return err
		}
		if len(state.Tasks) > 0 || len(state.Submissions) > 0 {
			return ErrBusy
		}
	}
	return ctx.Err()
}

// agentIdleLocked reports ErrBusy while agentID opens, runs an operation or a settlement, or
// has native work. Work of other agents and of group scopes does not count: Reload replaces
// only the resources of agentID. The caller holds mu so the idle check and the registration
// of the replacement stay atomic.
func (a *Agents) agentIdleLocked(ctx context.Context, agentID string) error {
	if a.opening[agentID] != nil || a.busy[agentID] > 0 {
		return ErrBusy
	}
	if resource := a.resources[agentID]; resource != nil {
		state, err := resource.harness.Inspect(ctx)
		if err != nil {
			return err
		}
		if len(state.Tasks) > 0 || len(state.Submissions) > 0 {
			return ErrBusy
		}
	}
	return ctx.Err()
}

func (a *Agents) Resume() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.closed {
		a.paused = false
		if len(a.dirty) != 0 {
			a.wakeReloads()
		}
	}
}

func (a *Agents) Close(ctx context.Context) error {
	a.mu.Lock()
	if !a.closed {
		a.closed = true
		for _, opening := range a.opening {
			opening.cancel()
		}
		go a.close(context.WithoutCancel(ctx))
	}
	done := a.closeDone
	a.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return a.closeErr
	}
}
func (a *Agents) close(ctx context.Context) {
	var errs []error
	a.Catalog.cancelDiscovery()
	a.operations.Wait()
	a.mu.Lock()
	resources := maps.Clone(a.resources)
	a.mu.Unlock()
	for _, resource := range resources {
		errs = append(errs, resource.harness.Close(ctx))
	}
	a.settlements.Wait()
	// Harnesses must stop starting model streams before Models.Close waits for them.
	errs = append(errs, a.Catalog.Close())
	for _, resource := range resources {
		errs = append(errs, resource.closeExtras(ctx))
	}
	errs = append(errs, a.codeSandbox.Close(), a.config.ExecEnv.Close(ctx))
	a.closeErr = errors.Join(errs...)
	close(a.closeDone)
}

func (a *Agents) files(agentID string, handle execenv.Handle) *agentExtensions {
	files := newAgentExtensions(a.config.Home, a.config.DataDir, agentID, handle, a.env)
	files.mcp = a.config.MCPServers
	files.vault = a.vault
	files.onSkillChange = func(scope string) { a.markExtensionChange(agentID, scope, "skill write") }
	for _, level := range files.levels {
		files.stores[level.Scope] = a.skillStore(level)
	}
	return files
}

// ReadWorkspaceFile reads up to maxBytes of a workspace-relative regular non-symlink file in the
// environment of the calling session.
func (a *Agents) ReadWorkspaceFile(ctx context.Context, caller TaskContext, relPath string, maxBytes int64) ([]byte, error) {
	environmentID, err := a.sessionEnvironmentID(ctx, Ref{AgentID: caller.AgentID, Scope: caller.Scope, ID: caller.ConversationID})
	if err != nil {
		return nil, err
	}
	var data []byte
	err = a.withEnvironment(ctx, caller.AgentID, environmentID, func(h execenv.Handle) error {
		var readErr error
		data, readErr = execenv.ReadWorkspaceFile(ctx, h, relPath, maxBytes)
		return readErr
	})
	return data, err
}

// ResolveProjectPath validates and normalizes a project path inside an execution environment of
// the agent. An empty environmentID selects the agent's own environment.
func (a *Agents) ResolveProjectPath(ctx context.Context, agentID, environmentID, raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	var resolved string
	err := a.withEnvironment(ctx, agentID, environmentID, func(h execenv.Handle) error {
		var err error
		resolved, err = execenv.ResolveProjectPath(ctx, h, raw)
		return err
	})
	return resolved, err
}

// FilterProjectPaths keeps up to limit existing directory paths from paths inside an environment of the agent.
func (a *Agents) FilterProjectPaths(ctx context.Context, agentID, environmentID string, paths []string, limit int) ([]string, error) {
	if len(paths) == 0 || limit <= 0 {
		return []string{}, nil
	}
	var existing []string
	err := a.withEnvironment(ctx, agentID, environmentID, func(h execenv.Handle) error {
		for _, p := range paths {
			if _, resErr := execenv.ResolveProjectPath(ctx, h, p); resErr == nil {
				existing = append(existing, p)
				if len(existing) >= limit {
					break
				}
			}
		}
		return nil
	})
	if existing == nil {
		existing = []string{}
	}
	return existing, err
}

// ListDirectories lists browsable directories in an execution environment of the agent.
func (a *Agents) ListDirectories(ctx context.Context, agentID, environmentID string, path *string) (execenv.DirectoryListing, error) {
	var listing execenv.DirectoryListing
	err := a.withEnvironment(ctx, agentID, environmentID, func(h execenv.Handle) error {
		var listErr error
		listing, listErr = execenv.ListDirectories(ctx, h, path)
		return listErr
	})
	return listing, err
}

// ExecEnv opens an ExecutionEnv for background or ad-hoc execution in an environment of the agent.
// An empty environmentID selects the agent's own environment. Callers must invoke release once finished.
func (a *Agents) ExecEnv(ctx context.Context, agentID, environmentID, cwd string, extraEnv map[string]string) (durableenv.ExecutionEnv, string, execenv.Kind, func(), error) {
	if !agentIDPattern.MatchString(agentID) {
		return nil, "", "", nil, ErrInvalidInput
	}
	if err := a.begin(agentID); err != nil {
		return nil, "", "", nil, err
	}
	defer a.end(agentID)
	if envs := a.environments(agentID); envs != nil {
		s, err := envs.byID(ctx, environmentID)
		if err != nil {
			return nil, "", "", nil, err
		}
		env, err := s.Handle.Env(ctx, cwd, extraEnv)
		if err != nil {
			return nil, "", "", nil, err
		}
		return env, s.Handle.Workspace(), s.Handle.Kind(), func() {}, nil
	}
	h, err := a.config.ExecEnv.Open(ctx, agentID, environmentID)
	if err != nil {
		return nil, "", "", nil, err
	}
	env, err := h.Env(ctx, cwd, extraEnv)
	if err != nil {
		_ = h.Close(context.WithoutCancel(ctx))
		return nil, "", "", nil, err
	}
	closeCtx := context.WithoutCancel(ctx)
	return env, h.Workspace(), h.Kind(), func() { _ = h.Close(closeCtx) }, nil
}

// SetOnePassword changes the 1Password credentials of tools. Open agents become stale and
// apply them at their next reload.
func (a *Agents) SetOnePassword(op OnePassword) {
	if a.env.setOnePassword(op) {
		a.markExtensionChange("", "dex", "1Password settings")
	}
}

// OnePasswordCLI returns the path of op in the PATH of the base tool environment, or "". It
// captures the login shell first when no agent has, because the service PATH can lack op.
func (a *Agents) OnePasswordCLI(ctx context.Context) string {
	env, _ := a.env.load(ctx)
	return findOp(env)
}

func (a *Agents) Extensions(ctx context.Context, agentID string) (ExtensionInfo, error) {
	if !agentIDPattern.MatchString(agentID) {
		return ExtensionInfo{}, ErrInvalidInput
	}
	if err := a.begin(agentID); err != nil {
		return ExtensionInfo{}, err
	}
	defer a.end(agentID)
	a.mu.Lock()
	resource := a.resources[agentID]
	pending := a.dirty[agentID] != "" || a.opening[agentID] != nil
	a.mu.Unlock()
	var info ExtensionInfo
	var err, stampErr error
	var currentExec string
	var restoreMCP bool
	if resource != nil {
		currentExec, stampErr = a.config.ExecEnv.Stamp(ctx, agentID, "")
		files, needsReload := resource.envs.defaultFiles()
		restoreMCP = needsReload
		if stampErr != nil || currentExec != resource.execStamp {
			// Show the stored configuration, not project skills from a stale handle.
			files = a.files(agentID, nil)
			files.defaultUnavailable = true
		}
		info, err = files.Info(ctx)
	} else {
		err = a.withEnvironment(ctx, agentID, "", func(h execenv.Handle) error {
			var infoErr error
			info, infoErr = a.files(agentID, h).Info(ctx)
			return infoErr
		})
	}
	if err != nil {
		return info, err
	}
	if resource != nil {
		info.Servers = resource.loaded.serverInfo()
		pending = pending || restoreMCP || a.files(agentID, nil).stamp() != resource.loaded.stamp || (stampErr == nil && currentExec != resource.execStamp) || resource.envs.stale(ctx)
	}
	info.ReloadPending = pending
	return info, nil
}

func (a *Agents) Reload(ctx context.Context, agentID string) error {
	return a.reload(ctx, agentID, false)
}

func (a *Agents) reload(ctx context.Context, agentID string, automatic bool) (resultErr error) {
	if !agentIDPattern.MatchString(agentID) {
		return ErrInvalidInput
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return ErrClosed
	}
	if a.paused {
		a.mu.Unlock()
		return ErrMaintenance
	}
	reason := a.dirty[agentID]
	if automatic && reason == "" {
		a.mu.Unlock()
		return nil
	}
	if err := a.agentIdleLocked(ctx, agentID); err != nil {
		a.mu.Unlock()
		return err
	}
	if err := a.Catalog.discoveryReady(); err != nil {
		a.mu.Unlock()
		return err
	}
	refused := a.refused[agentID]
	delete(a.dirty, agentID)
	openCtx, cancel := context.WithCancel(ctx)
	opening := &agentOpening{done: make(chan struct{}), cancel: cancel}
	a.opening[agentID] = opening
	a.trackLocked(agentID)
	a.mu.Unlock()
	defer a.end(agentID)
	files := a.files(agentID, nil)
	execStamp, _ := a.config.ExecEnv.Stamp(openCtx, agentID, "")
	stamp := files.stamp() + "|" + execStamp
	if automatic && refused == stamp {
		a.finishOpening(agentID, opening)
		return nil
	}
	defer func() {
		a.mu.Lock()
		switch {
		case resultErr == nil:
			delete(a.refused, agentID)
		case errors.Is(resultErr, ErrInvalidInput):
			a.refused[agentID] = stamp
		}
		a.mu.Unlock()
		if automatic {
			switch {
			case resultErr == nil:
				slog.Info("reloaded extensions after a change", "agent", agentID, "reason", reason)
			case errors.Is(resultErr, ErrInvalidInput):
				slog.Warn("extension change not applied; fix the configuration", "agent", agentID, "reason", reason)
			case ctx.Err() == nil:
				slog.Warn("reload extensions", "agent", agentID, "reason", reason, "err", resultErr)
			}
		}
	}()
	// Preflight errors leave the loaded target intact.
	if err := files.Validate(openCtx); err != nil {
		a.finishOpening(agentID, opening)
		return a.closedDuring(ctx, err)
	}
	a.mu.Lock()
	old := a.resources[agentID]
	delete(a.resources, agentID)
	a.mu.Unlock()
	if old != nil {
		cleanup := context.WithoutCancel(openCtx)
		if err := errors.Join(old.harness.Close(cleanup), old.closeExtras(cleanup)); err != nil {
			a.finishOpening(agentID, opening)
			return err
		}
	}
	_, err := a.openAndPublish(openCtx, agentID, opening)
	return a.closedDuring(ctx, err)
}

func (a *Agents) finishOpening(agentID string, opening *agentOpening) {
	a.mu.Lock()
	delete(a.opening, agentID)
	close(opening.done)
	a.mu.Unlock()
	opening.cancel()
}

// CloseAgent verifies agentID has no active tasks, submissions, or openings, then closes and removes
// its open runtime resources so the next turn opens a fresh execution handle.
func (a *Agents) CloseAgent(ctx context.Context, agentID string) error {
	if !agentIDPattern.MatchString(agentID) {
		return ErrInvalidInput
	}
	matchKey := func(key string) bool { return key == agentID || strings.HasPrefix(key, agentID+"/") }
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return ErrClosed
	}
	for key := range a.opening {
		if matchKey(key) {
			a.mu.Unlock()
			return ErrBusy
		}
	}
	var keys []string
	for key := range a.resources {
		if matchKey(key) {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		a.mu.Unlock()
		return ctx.Err()
	}
	_, cancel := context.WithCancel(ctx)
	closing := &agentOpening{done: make(chan struct{}), cancel: cancel}
	openKeys := append([]string{agentID}, keys...)
	for _, key := range openKeys {
		a.opening[key] = closing
	}
	a.trackLocked(agentID)
	resources := make([]*agentResources, 0, len(keys))
	for _, key := range keys {
		resources = append(resources, a.resources[key])
	}
	a.mu.Unlock()
	defer a.end(agentID)
	defer func() {
		a.mu.Lock()
		for _, key := range openKeys {
			delete(a.opening, key)
		}
		close(closing.done)
		a.mu.Unlock()
		closing.cancel()
	}()

	for _, res := range resources {
		state, err := res.harness.Inspect(ctx)
		if err != nil {
			return err
		}
		if len(state.Tasks) > 0 || len(state.Submissions) > 0 {
			return ErrBusy
		}
	}
	a.mu.Lock()
	for _, key := range keys {
		delete(a.resources, key)
	}
	delete(a.dirty, agentID)
	delete(a.refused, agentID)
	a.mu.Unlock()

	cleanupCtx := context.WithoutCancel(ctx)
	var errs []error
	for _, res := range resources {
		errs = append(errs, res.harness.Close(cleanupCtx), res.closeExtras(cleanupCtx))
	}
	return errors.Join(errs...)
}

func (a *Agents) source(ctx context.Context, ref Ref) (string, error) {
	conversation, err := a.conversation(ctx, ref)
	if err != nil {
		return "", err
	}
	var cursor durable.Cursor
	for {
		page, err := conversation.Entries(ctx, durable.EntryQuery{}, 100, cursor)
		if err != nil {
			return "", err
		}
		for _, entry := range page.Items {
			if entry.Kind != "dex.user" && entry.Kind != "dex.task_result" {
				continue
			}
			encoded, err := json.Marshal(entry.Data)
			if err != nil {
				return "", err
			}
			var metadata inputMetadata
			if err := json.Unmarshal(encoded, &metadata); err != nil {
				return "", err
			}
			if metadata.Source != "" {
				return metadata.Source, nil
			}
		}
		if page.Next == nil {
			return "", nil
		}
		cursor = *page.Next
	}
}

// Health reports lifecycle failures without opening an agent or changing admission.
func (a *Agents) Health(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return ErrClosed
	}
	for id, resource := range a.resources {
		select {
		case <-resource.harness.Closed():
			end := resource.harness.End()
			if end.Error != nil {
				return fmt.Errorf("agent %s: %w", id, end.Error)
			}
			return fmt.Errorf("agent %s: %w", id, ErrClosed)
		default:
		}
	}
	return nil
}
