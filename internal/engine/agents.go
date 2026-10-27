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
	"sync"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/chord"
	"github.com/MichaelKinsy/PiG/durable"
	durableenv "github.com/MichaelKinsy/PiG/durable/env"
	envnode "github.com/MichaelKinsy/PiG/durable/env/node"
	"github.com/MichaelKinsy/PiG/durable/harness"
	storage "github.com/MichaelKinsy/PiG/durable/storage/sqlite/node"
	"github.com/MichaelKinsy/PiG/durable/toolsearch"
)

var configureReporter sync.Once

var agentIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type agentOpening struct {
	done   chan struct{}
	cancel context.CancelFunc
}

type agentResources struct {
	harness harness.Harness
	loaded  *loadedExtensions
}

// Agents owns the native execution resources. Harnesses stay open until Close or Reload.
type Agents struct {
	Catalog          *Catalog
	config           Config
	memory           *MemoryRepo
	mu               sync.Mutex
	resources        map[string]*agentResources
	opening          map[string]*agentOpening
	recovering       map[string]map[durable.ConversationId]bool
	serial           map[Ref]chan struct{}
	paused, closed   bool
	active, settling int
	operations       sync.WaitGroup
	settlements      sync.WaitGroup
	closeDone        chan struct{}
	closeErr         error
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
	return &Agents{Catalog: NewCatalog(config.FakeModel), config: config, memory: NewMemoryRepo(filepath.Join(config.DataDir, "memory"), config.BaseEnv), resources: map[string]*agentResources{}, recovering: map[string]map[durable.ConversationId]bool{}, opening: map[string]*agentOpening{}, serial: map[Ref]chan struct{}{}, closeDone: make(chan struct{})}, nil
}

func (a *Agents) begin() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return ErrClosed
	}
	if a.paused {
		return ErrMaintenance
	}
	a.operations.Add(1)
	a.active++
	return nil
}

func (a *Agents) end() { a.mu.Lock(); a.active--; a.mu.Unlock(); a.operations.Done() }

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
		if !agentIDPattern.MatchString(ref.AgentID) || ref.ID <= 0 {
			return fmt.Errorf("%w: recovery reference", ErrInvalidInput)
		}
	}
	for _, ref := range refs {
		if a.recovering[ref.AgentID] == nil {
			a.recovering[ref.AgentID] = map[durable.ConversationId]bool{}
		}
		a.recovering[ref.AgentID][durable.ConversationId(ref.ID)] = true
	}
	return nil
}

func (a *Agents) harness(ctx context.Context, agentID string) (harness.Harness, error) {
	if !agentIDPattern.MatchString(agentID) {
		return nil, fmt.Errorf("%w: agent ID", ErrInvalidInput)
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
			resultErr = errors.Join(resultErr, resource.harness.Close(cleanup), resource.loaded.Close(cleanup))
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
	if err := a.Catalog.DiscoveryReady(); err != nil {
		return nil, err
	}
	dir := filepath.Join(a.config.DataDir, "agents", agentID)
	workspace := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		return nil, err
	}
	if err := a.memory.Open(ctx); err != nil {
		return nil, err
	}
	loaded, err := a.files(agentID).Load(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, loaded.Close(context.WithoutCancel(ctx)))
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	registry := harness.CreateRegistry()
	extensions := []*durable.Extension{codingTools(loaded.Env), codingSkill(), routineExtension(agentID, a.config.Routines), taskExtension(agentID, a.config.Tasks), a.memory.Extension(agentID, func(ctx context.Context, id durable.ConversationId) (string, error) {
		return a.source(ctx, Ref{AgentID: agentID, ID: int64(id)})
	}), loaded.Extension}
	if loaded.HasTools {
		extensions = append(extensions, toolsearch.CreateExtension())
	}
	for _, extension := range extensions {
		if err := registry.Install(extension); err != nil {
			return nil, err
		}
	}
	if err := registry.Install(checkExtension(agentID, a.config.Tasks.ReportCheck)); err != nil {
		return nil, err
	}
	if err := registry.Install(taskReportExtension(agentID, a.config.Tasks.ReportTask)); err != nil {
		return nil, err
	}
	store, err := storage.OpenNodeSqliteStorage(filepath.Join(dir, "state.sqlite"), storage.NodeSqliteStorageOptions{})
	if err != nil {
		return nil, err
	}
	h, err := harness.OpenHarness(ctx, store, harness.HarnessOptions{
		Models: a.Catalog.Models, Registry: registry,
		Settings: func() *harness.HarnessSettings {
			return &harness.HarnessSettings{Extensions: extensions, AfterFailedRun: durable.AfterFailedRunContinue}
		},
		Env: func(_ context.Context, target harness.EnvTarget) (durableenv.ExecutionEnv, error) {
			cwd := workspace
			if target.Cwd != nil {
				cwd = *target.Cwd
			}
			return envnode.NewNodeExecutionEnv(envnode.NodeExecutionEnvOptions{Cwd: cwd, ShellEnv: loaded.Env}), nil
		},
		OnReport: func(err error, info durable.ReportInfo) {
			slog.Error("agent execution", "agent", agentID, "type", info.Type, "error", err)
		},
	})
	if err != nil {
		return nil, err
	}
	return &agentResources{harness: h, loaded: loaded}, nil
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
	h, err := a.harness(ctx, ref.AgentID)
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

func (a *Agents) Create(ctx context.Context, agentID string, options Options) (Ref, error) {
	if err := a.begin(); err != nil {
		return Ref{}, err
	}
	defer a.end()
	h, err := a.harness(ctx, agentID)
	if err != nil {
		return Ref{}, err
	}
	change := agentChange(options)
	conversation, err := h.CreateConversation(ctx, harness.ConversationCreateOptions{Ownership: durable.ConversationOwnership{Kind: durable.ConversationOwnerless}, Agent: &change})
	if err != nil {
		return Ref{}, err
	}
	return Ref{AgentID: agentID, ID: int64(conversation.Id())}, nil
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
	if err := a.begin(); err != nil {
		return err
	}
	defer a.end()
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

func (a *Agents) Reset(ctx context.Context, ref Ref, requestID string) error {
	if requestID == "" {
		return fmt.Errorf("%w: reset request ID", ErrInvalidInput)
	}
	if err := a.begin(); err != nil {
		return err
	}
	defer a.end()
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
	submission, err := conversation.Submit(ctx, durable.SubmissionDraft{Type: durable.SubmissionTypeWrite, RequestId: &requestID, Entry: &durable.EntryDraft{Kind: durable.ResetEntry.Kind, HeadSelf: true}})
	if err != nil {
		return err
	}
	_, err = submission.Wait(ctx)
	return err
}

func (a *Agents) Abort(ctx context.Context, ref Ref) error {
	if !agentIDPattern.MatchString(ref.AgentID) || ref.ID <= 0 {
		return fmt.Errorf("%w: cancellation reference", ErrInvalidInput)
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return ErrClosed
	}
	// Stop may cancel open work while maintenance blocks new admission.
	a.operations.Add(1)
	a.active++
	if a.resources[ref.AgentID] == nil || a.opening[ref.AgentID] != nil {
		if a.recovering[ref.AgentID] == nil {
			a.recovering[ref.AgentID] = map[durable.ConversationId]bool{}
		}
		// Opening must isolate stored work before Harness.Resume can start it.
		a.recovering[ref.AgentID][durable.ConversationId(ref.ID)] = true
	}
	a.mu.Unlock()
	defer a.end()
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
	return conversation.Abort(bounded, &durable.ConversationAbortOptions{Background: true})
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
			metadata := InputMetadata{Reason: "aborted"}
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
	if a.active > 0 || a.settling > 0 {
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

func (a *Agents) Resume() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.closed {
		a.paused = false
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
	errs = append(errs, a.Catalog.Close())
	a.operations.Wait()
	a.mu.Lock()
	resources := maps.Clone(a.resources)
	a.mu.Unlock()
	for _, resource := range resources {
		errs = append(errs, resource.harness.Close(ctx))
	}
	a.settlements.Wait()
	for _, resource := range resources {
		errs = append(errs, resource.loaded.Close(ctx))
	}
	a.closeErr = errors.Join(errs...)
	close(a.closeDone)
}

func (a *Agents) files(agentID string) *agentExtensions {
	return newAgentExtensions(a.config.Home, a.config.DataDir, agentID, a.config.BaseEnv)
}
func (a *Agents) Extensions(ctx context.Context, agentID string) (ExtensionInfo, error) {
	if !agentIDPattern.MatchString(agentID) {
		return ExtensionInfo{}, ErrInvalidInput
	}
	info, err := a.files(agentID).Info(ctx)
	if err != nil {
		return info, err
	}
	a.mu.Lock()
	resource := a.resources[agentID]
	a.mu.Unlock()
	if resource != nil {
		info.Servers = resource.loaded.ServerInfo()
	}
	return info, nil
}
func (a *Agents) EditEnv(ctx context.Context, agentID, scope, key string, value *string) (ExtensionInfo, error) {
	if !agentIDPattern.MatchString(agentID) {
		return ExtensionInfo{}, ErrInvalidInput
	}
	return a.files(agentID).EditEnv(ctx, scope, key, value)
}

func (a *Agents) Reload(ctx context.Context, agentID string) error {
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
	if err := a.idleLocked(ctx); err != nil {
		a.mu.Unlock()
		return err
	}
	openCtx, cancel := context.WithCancel(ctx)
	opening := &agentOpening{done: make(chan struct{}), cancel: cancel}
	a.opening[agentID] = opening
	a.operations.Add(1)
	a.active++
	a.mu.Unlock()
	defer a.end()
	// Preflight errors leave the loaded target intact.
	if err := a.Catalog.DiscoveryReady(); err != nil {
		a.finishOpening(agentID, opening)
		return err
	}
	if err := a.files(agentID).Validate(openCtx); err != nil {
		a.finishOpening(agentID, opening)
		return a.closedDuring(ctx, err)
	}
	a.mu.Lock()
	old := a.resources[agentID]
	delete(a.resources, agentID)
	a.mu.Unlock()
	if old != nil {
		cleanup := context.WithoutCancel(openCtx)
		if err := errors.Join(old.harness.Close(cleanup), old.loaded.Close(cleanup)); err != nil {
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
			var metadata InputMetadata
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
