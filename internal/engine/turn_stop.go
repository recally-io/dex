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

// ErrStaleRunTarget means the selected run is not the current foreground run.
var ErrStaleRunTarget = errors.New("the selected run is no longer active in this session")

func runTargetPrefix(ref Ref) string {
	return ref.AgentID + "/" + strconv.FormatInt(ref.ID, 10) + "/"
}

func runTargetID(ref Ref, target string) (durable.TaskId, error) {
	raw, ok := strings.CutPrefix(target, runTargetPrefix(ref))
	id, err := strconv.ParseInt(raw, 10, 64)
	if ref.Scope != "" || !ok || err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
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
	if err := a.begin(resourceKey(ref)); err != nil {
		return err
	}
	defer a.end(resourceKey(ref))
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

// StopRun applies an exact, previously validated stop intent. PiG mints task IDs in durable
// storage. A handoff, reset or next run uses another ID, not a new invocation of this ID.
func (a *Agents) StopRun(ctx context.Context, ref Ref, target string) (resultErr error) {
	id, err := runTargetID(ref, target)
	if err != nil {
		return err
	}
	trace := slog.With("agent", ref.AgentID, "native_conversation_id", ref.ID, "native_task_id", id)
	started := time.Now()
	aborted := false
	trace.InfoContext(ctx, "session stop", "phase", "stop_attempt")
	defer func() {
		result := "no_abort_observed"
		switch {
		case errors.Is(resultErr, context.DeadlineExceeded):
			result = "deadline"
		case errors.Is(resultErr, context.Canceled):
			result = "canceled"
		case resultErr != nil:
			result = "unknown"
		case aborted:
			result = "aborted"
		}
		trace.InfoContext(ctx, "session stop", "phase", "stop_attempt_ended", "reason", result,
			"abort_observed", aborted, "elapsed_ms", time.Since(started).Milliseconds())
	}()
	if err := a.begin(resourceKey(ref)); err != nil {
		return err
	}
	defer a.end(resourceKey(ref))
	unlock, err := a.lock(ctx, ref)
	if err != nil {
		return err
	}
	defer unlock()
	c, err := a.conversation(ctx, ref)
	if err != nil {
		return err
	}
	h, err := a.harness(ctx, ref.AgentID, ref.Scope)
	if err != nil {
		return err
	}
	current, err := isCurrentRun(ctx, c, id)
	if err != nil {
		return err
	}
	if current {
		// PiG creates a successor owned by the conversation, not by this task.
		// After handoff the old task is terminal or completing. Neither can run its
		// abort handler. AbortTask cannot reach the successor or drain this Inbox.
		if _, err := h.AbortTask(ctx, id); err != nil {
			return err
		}
	}
	task, err := h.GetTask(ctx, id)
	if err != nil || task == nil || task.ConversationId != c.Id() {
		return err
	}
	if current && task.State.Status != durable.TaskTerminal {
		settled, err := h.WaitForTask(ctx, id)
		if err != nil {
			return err
		}
		task = &settled
	}
	if task.State.Outcome == nil || task.State.Outcome.Status != durable.OutcomeAborted {
		return nil
	}
	aborted = true

	// PiG 0.7 ends the generation on task abort without taking a final Inbox boundary.
	// Continue admits that boundary, without another user entry. Check idle + queued in
	// the same transaction, so retry after a crash cannot start a second run.
	_, err = c.Commit(ctx, func(tx durable.Tx) (any, error) {
		live, err := liveState(tx, c.Id())
		if err != nil || live.Run != nil || (live.RunFailure != nil && live.RunFailure.TaskId != id) {
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
	return err
}
