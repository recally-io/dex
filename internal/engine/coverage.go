package engine

import (
	"context"
	"reflect"

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

// runPeers lists the earlier inputs that may start the run a new input joins. It reads the run and
// the inbox in the commit that admits the input, so no run can start between the read and the
// admission. Admission is the only moment this fact is known: a later run replaces the live run
// and clears its failure.
//
// A followUp starts its own run, together with every queued steer. An earlier followUp in the queue
// runs first, so it has no peers. A steer joins the live run at the next tool step. When the run
// ends first, the steer starts the next run together with the other queued steers and the first
// queued followUp. The list names each input that could be the starter. The settlement tells which
// one was.
func runPeers(tx durable.Tx, conversation durable.ConversationId, whenBusy durable.WhenBusy) ([]int64, error) {
	live, err := durable.TxDoc[harness.LiveState](tx, harness.LiveDoc, conversation)
	if err != nil {
		return nil, err
	}
	inbox, err := durable.TxDoc[harness.InboxState](tx, harness.InboxDoc, conversation)
	if err != nil {
		return nil, err
	}
	state, err := durable.DecodeDoc[harness.LiveState](live.Snapshot())
	if err != nil {
		return nil, err
	}
	queue, err := durable.DecodeDoc[harness.InboxState](inbox.Snapshot())
	if err != nil {
		return nil, err
	}
	var firstSteer, firstFollowUp durable.SubmissionId
	for _, item := range queue.Items {
		switch {
		case item.Mode == harness.InboxSteer && firstSteer == 0:
			firstSteer = item.Id
		case item.Mode == harness.InboxFollowUp && firstFollowUp == 0:
			firstFollowUp = item.Id
		}
	}
	var peers []durable.SubmissionId
	switch whenBusy {
	case durable.WhenBusySteer:
		if state.Run != nil && len(state.Run.Inputs) > 0 {
			peers = append(peers, state.Run.Inputs[0])
		}
		peers = append(peers, firstSteer, firstFollowUp)
	case durable.WhenBusyFollowUp:
		if firstFollowUp == 0 {
			peers = append(peers, firstSteer)
		}
	case durable.WhenBusyReject:
	}
	var ids []int64
	for _, peer := range peers {
		if peer != 0 {
			ids = append(ids, int64(peer))
		}
	}
	return ids, nil
}

// covered reports whether an earlier input started the run of an input. The result of that starter
// speaks for this input, so it sends none of its own. The admission wrote the peers into the input
// entry, and the native record of each peer says whether it shared the run:
//
//   - placed: a placed peer is in the one run that exists.
//   - done: a peer with the same answer entry ran together with this input. This is exact.
//   - unanswered: a peer that ran and failed with the same reason and detail. Two runs that fail with
//     the same error look alike, so one result can stand for both. The parent still hears the error.
//
// A queued input has not joined a run yet. Only persisted native state decides, so a restart and a
// retried receipt give the same answer as the first settlement.
func covered(ctx context.Context, store durable.Storage, record *durable.SubmissionRecord) (bool, error) {
	if record.Status == durable.SubmissionQueued || record.Entry == nil {
		return false, nil
	}
	entry, err := store.Entry(ctx, *record.Entry)
	if err != nil || entry == nil || entry.Entry.Data == nil {
		return false, err
	}
	meta, err := durable.FromJsonValue[InputMetadata](entry.Entry.Data)
	if err != nil {
		return false, err
	}
	for _, id := range meta.RunPeers {
		peer, err := store.Submission(ctx, durable.SubmissionId(id))
		if err != nil {
			return false, err
		}
		if peer != nil && sharesRun(record, peer) {
			return true, nil
		}
	}
	return false, nil
}

func sharesRun(record, peer *durable.SubmissionRecord) bool {
	switch record.Status {
	case durable.SubmissionPlaced:
		return peer.Status == durable.SubmissionPlaced
	case durable.SubmissionDone:
		return peer.Status == durable.SubmissionDone && peer.Answer != nil && record.Answer != nil && *peer.Answer == *record.Answer
	case durable.SubmissionUnanswered:
		return peer.Status == durable.SubmissionUnanswered && peer.Entry != nil && peer.Reason != nil && record.Reason != nil &&
			*peer.Reason == *record.Reason && reflect.DeepEqual(peer.Detail, record.Detail)
	case durable.SubmissionQueued:
	}
	return false
}
