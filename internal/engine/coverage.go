package engine

import (
	"context"
	"slices"

	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

// covered is covered on the open storage of an agent.
func (a *Agents) covered(ctx context.Context, ref Ref, record durable.SubmissionRecord) (bool, error) {
	if _, err := a.harness(ctx, ref.AgentID); err != nil {
		return false, err
	}
	a.mu.Lock()
	resource := a.resources[ref.AgentID]
	a.mu.Unlock()
	if resource == nil {
		return false, ErrClosed
	}
	return covered(ctx, resource.store, &record)
}

// doneScanLimit bounds how many done inputs of a conversation covered looks at, newest first. An
// input whose starter lies further back counts as a starter itself: a repeated result is better
// than a lost one.
const doneScanLimit = 256

// covered reports whether the run of an input has an earlier input that started it. The result of
// the starter covers such an input, so it sends none of its own. Only persisted native state decides,
// so a restart and a retried receipt give the same answer as the first settlement:
//
//   - done: inputs that share one answer entry ran together. The earliest input entry started them.
//   - placed: the live run lists its inputs in order. The first one started it.
//   - unanswered: the newest failed run lists its inputs in order. The first one started it. An older
//     failure is gone, and its inputs count as starters.
//
// A queued input has not joined a run yet.
func covered(ctx context.Context, store durable.Storage, record *durable.SubmissionRecord) (bool, error) {
	switch record.Status {
	case durable.SubmissionDone:
		return coveredByAnswer(ctx, store, record)
	case durable.SubmissionPlaced, durable.SubmissionUnanswered:
		live, err := liveState(ctx, store, record.ConversationId)
		if err != nil || live == nil {
			return false, err
		}
		var inputs []durable.SubmissionId
		switch {
		case record.Status == durable.SubmissionPlaced && live.Run != nil:
			inputs = live.Run.Inputs
		case record.Status == durable.SubmissionUnanswered && live.RunFailure != nil:
			inputs = live.RunFailure.Inputs
		}
		return slices.Index(inputs, record.Id) > 0, nil
	case durable.SubmissionQueued:
	}
	return false, nil
}

func coveredByAnswer(ctx context.Context, store durable.Storage, record *durable.SubmissionRecord) (bool, error) {
	if record.Answer == nil || record.Entry == nil {
		return false, nil
	}
	done, order := durable.SubmissionDone, durable.ScanDescending
	page, err := store.ScanSubmissions(ctx, durable.SubmissionQuery{ConversationId: &record.ConversationId, Status: &done, Order: &order}, doneScanLimit, nil)
	if err != nil {
		return false, err
	}
	for _, other := range page.Items {
		if other.Id != record.Id && other.Answer != nil && *other.Answer == *record.Answer && other.Entry != nil && *other.Entry < *record.Entry {
			return true, nil
		}
	}
	return false, nil
}

// liveState reads the committed run control of a conversation. It returns nil when the conversation
// has no such document.
func liveState(ctx context.Context, store durable.Storage, conversation durable.ConversationId) (*harness.LiveState, error) {
	address := durable.DocumentAddress{Kind: harness.LiveDoc.AnyDefinition().Kind, Scope: durable.DocumentRecordScope{Kind: durable.ScopeConversation, ConversationId: conversation}}
	record, err := store.FindDocument(ctx, address, durable.CurrentPoint)
	if err != nil || record == nil {
		return nil, err
	}
	stored, err := store.Document(ctx, record.Id, durable.CurrentPoint)
	if err != nil || stored == nil {
		return nil, err
	}
	return durable.DecodeDoc[harness.LiveState](stored.Value)
}
