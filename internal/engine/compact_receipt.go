package engine

import (
	"context"
	"fmt"

	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

type compactReceipt struct {
	TaskID         durable.TaskId         `json:"taskId"`
	ConversationID durable.ConversationId `json:"conversationId"`
	Instructions   string                 `json:"instructions"`
}

var compactReceipts = durable.DefineDocFamily(durable.DocFamilyDefinition[compactReceipt, string]{
	Kind: "dex.compact.receipt", Version: 1, Family: true,
	DocumentSemantics: durable.DocumentSemantics{Scope: durable.ScopeSession},
	Initial:           func(string) compactReceipt { return compactReceipt{} },
	CheckpointWhen:    func(compactReceipt, []durable.Op, durable.CheckpointInfo) bool { return false },
})

type compactAdmission struct {
	id      durable.TaskId
	created bool
}

// compactOnce commits the operation key with the PiG compaction task.
func compactOnce(ctx context.Context, conversation harness.Conversation, conversationID durable.ConversationId, requestID, instructions string) (compactAdmission, error) {
	result, err := conversation.Commit(ctx, func(tx durable.Tx) (any, error) {
		doc, err := tx.Doc(compactReceipts, requestID, requestID)
		if err != nil {
			return nil, err
		}
		receipt, err := durable.FromJsonValue[compactReceipt](doc.Snapshot())
		if err != nil {
			return nil, err
		}
		if receipt.TaskID != 0 {
			if receipt.ConversationID != conversationID || receipt.Instructions != instructions {
				return nil, fmt.Errorf("%w: compact request ID belongs to another operation", ErrInvalidInput)
			}
			return compactAdmission{id: receipt.TaskID}, nil
		}
		var keep *string
		if instructions != "" {
			keep = &instructions
		}
		id, err := harness.CreateCompaction(tx, conversationID, harness.CompactionInput{Reason: durable.CompactionManual, Instructions: keep}, nil)
		if err != nil {
			return nil, err
		}
		if err := doc.Set("taskId", int64(id)); err != nil {
			return nil, err
		}
		if err := doc.Set("conversationId", int64(conversationID)); err != nil {
			return nil, err
		}
		if err := doc.Set("instructions", instructions); err != nil {
			return nil, err
		}
		return compactAdmission{id: id, created: true}, nil
	})
	if err != nil {
		return compactAdmission{}, err
	}
	return result.(compactAdmission), nil
}
