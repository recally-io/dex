package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/MichaelKinsy/PiG/durable"
	storage "github.com/MichaelKinsy/PiG/durable/storage/sqlite/node"
)

// readOperationDocument reads committed native operation state without opening a cold Harness.
func (a *Agents) readOperationDocument(ctx context.Context, ref Ref, token durable.AnyDocToken, kind string) (_ durable.JsonObject, resultErr error) {
	if !agentIDPattern.MatchString(ref.AgentID) || ref.ID <= 0 {
		return nil, fmt.Errorf("%w: operation reference", ErrInvalidInput)
	}
	if err := a.begin(); err != nil {
		return nil, err
	}
	defer a.end()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			return nil, ErrClosed
		}
		if opening := a.opening[ref.AgentID]; opening != nil {
			a.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-opening.done:
				continue
			}
		}
		if resource := a.resources[ref.AgentID]; resource != nil {
			a.mu.Unlock()
			return resource.harness.SnapshotErased(ctx, token, durable.ConversationId(ref.ID))
		}
		readCtx, cancel := context.WithCancel(ctx)
		opening := &agentOpening{done: make(chan struct{}), cancel: cancel}
		a.opening[ref.AgentID] = opening
		a.mu.Unlock()
		defer a.finishOpening(ref.AgentID, opening)
		path := filepath.Join(a.config.DataDir, "agents", ref.AgentID, "state.sqlite")
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		} else if err != nil {
			return nil, err
		}
		store, err := storage.OpenNodeSqliteStorage(path, storage.NodeSqliteStorageOptions{})
		if err != nil {
			return nil, err
		}
		defer func() { resultErr = errors.Join(resultErr, store.Close(context.WithoutCancel(ctx))) }()
		conversation, err := store.Conversation(readCtx, durable.ConversationId(ref.ID))
		if err != nil {
			return nil, err
		}
		if conversation == nil {
			return nil, ErrNotFound
		}
		doc, err := store.FindDocument(readCtx, durable.DocumentAddress{Kind: kind, Scope: durable.DocumentRecordScope{Kind: durable.ScopeConversation, ConversationId: durable.ConversationId(ref.ID)}}, durable.CurrentPoint)
		if err != nil || doc == nil {
			return nil, err
		}
		value, err := store.Document(readCtx, doc.Id, durable.CurrentPoint)
		if err != nil {
			return nil, err
		}
		if value == nil {
			return nil, errors.New("operation document disappeared")
		}
		return value.Value, nil
	}
}
