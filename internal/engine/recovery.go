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

// RecoveryState is how far native execution got with one input.
type RecoveryState string

const (
	// RecoveryAbsent means native execution has no input with this request ID.
	RecoveryAbsent RecoveryState = "absent"
	// RecoveryQueued means the input waits in the inbox and never ran.
	RecoveryQueued RecoveryState = "queued"
	// RecoveryPlaced means the input entered a run that has not ended.
	RecoveryPlaced RecoveryState = "placed"
	// RecoverySettled means the input has an outcome.
	RecoverySettled RecoveryState = "settled"
)

// Recovered is the committed native state of one input. Outcome is set only when State is
// RecoverySettled. A settled outcome never has a Starter, so a steered input that settled before the
// restart reports on its own.
type Recovered struct {
	State   RecoveryState
	Outcome Outcome
}

// RecoveryOutcome reads a committed input before any Harness can resume. It never admits work.
func (a *Agents) RecoveryOutcome(ctx context.Context, ref Ref, requestID string) (_ Recovered, resultErr error) {
	if !agentIDPattern.MatchString(ref.AgentID) || ref.ID <= 0 || requestID == "" {
		return Recovered{}, fmt.Errorf("%w: recovery input", ErrInvalidInput)
	}
	if err := a.begin(); err != nil {
		return Recovered{}, err
	}
	defer a.end()
	a.mu.Lock()
	if len(a.resources) != 0 || len(a.opening) != 0 {
		a.mu.Unlock()
		return Recovered{}, errors.New("recovery must precede opening agents")
	}
	readCtx, cancel := context.WithCancel(ctx)
	opening := &agentOpening{done: make(chan struct{}), cancel: cancel}
	a.opening[ref.AgentID] = opening
	a.mu.Unlock()
	defer a.finishOpening(ref.AgentID, opening)
	path := filepath.Join(a.config.DataDir, "agents", ref.AgentID, "state.sqlite")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return Recovered{State: RecoveryAbsent}, nil
	} else if err != nil {
		return Recovered{}, err
	}
	store, err := storage.OpenNodeSqliteStorage(path, storage.NodeSqliteStorageOptions{})
	if err != nil {
		return Recovered{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, store.Close(context.WithoutCancel(ctx))) }()
	record, err := store.SubmissionByRequest(readCtx, durable.ConversationId(ref.ID), requestID)
	if err != nil {
		return Recovered{}, err
	}
	switch {
	case record == nil:
		return Recovered{State: RecoveryAbsent}, nil
	case record.Status == durable.SubmissionQueued:
		return Recovered{State: RecoveryQueued}, nil
	case record.Status == durable.SubmissionPlaced:
		return Recovered{State: RecoveryPlaced}, nil
	}
	outcome := Outcome{Status: string(record.Status)}
	if record.Reason != nil {
		outcome.Reason = *record.Reason
	}
	if record.Status == durable.SubmissionDone && record.Answer != nil {
		entry, err := store.Entry(readCtx, *record.Answer)
		if err != nil {
			return Recovered{}, err
		}
		if entry == nil {
			return Recovered{}, errors.New("completed native input has no answer entry")
		}
		outcome.Answer = answerText(entry.Entry.Model)
	}
	return Recovered{State: RecoverySettled, Outcome: outcome}, nil
}
