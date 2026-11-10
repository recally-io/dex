package engine

import (
	"context"
	"fmt"

	"github.com/MichaelKinsy/PiG/chord/delta"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

// taskBindingDoc is private execution authority, not transcript metadata.
type taskBoundInput struct {
	ID   durable.SubmissionId `json:"id"`
	Kind string               `json:"kind"`
}

type taskBinding struct {
	Root   string                    `json:"root"`
	Inputs map[string]taskBoundInput `json:"inputs"`
	Sealed bool                      `json:"sealed"`
}

var taskBindingDoc = durable.DefineDoc(durable.DocDefinition[taskBinding]{
	Kind: "dex.task_binding", Version: 1, Initial: func() taskBinding { return taskBinding{Inputs: map[string]taskBoundInput{}} }, CheckpointWhen: func(taskBinding, []durable.Op, durable.CheckpointInfo) bool { return true },
	DocumentSemantics: durable.NewLatestConversationSemantics(durable.ForkInitial).DocumentSemantics,
})

func bindingDraft(tx durable.Tx, id durable.ConversationId) (*delta.Object, taskBinding, error) {
	doc, err := tx.Doc(taskBindingDoc, id)
	if err != nil {
		return nil, taskBinding{}, err
	}
	binding, err := durable.FromJsonValue[taskBinding](doc.Snapshot())
	return doc, binding, err
}

func saveBinding(doc *delta.Object, b taskBinding) error {
	value, err := durable.ToJsonValue(b.Inputs)
	if err != nil {
		return err
	}
	if err = doc.Set("root", b.Root); err != nil {
		return err
	}
	if err = doc.Set("inputs", value); err != nil {
		return err
	}
	return doc.Set("sealed", b.Sealed)
}

// SealTask atomically seals an idle, empty task. A false result means work is
// still running or queued. A sealed binding is never reopened.
func (a *Agents) SealTask(ctx context.Context, ref Ref, root string) (bool, error) {
	if root == "" {
		return false, fmt.Errorf("%w: task root required", ErrInvalidInput)
	}
	if err := a.begin(); err != nil {
		return false, err
	}
	defer a.end()
	unlock, err := a.lock(ctx, ref)
	if err != nil {
		return false, err
	}
	defer unlock()
	conversation, err := a.conversation(ctx, ref)
	if err != nil {
		return false, err
	}
	sealed := false
	_, err = conversation.Commit(ctx, func(tx durable.Tx) (any, error) {
		doc, b, err := bindingDraft(tx, conversation.Id())
		if err != nil {
			return nil, err
		}
		if b.Root != root {
			return nil, fmt.Errorf("%w: task binding", ErrNotFound)
		}
		if b.Sealed {
			sealed = true
			return nil, nil
		}
		liveDoc, err := tx.Doc(harness.LiveDoc, conversation.Id())
		if err != nil {
			return nil, err
		}
		live, err := durable.FromJsonValue[harness.LiveState](liveDoc.Snapshot())
		if err != nil {
			return nil, err
		}
		inboxDoc, err := tx.Doc(harness.InboxDoc, conversation.Id())
		if err != nil {
			return nil, err
		}
		inbox, err := durable.FromJsonValue[harness.InboxState](inboxDoc.Snapshot())
		if err != nil {
			return nil, err
		}
		if live.Run != nil || len(inbox.Items) != 0 {
			return nil, nil
		}
		// PiG's run and inbox omit background work, including compaction.
		// Query each nonterminal status with limit one rather than walking history.
		id := conversation.Id()
		for _, status := range []durable.TaskStatus{durable.TaskPending, durable.TaskRunning, durable.TaskWaiting, durable.TaskSleeping, durable.TaskCompleting} {
			page, err := tx.ScanTasks(durable.TaskQuery{ConversationId: &id, Status: &status}, 1, nil)
			if err != nil {
				return nil, err
			}
			if len(page.Items) != 0 {
				return nil, nil
			}
		}
		b.Sealed = true
		if err = saveBinding(doc, b); err != nil {
			return nil, err
		}
		sealed = true
		return nil, nil
	})
	return sealed, err
}
