package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

// ErrOperationConflict means that an operation ID was reused with different arguments.
var ErrOperationConflict = errors.New("operation ID has different arguments")

type forkOperation struct {
	EntryID durable.EntryId        `json:"entryId"`
	Options Options                `json:"options"`
	Result  durable.ConversationId `json:"result"`
}
type forkOperations struct {
	Operations map[string]forkOperation `json:"operations"`
}

var forkOperationsDoc = durable.DefineDoc(durable.DocDefinition[forkOperations]{
	Kind: "dex.fork_operations", Version: 1,
	Initial:           func() forkOperations { return forkOperations{Operations: map[string]forkOperation{}} },
	CheckpointWhen:    func(forkOperations, []durable.Op, durable.CheckpointInfo) bool { return true },
	DocumentSemantics: durable.NewLatestConversationSemantics(durable.ForkInitial).DocumentSemantics,
})

// Fork creates one independent Chat execution at a committed visible entry. A replay
// must use the same operation ID and frozen options, including after a restart.
func (a *Agents) Fork(ctx context.Context, ref Ref, operationID string, entryID int64, options Options) (Ref, error) {
	if operationID == "" || entryID <= 0 {
		return Ref{}, fmt.Errorf("%w: fork operation or entry", ErrInvalidInput)
	}
	if err := a.begin(); err != nil {
		return Ref{}, err
	}
	defer a.end()
	unlock, err := a.lock(ctx, ref)
	if err != nil {
		return Ref{}, err
	}
	defer unlock()
	conversation, err := a.conversation(ctx, ref)
	if err != nil {
		return Ref{}, err
	}
	result, err := conversation.Commit(ctx, func(tx durable.Tx) (any, error) {
		doc, err := tx.Doc(forkOperationsDoc, conversation.Id())
		if err != nil {
			return nil, err
		}
		state, err := durable.FromJsonValue[forkOperations](doc.Snapshot())
		if err != nil {
			return nil, err
		}
		entry := durable.EntryId(entryID)
		if previous, ok := state.Operations[operationID]; ok {
			if previous.EntryID != entry || previous.Options != options {
				return nil, ErrOperationConflict
			}
			return previous.Result, nil
		}
		page, err := tx.ScanEntries(durable.EntryQuery{ConversationId: conversation.Id(), MinEntryId: &entry, MaxEntryId: &entry}, 1, nil)
		if err != nil {
			return nil, err
		}
		if len(page.Items) != 1 || page.Items[0].Id != entry {
			return nil, ErrNotFound
		}
		child, err := tx.ForkConversation(conversation.Id(), entry, durable.CreateConversationOptions{Ownership: durable.ConversationOwnership{Kind: durable.ConversationOwnerless}})
		if err != nil {
			return nil, err
		}
		change := agentChange(options)
		change.Extensions = harness.Cleared[harness.ExtensionChange]()
		change.Tools = harness.Cleared[harness.ToolChange]()
		change.ModelTools = harness.Cleared[harness.ToolChange]()
		change.MaxTurns = harness.Cleared[float64]()
		if err = harness.Configure(tx, child.Id, change); err != nil {
			return nil, err
		}
		state.Operations[operationID] = forkOperation{EntryID: entry, Options: options, Result: child.Id}
		value, err := durable.ToJsonValue(state.Operations)
		if err != nil {
			return nil, err
		}
		if err = doc.Set("operations", value); err != nil {
			return nil, err
		}
		return child.Id, nil
	})
	if err != nil {
		return Ref{}, err
	}
	id, ok := result.(durable.ConversationId)
	if !ok {
		return Ref{}, fmt.Errorf("unexpected fork result %T", result)
	}
	return Ref{AgentID: ref.AgentID, ID: int64(id)}, nil
}
