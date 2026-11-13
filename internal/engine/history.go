package engine

import (
	"context"
	"fmt"
	"slices"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
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
	out.Items = brief((projection{entries: entries}).render())
	return out, nil
}

// historyBoundary reports an entry that a page can start at: a tool result never starts a page
// without its call.
func historyBoundary(e durable.EntryRecord) bool {
	return turnStart(e) || answer(e)
}

// turnStart reports an input or a context boundary, which starts a turn of the transcript.
func turnStart(e durable.EntryRecord) bool {
	// Withdrawn inputs can appear between a tool call and its result.
	switch e.Kind {
	case "dex.user", "dex.task_result", "pi.user", "pi.reset", "pi.compaction", CompactionNoteKind:
		return true
	}
	return false
}

func answer(e durable.EntryRecord) bool {
	if len(e.Model) > 0 {
		_, ok := e.Model[0].(ai.AssistantMessage)
		return ok
	}
	return false
}

// Detail returns one committed item with its full tool output or thought, for an item that brief
// cut. entryID is the entry of the item: for a tool, the entry of its call.
func (a *Agents) Detail(ctx context.Context, ref Ref, entryID int64, itemID string) (Item, error) {
	if entryID <= 0 || itemID == "" {
		return Item{}, fmt.Errorf("%w: detail requires an entry and an item", ErrInvalidInput)
	}
	if err := a.begin(); err != nil {
		return Item{}, err
	}
	defer a.end()
	conversation, err := a.conversation(ctx, ref)
	if err != nil {
		return Item{}, err
	}
	order := durable.ScanAscending
	query := durable.EntryQuery{MinEntryId: new(durable.EntryId(entryID)), Order: &order}
	page, err := conversation.Entries(ctx, query, 1, nil)
	if err != nil {
		return Item{}, err
	}
	if len(page.Items) == 0 || int64(page.Items[0].Id) != entryID {
		return Item{}, ErrItemNotFound
	}
	entries := page.Items
	item, ok := find((projection{entries: entries}).render(), itemID)
	if !ok {
		return Item{}, ErrItemNotFound
	}
	// The result of a call follows it, before the next answer.
	for cursor := page.Next; item.Kind == "tool" && cursor != nil && len(entries) == 1; {
		more, err := conversation.Entries(ctx, query, 16, *cursor)
		if err != nil {
			return Item{}, err
		}
		cursor = more.Next
		for _, e := range more.Items {
			if answer(e) {
				cursor = nil
				break
			}
			if slices.ContainsFunc(e.Model, func(m ai.Message) bool {
				result, ok := m.(ai.ToolResultMessage)
				return ok && result.ToolCallID == itemID
			}) {
				entries = append(entries, e)
				break
			}
		}
	}
	item, _ = find((projection{entries: entries}).render(), itemID)
	return item, nil
}

func find(items []Item, id string) (Item, bool) {
	i := slices.IndexFunc(items, func(item Item) bool { return item.ID == id })
	if i < 0 {
		return Item{}, false
	}
	return items[i], true
}
