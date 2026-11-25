package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

// ErrStaleRunTarget means Stop did not cancel the selected foreground task.
var ErrStaleRunTarget = errors.New("the selected task is no longer active in this session")

func runTargetPrefix(ref Ref) string {
	return ref.AgentID + "/" + strconv.FormatInt(ref.ID, 10) + "/"
}

func runTargetID(ref Ref, target string) (durable.TaskId, error) {
	raw, ok := strings.CutPrefix(target, runTargetPrefix(ref))
	id, err := strconv.ParseInt(raw, 10, 64)
	if !ok || err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
		return 0, ErrStaleRunTarget
	}
	return durable.TaskId(id), nil
}

func liveState(tx durable.Tx, ref durable.ConversationId) (*harness.LiveState, error) {
	doc, err := durable.TxDoc[harness.LiveState](tx, harness.LiveDoc, ref)
	if err != nil {
		return nil, err
	}
	return durable.DecodeDoc[harness.LiveState](doc.Snapshot())
}

func isCurrentRun(ctx context.Context, c harness.Conversation, id durable.TaskId) (bool, error) {
	var current bool
	_, err := c.Commit(ctx, func(tx durable.Tx) (any, error) {
		live, err := liveState(tx, c.Id())
		if err != nil || live.Run == nil || live.Run.TaskId != id {
			return nil, err
		}
		task, err := tx.Task(id)
		current = err == nil && task != nil && task.ConversationId == c.Id() && task.State.Status != durable.TaskTerminal
		return nil, err
	})
	return current, err
}

// ValidateRunTarget checks the target before the caller records a new stop intent.
func (a *Agents) ValidateRunTarget(ctx context.Context, ref Ref, target string) error {
	id, err := runTargetID(ref, target)
	if err != nil {
		return err
	}
	if err := a.begin(ref.AgentID); err != nil {
		return err
	}
	defer a.end(ref.AgentID)
	c, err := a.conversation(ctx, ref)
	if err != nil {
		return err
	}
	current, err := isCurrentRun(ctx, c, id)
	if err != nil {
		return err
	}
	if !current {
		return ErrStaleRunTarget
	}
	return nil
}

// Stop applies a cancellation intent. A nil target explicitly cancels the session and
// withdraws its queued input. A non-nil target can cancel only that native foreground task.
func (a *Agents) Stop(ctx context.Context, ref Ref, target *string) (resultErr error) {
	if !agentIDPattern.MatchString(ref.AgentID) || ref.ID <= 0 {
		return fmt.Errorf("%w: cancellation reference", ErrInvalidInput)
	}
	var id durable.TaskId
	var err error
	if target == nil {
		err = a.beginSessionStop(ref)
	} else {
		id, err = runTargetID(ref, *target)
		if err != nil {
			return err
		}
		finish := traceTaskStop(ctx, ref, id)
		defer func() { finish(resultErr) }()
		err = a.begin(ref.AgentID)
	}
	if err != nil {
		return err
	}
	defer a.end(ref.AgentID)
	unlock, err := a.lock(ctx, ref)
	if err != nil {
		return err
	}
	defer unlock()
	c, err := a.conversation(ctx, ref)
	if err != nil {
		return err
	}
	if target == nil {
		return a.stopSession(ctx, ref, c)
	}
	return a.stopTask(ctx, ref, c, id)
}

func traceTaskStop(ctx context.Context, ref Ref, id durable.TaskId) func(error) {
	trace := slog.With("agent", ref.AgentID, "native_conversation_id", ref.ID, "native_task_id", id)
	started := time.Now()
	trace.InfoContext(ctx, "session stop", "phase", "stop_attempt")
	return func(resultErr error) {
		result := "aborted"
		switch {
		case errors.Is(resultErr, context.DeadlineExceeded):
			result = "deadline"
		case errors.Is(resultErr, context.Canceled):
			result = "canceled"
		case errors.Is(resultErr, ErrStaleRunTarget):
			result = "stale_target"
		case resultErr != nil:
			result = "unknown"
		}
		trace.InfoContext(ctx, "session stop", "phase", "stop_attempt_ended", "reason", result,
			"abort_observed", resultErr == nil, "elapsed_ms", time.Since(started).Milliseconds())
	}
}

func (a *Agents) stopTask(ctx context.Context, ref Ref, c harness.Conversation, id durable.TaskId) error {
	h, err := a.harness(ctx, ref.AgentID)
	if err != nil {
		return err
	}
	current, err := isCurrentRun(ctx, c, id)
	if err != nil {
		return err
	}
	marked := false
	if current {
		// Handoff can replace the task after validation. PiG can mark only this ID.
		result, err := h.AbortTask(ctx, id)
		if err != nil {
			return err
		}
		marked = result == "marked"
	}
	task, err := h.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if task == nil || task.ConversationId != c.Id() {
		return ErrStaleRunTarget
	}
	if task.State.Status != durable.TaskTerminal {
		if !marked && !task.AbortRequested {
			return ErrStaleRunTarget
		}
		settled, err := h.WaitForTask(ctx, id)
		if err != nil {
			return err
		}
		task = &settled
	}
	if task.State.Outcome == nil || task.State.Outcome.Status != durable.OutcomeAborted {
		return ErrStaleRunTarget
	}

	// PiG 0.7 ends the generation on task abort without taking a final Inbox boundary.
	// Continue admits that boundary, without another user entry. Check idle + queued in
	// the same transaction, so retry after a crash cannot start a second run.
	_, err = c.Commit(ctx, func(tx durable.Tx) (any, error) {
		live, err := liveState(tx, c.Id())
		if err != nil || live.Run != nil || live.RunFailure == nil || live.RunFailure.TaskId != id {
			return nil, err
		}
		doc, err := durable.TxDoc[harness.InboxState](tx, harness.InboxDoc, c.Id())
		if err != nil {
			return nil, err
		}
		inbox, err := durable.DecodeDoc[harness.InboxState](doc.Snapshot())
		if err != nil {
			return nil, err
		}
		for _, item := range inbox.Items {
			if item.Mode == harness.InboxFollowUp || item.Mode == harness.InboxSteer {
				request := fmt.Sprintf("dex:stop:resume:%d", id)
				_, err = c.SubmitIn(tx, durable.SubmissionDraft{Type: durable.SubmissionTypeContinue, RequestId: &request})
				return nil, err
			}
		}
		return nil, nil
	})
	if err != nil {
		return err
	}
	if !marked {
		return ErrStaleRunTarget
	}
	return nil
}
