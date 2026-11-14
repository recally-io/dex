package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"

	"github.com/MichaelKinsy/PiG/chord"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

// Watch owns one native subscription. A slow reader reconnects for a fresh snapshot.
type Watch struct {
	Snapshot Event
	Events   <-chan Event
	cancel   context.CancelFunc
	done     <-chan struct{}
}

// Close cancels this subscription and waits for cleanup. Other subscriptions remain active.
func (w *Watch) Close() { w.cancel(); <-w.done }

// Watch captures the snapshot and subscription at the same native revision.
func (a *Agents) Watch(ctx context.Context, ref Ref) (*Watch, error) {
	if err := a.begin(); err != nil {
		return nil, err
	}
	defer a.end()
	conversation, err := a.conversation(ctx, ref)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	// The SDK panics if cancellation stops the native watch before Start.
	// Link request cancellation after Start through the cleanup goroutine below.
	if err := ctx.Err(); err != nil {
		cancel()
		return nil, err
	}
	native, err := conversation.Watch(context.WithoutCancel(ctx), nil)
	if err != nil {
		cancel()
		return nil, err
	}
	var active activeContext
	current, err := active.project(ctx, conversation, native.Value())
	if err != nil {
		cancel()
		_, _ = native.Stop()
		return nil, err
	}
	events := make(chan Event, 256)
	done := make(chan struct{})
	w := &Watch{Snapshot: current, Events: events, cancel: cancel, done: done}
	var delivery sync.Mutex
	closed := false
	native.Start(func(_ context.Context, call chord.WatchCall[harness.ConversationView]) error {
		if call.Kind != chord.WatchCallValue {
			return call.Error
		}
		chord.DeliveryEntered()
		delivery.Lock()
		defer delivery.Unlock()
		if closed {
			return ctx.Err()
		}
		next, err := active.project(ctx, conversation, call.Value)
		if err != nil {
			return err
		}
		changes, ok := diff(current.Items, next.Items)
		if !ok || current.HistoryBefore != next.HistoryBefore {
			changes = []Event{next}
		} else if current.Running != next.Running || current.Phase != next.Phase || !reflect.DeepEqual(current.Retry, next.Retry) {
			changes = append(changes, Event{Type: "run", Running: next.Running, Retry: next.Retry, Phase: next.Phase})
		}
		current = next
		for _, event := range changes {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case events <- event:
			default:
				return errors.New("transcript reader fell behind")
			}
		}
		return nil
	})
	go func() {
		defer close(done)
		defer cancel()
		select {
		case <-ctx.Done():
		case <-native.Closed():
		}
		_, _ = native.Stop()
		delivery.Lock()
		closed = true
		close(events)
		delivery.Unlock()
	}()
	return w, nil
}

// snapshotTurns is the number of recent turns in a first snapshot. Older turns load as history.
// A turn counts once however many tool calls it has, because its steps fold into one row.
const snapshotTurns = 20

// snapshotEntries caps the window for a long turn, such as a child session that runs hundreds of
// tool calls for one input. Each committed step of a run sends the window again.
const snapshotEntries = 300

// activeContext keeps the recent window of the active context without retaining archived history.
// Entries are immutable, so a read pinned to the view's tail matches that revision's docs.
// Refresh on entry changes only; streamed tokens reuse the same entries.
type activeContext struct {
	tail    durable.EntryId
	entries []durable.EntryRecord
	// from is the first entry of the window, or 0 for the full context. It is fixed for the
	// subscription, so a later snapshot keeps the history cursor of the client.
	from durable.EntryId
}

func (active *activeContext) project(ctx context.Context, conversation harness.Conversation, view harness.ConversationView) (Event, error) {
	if len(view.Entries) > 0 {
		tail := view.Entries[len(view.Entries)-1].Id
		if tail != active.tail {
			context, err := conversation.Context(ctx, &durable.ContextOptions{At: &tail})
			if err != nil {
				return Event{}, err
			}
			entries := context.Entries
			if active.tail == 0 {
				active.from = windowStart(entries)
			}
			if active.from > 0 {
				entries = slices.DeleteFunc(entries, func(e durable.EntryRecord) bool { return e.Id < active.from })
			}
			active.tail, active.entries = tail, entries
		}
	}
	view.Entries = active.entries
	event, err := projectView(view)
	event.HistoryBefore = max(event.HistoryBefore, int64(active.from))
	return event, err
}

// windowStart returns the first entry of the last snapshotTurns turns, or 0 when the context has
// no more turns. When those turns hold more than snapshotEntries entries, the window starts at the
// history boundary before the cap, so a tool result keeps its call.
func windowStart(entries []durable.EntryRecord) durable.EntryId {
	sorted := slices.SortedFunc(slices.Values(entries), func(a, b durable.EntryRecord) int { return cmp.Compare(a.Id, b.Id) })
	start, turns := 0, 0
	for i := len(sorted) - 1; i > 0 && turns < snapshotTurns; i-- {
		if turnStart(sorted[i]) {
			start, turns = i, turns+1
		}
	}
	if turns < snapshotTurns {
		start = 0
	}
	if len(sorted)-start > snapshotEntries {
		start = len(sorted) - snapshotEntries
		for start > 0 && !historyBoundary(sorted[start]) {
			start--
		}
	}
	if start == 0 {
		return 0
	}
	return sorted[start].Id
}

func projectView(view harness.ConversationView) (Event, error) {
	p := projection{entries: view.Entries}
	if doc, ok := view.Docs.Get("pi.live"); ok {
		live, err := durable.FromJsonValue[harness.LiveState](doc)
		if err != nil {
			return Event{}, fmt.Errorf("native live state: %w", err)
		}
		p.live = live
	}
	if doc, ok := view.Docs.Get("pi.inbox"); ok {
		inbox, err := durable.FromJsonValue[harness.InboxState](doc)
		if err != nil {
			return Event{}, fmt.Errorf("native inbox: %w", err)
		}
		p.inbox = inbox.Items
	}
	return p.event(), nil
}
