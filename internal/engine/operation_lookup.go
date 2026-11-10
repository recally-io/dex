package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
	storage "github.com/MichaelKinsy/PiG/durable/storage/sqlite/node"
)

// readAgentStorage reads an open Harness or a cold SQLite store without starting a Harness.
// The operation count prevents Reload and Close from replacing the selected resource.
func (a *Agents) readAgentStorage(ctx context.Context, ref Ref, live func(context.Context, harness.Harness) error, cold func(context.Context, durable.Storage) error) (resultErr error) {
	if !agentIDPattern.MatchString(ref.AgentID) || ref.ID <= 0 {
		return fmt.Errorf("%w: operation reference", ErrInvalidInput)
	}
	if err := a.begin(); err != nil {
		return err
	}
	defer a.end()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			return ErrClosed
		}
		if opening := a.opening[ref.AgentID]; opening != nil {
			a.mu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-opening.done:
				continue
			}
		}
		if resource := a.resources[ref.AgentID]; resource != nil {
			a.mu.Unlock()
			return live(ctx, resource.harness)
		}
		readCtx, cancel := context.WithCancel(ctx)
		opening := &agentOpening{done: make(chan struct{}), cancel: cancel}
		a.opening[ref.AgentID] = opening
		a.mu.Unlock()
		defer a.finishOpening(ref.AgentID, opening)
		path := filepath.Join(a.config.DataDir, "agents", ref.AgentID, "state.sqlite")
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		store, err := storage.OpenNodeSqliteStorage(path, storage.NodeSqliteStorageOptions{})
		if err != nil {
			return err
		}
		defer func() { resultErr = errors.Join(resultErr, store.Close(context.WithoutCancel(ctx))) }()
		conversation, err := store.Conversation(readCtx, durable.ConversationId(ref.ID))
		if err != nil {
			return err
		}
		if conversation == nil {
			return ErrNotFound
		}
		return cold(readCtx, store)
	}
}

// readOperationDocument reads committed native operation state without opening a cold Harness.
func (a *Agents) readOperationDocument(ctx context.Context, ref Ref, token durable.AnyDocToken, kind string) (durable.JsonObject, error) {
	var result durable.JsonObject
	err := a.readAgentStorage(ctx, ref, func(ctx context.Context, h harness.Harness) error {
		var err error
		result, err = h.SnapshotErased(ctx, token, durable.ConversationId(ref.ID))
		return err
	}, func(ctx context.Context, store durable.Storage) error {
		doc, err := store.FindDocument(ctx, durable.DocumentAddress{Kind: kind, Scope: durable.DocumentRecordScope{Kind: durable.ScopeConversation, ConversationId: durable.ConversationId(ref.ID)}}, durable.CurrentPoint)
		if err != nil || doc == nil {
			return err
		}
		value, err := store.Document(ctx, doc.Id, durable.CurrentPoint)
		if err != nil {
			return err
		}
		if value == nil {
			return errors.New("operation document disappeared")
		}
		result = value.Value
		return nil
	})
	return result, err
}
