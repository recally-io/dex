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
	recovering       map[string]map[durable.ConversationId]bool
	serial           map[Ref]*sync.Mutex
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
	return &Agents{Catalog: NewCatalog(config.FakeModel), config: config, memory: NewMemoryRepo(filepath.Join(config.DataDir, "memory"), config.BaseEnv), resources: map[string]*agentResources{}, recovering: map[string]map[durable.ConversationId]bool{}, serial: map[Ref]*sync.Mutex{}, closeDone: make(chan struct{})}, nil
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

func (a *Agents) lock(ref Ref) func() {
	a.mu.Lock()
	mutex := a.serial[ref]
	if mutex == nil {
		mutex = &sync.Mutex{}
		a.serial[ref] = mutex
	}
	a.mu.Unlock()
	mutex.Lock()
	return mutex.Unlock
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
	if len(a.resources) > 0 {
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
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, ErrClosed
	}
	if resource := a.resources[agentID]; resource != nil {
		return resource.harness, nil
	}
	if a.paused {
		return nil, ErrMaintenance
	}
	resource, err := a.open(ctx, agentID)
	if err != nil {
		return nil, err
	}
	a.resources[agentID] = resource
	resource.harness.Resume()
	return resource.harness, nil
}

// Open holds mu across agents; use per-agent opening locks if cold starts need concurrency.
func (a *Agents) open(ctx context.Context, agentID string) (_ *agentResources, resultErr error) {
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
	registry := harness.CreateRegistry()
	extensions := []*durable.Extension{codingTools(loaded.Env), codingSkill(), taskExtension(agentID, a.config.Tasks), a.memory.Extension(agentID, func(ctx context.Context, id durable.ConversationId) (string, error) {
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
	store, err := storage.OpenNodeSqliteStorage(filepath.Join(dir, "state.sqlite"), storage.NodeSqliteStorageOptions{})
	if err != nil {
		return nil, err
	}
	h, err := harness.OpenHarness(ctx, store, harness.HarnessOptions{
		Models: a.Catalog.Models, Registry: registry,
		Settings: func() *harness.HarnessSettings {
			return &harness.HarnessSettings{AfterFailedRun: durable.AfterFailedRunContinue}
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
	// Abort marks and queue withdrawal happen before scheduling can resume old task executions.
	isolated := a.recovering[agentID]
	if len(isolated) > 0 {
		state, err := h.Inspect(ctx)
		if err != nil {
			_ = h.Close(context.WithoutCancel(ctx))
			return nil, err
		}
		for _, task := range state.Tasks {
			if isolated[task.Record.ConversationId] {
				if _, err := h.AbortTask(ctx, task.Record.Id); err != nil {
					_ = h.Close(context.WithoutCancel(ctx))
					return nil, err
				}
			}
		}
		for _, submission := range state.Submissions {
			if isolated[submission.ConversationId] && submission.Status == durable.SubmissionQueued {
				if _, err := h.AbortSubmission(ctx, submission.Id, &submission.ConversationId); err != nil {
					_ = h.Close(context.WithoutCancel(ctx))
					return nil, err
				}
			}
		}
	}
	delete(a.recovering, agentID)
	return &agentResources{harness: h, loaded: loaded}, nil
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
	change := harness.AgentChange{Model: harness.SetTo(durable.ModelRef{Provider: options.Model.Provider, ModelId: options.Model.ModelID}), ThinkingLevel: harness.SetTo(ai.ModelThinkingLevel(options.ThinkingLevel)), Instructions: harness.Cleared[string]()}
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
	unlock := a.lock(ref)
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
	unlock := a.lock(ref)
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
	if a.resources[ref.AgentID] == nil {
		if a.recovering[ref.AgentID] == nil {
			a.recovering[ref.AgentID] = map[durable.ConversationId]bool{}
		}
		// Opening must isolate stored work before Harness.Resume can start it.
		a.recovering[ref.AgentID][durable.ConversationId(ref.ID)] = true
	}
	a.mu.Unlock()
	defer a.end()
	unlock := a.lock(ref)
	defer unlock()
	conversation, err := a.conversation(ctx, ref)
	if err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err = conversation.Commit(bounded, func(tx durable.Tx) (any, error) {
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
	if err != nil {
		return err
	}
	return conversation.Abort(bounded, &durable.ConversationAbortOptions{Background: true})
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
	a.paused = true
	if a.active > 0 || a.settling > 0 {
		a.paused = false
		return ErrBusy
	}
	for _, resource := range a.resources {
		state, err := resource.harness.Inspect(ctx)
		if err != nil {
			a.paused = false
			return err
		}
		if len(state.Tasks) > 0 || len(state.Submissions) > 0 {
			a.paused = false
			return ErrBusy
		}
	}
	return nil
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
	a.operations.Wait()
	a.mu.Lock()
	resources := maps.Clone(a.resources)
	a.mu.Unlock()
	var errs []error
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
	defer a.mu.Unlock()
	if resource := a.resources[agentID]; resource != nil {
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
	if err := a.Quiesce(ctx); err != nil {
		return err
	}
	defer a.Resume()
	a.mu.Lock()
	defer a.mu.Unlock()
	// An Abort can start after Quiesce closes admission. Do not close its Harness.
	if a.active > 0 {
		return ErrBusy
	}
	if err := a.files(agentID).Validate(ctx); err != nil {
		return err
	}
	old := a.resources[agentID]
	if old == nil {
		return nil
	}
	if err := old.harness.Close(ctx); err != nil {
		return err
	}
	if err := old.loaded.Close(ctx); err != nil {
		return err
	}
	delete(a.resources, agentID)
	resource, err := a.open(ctx, agentID)
	if err != nil {
		return err
	}
	a.resources[agentID] = resource
	resource.harness.Resume()
	return nil
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
