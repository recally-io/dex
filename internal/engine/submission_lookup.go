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

type SubmissionState string

const (
	SubmissionAbsent     SubmissionState = "absent"
	SubmissionQueued     SubmissionState = "queued"
	SubmissionPlaced     SubmissionState = "placed"
	SubmissionDone       SubmissionState = "done"
	SubmissionUnanswered SubmissionState = "unanswered"
)

// SubmissionLookup is a point-in-time native receipt. Entry is present only after
// placement; unanswered may have an Entry. Placement does not prove model execution.
type SubmissionLookup struct {
	State  SubmissionState
	Entry  *SubmissionInputEntry
	Reason *string
}

type SubmissionInputEntry struct {
	ID   int64
	Kind string
	Data durable.JsonValue
}

// LookupSubmission reads a committed receipt without admitting or resuming work.
// Cold lookup uses the recovery opener, which may migrate or back up storage.
// Like RecoveryOutcome, a cold lookup must precede opening any other agent.
func (a *Agents) LookupSubmission(ctx context.Context, ref Ref, requestID string) (_ SubmissionLookup, resultErr error) {
	if !agentIDPattern.MatchString(ref.AgentID) || ref.ID <= 0 || requestID == "" {
		return SubmissionLookup{}, fmt.Errorf("%w: submission lookup", ErrInvalidInput)
	}
	if err := a.begin(); err != nil {
		return SubmissionLookup{}, err
	}
	defer a.end()
	for {
		if err := ctx.Err(); err != nil {
			return SubmissionLookup{}, err
		}
		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			return SubmissionLookup{}, ErrClosed
		}
		if opening := a.opening[ref.AgentID]; opening != nil {
			a.mu.Unlock()
			select {
			case <-ctx.Done():
				return SubmissionLookup{}, ctx.Err()
			case <-opening.done:
				continue
			}
		}
		if resource := a.resources[ref.AgentID]; resource != nil {
			a.mu.Unlock()
			var result SubmissionLookup
			_, err := resource.harness.Commit(ctx, func(tx durable.Tx) (any, error) {
				var err error
				result, err = lookupSubmission(func() (*durable.SubmissionRecord, error) {
					return tx.SubmissionByRequest(durable.ConversationId(ref.ID), requestID)
				}, tx.Entry)
				return nil, err
			})
			return result, err
		}
		if len(a.resources) != 0 || len(a.opening) != 0 {
			a.mu.Unlock()
			return SubmissionLookup{}, errors.New("recovery must precede opening agents")
		}
		readCtx, cancel := context.WithCancel(ctx)
		opening := &agentOpening{done: make(chan struct{}), cancel: cancel}
		a.opening[ref.AgentID] = opening
		a.mu.Unlock()
		defer a.finishOpening(ref.AgentID, opening)
		path := filepath.Join(a.config.DataDir, "agents", ref.AgentID, "state.sqlite")
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return SubmissionLookup{State: SubmissionAbsent}, nil
		} else if err != nil {
			return SubmissionLookup{}, err
		}
		store, err := storage.OpenNodeSqliteStorage(path, storage.NodeSqliteStorageOptions{})
		if err != nil {
			return SubmissionLookup{}, err
		}
		defer func() { resultErr = errors.Join(resultErr, store.Close(context.WithoutCancel(ctx))) }()
		return lookupSubmission(func() (*durable.SubmissionRecord, error) {
			return store.SubmissionByRequest(readCtx, durable.ConversationId(ref.ID), requestID)
		}, func(id durable.EntryId) (*durable.EntryRecord, error) {
			found, err := store.Entry(readCtx, id)
			if err != nil || found == nil {
				return nil, err
			}
			return &found.Entry, nil
		})
	}
}

func lookupSubmission(read func() (*durable.SubmissionRecord, error), entry func(durable.EntryId) (*durable.EntryRecord, error)) (SubmissionLookup, error) {
	record, err := read()
	if err != nil {
		return SubmissionLookup{}, err
	}
	if record == nil {
		return SubmissionLookup{State: SubmissionAbsent}, nil
	}
	result := SubmissionLookup{State: SubmissionState(record.Status), Reason: record.Reason}
	switch record.Status {
	case durable.SubmissionQueued, durable.SubmissionPlaced, durable.SubmissionDone, durable.SubmissionUnanswered:
	default:
		return SubmissionLookup{}, fmt.Errorf("unknown native submission status %q", record.Status)
	}
	if record.Entry != nil {
		e, err := entry(*record.Entry)
		if err != nil {
			return SubmissionLookup{}, err
		}
		if e == nil {
			return SubmissionLookup{}, errors.New("native submission has no input entry")
		}
		result.Entry = &SubmissionInputEntry{ID: int64(e.Id), Kind: e.Kind, Data: e.Data}
	}
	return result, nil
}
