package engine

import (
	"context"
	"fmt"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

// History reads durable entries. A page extends through the oldest result's assistant call.
func (a *Agents) History(ctx context.Context, ref Ref, before int64, limit int) (History, error) {
	out := History{Items: []Item{}}
	if before <= 0 || limit <= 0 {
		return out, fmt.Errorf("%w: history requires positive before and limit", ErrInvalidInput)
	}
	if err := a.begin(); err != nil {
		return out, err
	}
	defer a.end()
	conversation, err := a.conversation(ctx, ref)
	if err != nil {
		return out, err
	}
	query := durable.EntryQuery{MaxEntryId: new(durable.EntryId(before - 1))}
	page, err := conversation.Entries(ctx, query, limit, nil)
	if err != nil {
		return out, err
	}
	entries := page.Items
	cursor := page.Next
	for cursor != nil && len(entries) > 0 && !historyBoundary(entries[len(entries)-1]) {
		more, err := conversation.Entries(ctx, query, 1, *cursor)
		if err != nil {
			return out, err
		}
		entries = append(entries, more.Items...)
		cursor = more.Next
	}
	if cursor != nil && len(entries) > 0 {
		out.NextBefore = int64(entries[len(entries)-1].Id)
	}
	out.Items = (projection{snapshot: harness.SnapshotEvent{Entries: entries}}).render()
	return out, nil
}

func historyBoundary(e durable.EntryRecord) bool {
	switch e.Kind {
	case "dex.user", "dex.task_result", "dex.withdrawn", "pi.user", "pi.reset", "pi.compaction":
		return true
	}
	if len(e.Model) > 0 {
		_, ok := e.Model[0].(ai.AssistantMessage)
		return ok
	}
	return false
}
