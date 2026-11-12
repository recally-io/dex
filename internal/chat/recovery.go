package chat

import (
	"context"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

// RecoverSessions decides, before any Harness can resume, what became of each parent turn that a
// child session had admitted. A run that the restart cut off is not run again: its side effects
// are not known. The parent gets one result per turn, and the child stays open for session_send.
func (s *Service) RecoverSessions(ctx context.Context) error {
	children, err := s.q.ListRecoveryChildren(ctx)
	if err != nil {
		return err
	}
	for _, c := range children {
		if err := s.recoverChild(ctx, c); err != nil {
			return err
		}
	}
	aborts, err := s.q.ListPendingExecutionAborts(ctx)
	if err != nil {
		return err
	}
	refs := make([]engine.Ref, 0, len(aborts))
	for _, c := range aborts {
		refs = append(refs, engine.Ref{AgentID: c.AgentID, ID: c.ExecutionID.Int64})
	}
	return s.engine.Recover(ctx, refs)
}

type recoveredInput struct {
	id      string
	outcome engine.Outcome
}

// recoverChild settles the pending inputs of one child from the native state of each. Native
// admission and its receipt are two commits, so the receipt cannot tell which inputs entered a run.
// A run that a restart cut off is aborted. Every input that waited behind it is withdrawn, whether
// it sits in the native queue or only in the control database, and the result of the interrupted
// turn counts them.
func (s *Service) recoverChild(ctx context.Context, c sqlc.ListRecoveryChildrenRow) error {
	ref := engine.Ref{AgentID: c.AgentID, ID: c.ExecutionID.Int64}
	pending, err := s.q.ListChildPending(ctx, c.ID)
	if err != nil {
		return err
	}
	var settled, running, waiting []recoveredInput
	interrupted := false
	for _, p := range pending {
		recovered, err := s.engine.RecoveryOutcome(ctx, ref, p.ID)
		if err != nil {
			return err
		}
		in := recoveredInput{id: p.ID}
		switch recovered.State {
		case engine.RecoverySettled:
			in.outcome = recovered.Outcome
			settled = append(settled, in)
		case engine.RecoveryPlaced:
			in.outcome = engine.Outcome{Status: "unanswered", Reason: "interrupted"}
			// A run of user input resumes by itself. Only a parent turn in the run interrupts it.
			interrupted = interrupted || p.FromParent
			running = append(running, in)
		case engine.RecoveryQueued, engine.RecoveryAbsent:
			in.outcome = engine.Outcome{Status: "unanswered", Reason: "withdrawn"}
			waiting = append(waiting, in)
		}
	}
	withdrawn := 0
	if interrupted {
		settled = append(append(settled, running...), waiting...)
		withdrawn = len(waiting)
	}
	if len(settled) == 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var parents []int64
	for _, in := range settled {
		// Only an interrupted turn names the withdrawn inputs: they waited behind it.
		count := 0
		if in.outcome.Reason == "interrupted" {
			count = withdrawn
		}
		_, parent, err := s.settleIn(ctx, q, in.id, in.outcome, count)
		if err != nil {
			return err
		}
		if parent != 0 {
			parents = append(parents, parent)
		}
	}
	if interrupted {
		if err := q.RequestExecutionAbort(ctx, c.ID); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.signal(c.ID)
	for _, parent := range parents {
		s.signal(parent)
	}
	return nil
}
