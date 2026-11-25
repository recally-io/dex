package engine

import (
	"context"
	"errors"

	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

func liveRun(tx durable.Tx, id durable.ConversationId) (*harness.LiveRun, error) {
	doc, err := tx.Doc(harness.LiveDoc, id)
	if err != nil {
		return nil, err
	}
	state, err := durable.DecodeDoc[harness.LiveState](doc.Snapshot())
	return state.Run, err
}

var errReviewBusy = errors.Join(ErrBusy, ErrReviewBarrier)

// PiG selects steers and follow-ups together at a final boundary. Never put an
// isolated input in that Inbox, even if there is no current run: SubmitIn can
// itself apply a final boundary on an idle conversation with pending items.
// Check on the native transaction line, not against an earlier run snapshot.
func isolatedAdmission(tx durable.Tx, id durable.ConversationId, input Input, mode durable.WhenBusy) (durable.WhenBusy, error) {
	run, err := liveRun(tx, id)
	if err != nil {
		return mode, err
	}
	if input.Isolation != "" {
		blocked := ErrBusy
		if input.Isolation == IsolatedReview {
			blocked = errReviewBusy
		}
		if run != nil {
			return mode, blocked
		}
		inbox, err := tx.Doc(harness.InboxDoc, id)
		if err != nil {
			return mode, err
		}
		if inbox.Array("items").Len() != 0 {
			return mode, blocked
		}
		return mode, nil
	}
	if run == nil {
		return mode, nil // SubmitIn rejects an idle bypass in the same transaction.
	}
	var first inputMetadata
	if run.First != nil {
		entry, err := tx.Entry(*run.First)
		if err != nil {
			return mode, err
		}
		if entry != nil {
			first = metadata(entry.Data)
		}
	}
	if input.ReviewBypass && mode == durable.WhenBusySteer {
		// A final boundary can start a run with the steer. Let that run finish
		// without a bypass so continuous steers cannot starve the review.
		if first.Kind != "message" || first.ReviewBypass || len(run.Inputs) == 0 {
			return mode, errReviewBusy
		}
		// Continue can use the previous run's first entry. Only bypass when
		// the first entry belongs to this run's first placed input.
		start, err := tx.SubmissionByRequest(id, first.RequestID)
		if err != nil {
			return mode, err
		}
		if start == nil || start.Id != run.Inputs[0] {
			return mode, errReviewBusy
		}
	}
	switch first.Isolation {
	case IsolatedReview:
		return mode, errReviewBusy
	case IsolatedDelegation:
		if mode == durable.WhenBusySteer {
			return durable.WhenBusyFollowUp, nil
		}
	}
	return mode, nil
}

// The first entry is immutable across PiG's generation handovers after tools.
// Its request and isolation marker commit with the native input and survive
// both settlement and a missing control admission receipt.
func activationOf(record durable.SubmissionRecord, entry *durable.EntryRecord) int64 {
	if entry == nil || record.RequestId == nil || record.Entry == nil || *record.Entry != entry.Id || entry.ConversationId != record.ConversationId {
		return 0
	}
	m := metadata(entry.Data)
	if m.Isolation == "" || m.RequestID != *record.RequestId {
		return 0
	}
	return int64(entry.Id)
}

// Prove that the durable operation belongs to the current generation and that
// the protected first entry is the sole native input. Neither a source memo nor
// membership in the run-input set can prove this.
func reportActivation(ctx context.Context, api durable.ToolExecutionApi, call *TaskContext) error {
	_, err := api.Commit(ctx, func(tx durable.Tx) (any, error) {
		id := api.ConversationId()
		run, err := liveRun(tx, id)
		if err != nil || run == nil || run.First == nil {
			return nil, err
		}
		entry, err := tx.Entry(*run.First)
		if err != nil || entry == nil {
			return nil, err
		}
		m := metadata(entry.Data)
		call.Isolation = m.Isolation
		if m.Isolation == "" {
			return nil, nil
		}
		task, err := tx.Task(api.TaskId())
		for err == nil && task != nil && task.Id != run.TaskId && task.Owner != nil {
			task, err = tx.Task(*task.Owner)
		}
		if err != nil {
			return nil, err
		}
		if task == nil || task.Id != run.TaskId || task.ConversationId != id {
			return nil, errors.New("tool operation does not own the isolated activation")
		}
		record, err := tx.SubmissionByRequest(id, call.SourceRequestID)
		if err == nil && record != nil && len(run.Inputs) == 1 && run.Inputs[0] == record.Id {
			call.ActivationID = activationOf(*record, entry)
		}
		return nil, err
	})
	return err
}
