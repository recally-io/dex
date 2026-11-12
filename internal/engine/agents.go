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
	store   durable.Storage
	loaded  *loadedExtensions
}

type activityRecord struct {
	lastAction     string
	lastActivityAt int64
	toolStarted    map[string]int64
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
		activity := Activity{LastAction: r.lastAction, LastActivityAt: new(r.lastActivityAt)}
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
	Catalog          *Catalog
	config           Config
	memory           *MemoryRepo
	env              *toolEnv
	mu               sync.Mutex
	resources        map[string]*agentResources
	activity         map[Ref]*activityRecord
	activityWatches  map[Ref]int // internal watches per child conversation; at most one
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
	return &Agents{Catalog: NewCatalog(config.FakeModel), config: config, memory: NewMemoryRepo(filepath.Join(config.DataDir, "memory"), config.BaseEnv), env: newToolEnv(config.Home, config.BaseEnv, config.LoginEnv), resources: map[string]*agentResources{}, activity: map[Ref]*activityRecord{}, activityWatches: map[Ref]int{}, recovering: map[string]map[durable.ConversationId]bool{}, opening: map[string]*agentOpening{}, serial: map[Ref]chan struct{}{}, closeDone: make(chan struct{})}, nil
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
	files := a.files(agentID)
	loaded, err := files.Load(ctx)
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
	extensions := []*durable.Extension{codingTools(loaded.Env), routineExtension(agentID, a.config.Routines), sessionExtension(agentID, a.config.Tasks), sessionReadExtension(agentID, a.config.Tasks), backgroundExtension(agentID, a.config.Tasks.Background, loaded.Env, filepath.Join(dir, "background"), workspace), workLoopExtension(), artifactExtension(agentID, workspace, a.config.Tasks.DeliverArtifact), a.memory.Extension(agentID, func(ctx context.Context, id durable.ConversationId) (string, error) {
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
	if err := registry.Install(frontDeskExtension()); err != nil {
		return nil, err
	}
	if err := registry.Install(askUserExtension(agentID, a.config.Tasks.AskUser)); err != nil {
		return nil, err
	}
	if err := errors.Join(registry.Install(botReadExtension(agentID, a.config.Bots)), registry.Install(botManageExtension(agentID, a.config.Bots))); err != nil {
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
	h, err := harness.OpenHarness(ctx, store, harness.HarnessOptions{
		Models: a.Catalog.Models, Registry: registry,
		Settings: func() *harness.HarnessSettings {
			return &harness.HarnessSettings{Extensions: extensions, AfterFailedRun: durable.AfterFailedRunContinue, SteeringMode: durable.QueueAll}
		},
		Env: func(_ context.Context, target harness.EnvTarget) (durableenv.ExecutionEnv, error) {
			cwd := workspace
			if target.Cwd != nil {
				cwd = *target.Cwd
			}
			return envnode.NewNodeExecutionEnv(envnode.NodeExecutionEnvOptions{Cwd: cwd, ShellEnv: loaded.Env}), nil
		},
		OnReport: func(report durable.Report) {
			slog.Error("agent execution", "agent", agentID, "type", report.Kind, "error", report.Error)
		},
	})
	if err != nil {
		return nil, err
	}
	return &agentResources{harness: h, store: store, loaded: loaded}, nil
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

// agentChange adds the AGENTS.md text to the instructions. It reads the files at each admission,
// so an edit applies from the next input of a conversation.
func (a *Agents) agentChange(agentID string, options Options) harness.AgentChange {
	options.Instructions = a.files(agentID).instructions(options.Cwd, options.Instructions)
	return agentChange(options)
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
	change := a.agentChange(agentID, options)
	conversation, err := h.CreateConversation(ctx, harness.ConversationCreateOptions{Ownership: durable.ConversationOwnership{Kind: durable.ConversationOwnerless}, Agent: &change})
	if err != nil {
		return Ref{}, err
	}
	return Ref{AgentID: agentID, ID: int64(conversation.Id())}, nil
}

// Fork copies the committed history of ref into a new native conversation, which keeps the
// source's configuration until the next admission configures it. Without before, the copy ends
// at entryID, which must be an answer that waits for no tool result. With before, the copy ends
// just before entryID; ok is false when nothing comes before it.
func (a *Agents) Fork(ctx context.Context, ref Ref, entryID int64, before bool) (forked Ref, ok bool, err error) {
	if err := a.begin(); err != nil {
		return Ref{}, false, err
	}
	defer a.end()
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
	copied, err := conversation.Fork(ctx, at, harness.ConversationCreateOptions{Ownership: durable.ConversationOwnership{Kind: durable.ConversationOwnerless}})
	if err != nil {
		return Ref{}, false, err
	}
	return Ref{AgentID: ref.AgentID, ID: int64(copied.Id())}, true, nil
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

// Reset starts a new context. A handoff becomes the first user message of that context; it does
// not start a run, and the next run reads it.
func (a *Agents) Reset(ctx context.Context, ref Ref, requestID, handoff string) error {
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
	// The user can choose another model after a model error; the continued run uses it.
	if err := conversation.Configure(ctx, a.agentChange(ref.AgentID, options)); err != nil {
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
func (a *Agents) Compact(ctx context.Context, ref Ref, instructions, requestID string, options Options) (int64, error) {
	if options.Model.ModelID == "" {
		return 0, ErrNoModel
	}
	if err := a.begin(); err != nil {
		return 0, err
	}
	defer a.end()
	unlock, err := a.lock(ctx, ref)
	if err != nil {
		return 0, err
	}
	defer unlock()
	conversation, err := a.conversation(ctx, ref)
	if err != nil {
		return 0, err
	}
	// The summary uses the conversation model, so it must be the chat's current choice.
	if err := conversation.Configure(ctx, a.agentChange(ref.AgentID, options)); err != nil {
		return 0, err
	}
	h, err := a.harness(ctx, ref.AgentID)
	if err != nil {
		return 0, err
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
			return int64(id), nil
		}
	}
	if err != nil {
		return 0, err
	}
	a.mu.Lock()
	a.settling++
	a.mu.Unlock()
	a.settlements.Go(func() {
		defer func() { a.mu.Lock(); a.settling--; a.mu.Unlock() }()
		if err := noteCompaction(context.WithoutCancel(ctx), h, conversation, id); err != nil {
			slog.Error("note compaction outcome", "agent", ref.AgentID, "conversation", ref.ID, "task", id, "error", err)
		}
	})
	return int64(id), nil
}

// CompactionNoteKind is the transcript entry for a manual compaction that made no summary. A
// summary is its own pi.compaction entry, so a note is only written for the other outcomes.
const CompactionNoteKind = "dex.compaction"

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
	_, err = conversation.Submit(ctx, durable.SubmissionDraft{Type: durable.SubmissionTypeWrite, RequestId: &requestID, Entry: &durable.EntryDraft{Kind: CompactionNoteKind, Data: data}})
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
	h, err := a.harness(ctx, ref.AgentID)
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
		errs = append(errs, resource.loaded.Close(ctx))
	}
	a.closeErr = errors.Join(errs...)
	close(a.closeDone)
}

func (a *Agents) files(agentID string) *agentExtensions {
	return newAgentExtensions(a.config.Home, a.config.DataDir, agentID, a.env)
}

// SetOnePassword changes the 1Password credentials of tools. Open agents become stale and
// apply them at their next reload.
func (a *Agents) SetOnePassword(op OnePassword) { a.env.setOnePassword(op) }

// OnePasswordCLI returns the path of op in the PATH of the base tool environment, or "". It
// captures the login shell first when no agent has, because the service PATH can lack op.
func (a *Agents) OnePasswordCLI(ctx context.Context) string {
	env, _ := a.env.load(ctx)
	return findOp(env)
}

// StaleExtensions returns the open agents whose extension files, shell rc files, or 1Password
// settings changed after their load, with the current stamp of each.
func (a *Agents) StaleExtensions() map[string]string {
	a.mu.Lock()
	loaded := map[string]string{}
	for id, resource := range a.resources {
		loaded[id] = resource.loaded.stamp
	}
	a.mu.Unlock()
	stale := map[string]string{}
	for id, stamp := range loaded {
		if current := a.files(id).stamp(); current != stamp {
			stale[id] = current
		}
	}
	return stale
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
		info.ReloadPending = a.files(agentID).stamp() != resource.loaded.stamp
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
