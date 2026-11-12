package engine

import (
	"context"
	"fmt"

	"github.com/MichaelKinsy/PiG/durable"
)

// Withdrawal is the native outcome of withdrawing one input.
type Withdrawal string

const (
	// Withdrawn means the input never ran. It is also the answer for an input that an earlier
	// withdrawal aborted, so a retry after a crash gets the same result.
	Withdrawn Withdrawal = "withdrawn"
	// Placed means the input already entered an execution context, or it finished.
	Placed Withdrawal = "placed"
	// NotAdmitted means native execution has no input with this request ID.
	NotAdmitted Withdrawal = "not_admitted"
)

// PeekWithdrawal reads the native state without waiting for the execution serialization gate.
// The result is a snapshot; placement can race with a later Stop.
func (a *Agents) PeekWithdrawal(ctx context.Context, ref Ref, requestID string) (Withdrawal, error) {
	if requestID == "" {
		return "", fmt.Errorf("%w: request ID is required", ErrInvalidInput)
	}
	if err := a.begin(); err != nil {
		return "", err
	}
	defer a.end()
	conversation, err := a.conversation(ctx, ref)
	if err != nil {
		return "", err
	}
	var record *durable.SubmissionRecord
	if _, err := conversation.Commit(ctx, func(tx durable.Tx) (any, error) {
		var err error
		record, err = tx.SubmissionByRequest(conversation.Id(), requestID)
		return nil, err
	}); err != nil {
		return "", err
	}
	if record == nil {
		return NotAdmitted, nil
	}
	if record.Status == durable.SubmissionQueued || (record.Status == durable.SubmissionUnanswered && record.Reason != nil && *record.Reason == "aborted") {
		return Withdrawn, nil
	}
	return Placed, nil
}

// Withdraw removes one queued input by its business request ID. The native transaction decides
// whether the input is still queued; a placed input keeps running.
func (a *Agents) Withdraw(ctx context.Context, ref Ref, requestID string) (Withdrawal, error) {
	if requestID == "" {
		return "", fmt.Errorf("%w: request ID is required", ErrInvalidInput)
	}
	if err := a.begin(); err != nil {
		return "", err
	}
	defer a.end()
	unlock, err := a.lock(ctx, ref)
	if err != nil {
		return "", err
	}
	defer unlock()
	conversation, err := a.conversation(ctx, ref)
	if err != nil {
		return "", err
	}
	var record *durable.SubmissionRecord
	if _, err = conversation.Commit(ctx, func(tx durable.Tx) (any, error) {
		var err error
		record, err = tx.SubmissionByRequest(conversation.Id(), requestID)
		return nil, err
	}); err != nil {
		return "", err
	}
	if record == nil {
		return NotAdmitted, nil
	}
	h, err := a.harness(ctx, ref.AgentID)
	if err != nil {
		return "", err
	}
	native, err := h.Submission(ctx, record.Id)
	if err != nil {
		return "", err
	}
	result, err := native.Abort(ctx)
	if err != nil {
		return "", err
	}
	switch result {
	case durable.SubmissionAborted:
		return Withdrawn, nil
	case durable.SubmissionSettled:
		settled, err := native.Status(ctx)
		if err != nil {
			return "", err
		}
		// An earlier withdrawal can have aborted the input before the receipt write failed.
		if settled.Status == durable.SubmissionUnanswered && settled.Reason != nil && *settled.Reason == "aborted" {
			return Withdrawn, nil
		}
		return Placed, nil
	case durable.SubmissionAlreadyPlaced:
		return Placed, nil
	case durable.SubmissionNotFound:
		return NotAdmitted, nil
	}
	return "", fmt.Errorf("unknown submission abort result %q", result)
}
