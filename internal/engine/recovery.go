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

// RecoveryOutcome reads a committed terminal input before any Harness can resume.
// A nil outcome means the request is absent or unfinished. It never admits work.
func (a *Agents) RecoveryOutcome(ctx context.Context, ref Ref, requestID string) (_ *Outcome, resultErr error) {
	if !agentIDPattern.MatchString(ref.AgentID) || ref.ID <= 0 || requestID == "" {
		return nil, fmt.Errorf("%w: recovery input", ErrInvalidInput)
	}
	if err := a.begin(); err != nil {
		return nil, err
	}
	defer a.end()
	a.mu.Lock()
	if len(a.resources) != 0 || len(a.opening) != 0 {
		a.mu.Unlock()
		return nil, errors.New("recovery must precede opening agents")
	}
	readCtx, cancel := context.WithCancel(ctx)
	opening := &agentOpening{done: make(chan struct{}), cancel: cancel}
	a.opening[ref.AgentID] = opening
	a.mu.Unlock()
	defer a.finishOpening(ref.AgentID, opening)
	path := filepath.Join(a.config.DataDir, "agents", ref.AgentID, "state.sqlite")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	store, err := storage.OpenNodeSqliteStorage(path, storage.NodeSqliteStorageOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, store.Close(context.WithoutCancel(ctx))) }()
	record, err := store.SubmissionByRequest(readCtx, durable.ConversationId(ref.ID), requestID)
	if err != nil {
		return nil, err
	}
	if record == nil || record.Status == durable.SubmissionQueued || record.Status == durable.SubmissionPlaced {
		return nil, nil
	}
	outcome := &Outcome{Status: string(record.Status)}
	if record.Reason != nil {
		outcome.Reason = *record.Reason
	}
	if record.Status == durable.SubmissionDone && record.Answer != nil {
		entry, err := store.Entry(readCtx, *record.Answer)
		if err != nil {
			return nil, err
		}
		if entry == nil {
			return nil, errors.New("completed native input has no answer entry")
		}
		outcome.Answer = answerText(entry.Entry.Model)
	}
	return outcome, nil
}
