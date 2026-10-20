package engine

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"

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
	native, err := conversation.Watch(context.WithoutCancel(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	current, err := projectView(native.Value())
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
	native.Start(func(_ context.Context, v harness.ConversationView, _ []durable.Op) error {
		delivery.Lock()
		defer delivery.Unlock()
		if closed {
			return ctx.Err()
		}
		next, err := projectView(v)
		if err != nil {
			return err
		}
		changes, ok := diff(current.Items, next.Items)
		if !ok || current.HistoryBefore != next.HistoryBefore {
			changes = []Event{next}
		} else if current.Running != next.Running || !reflect.DeepEqual(current.Retry, next.Retry) {
			changes = append(changes, Event{Type: "run", Running: next.Running, Retry: next.Retry})
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

func projectView(view harness.ConversationView) (Event, error) {
	p := projection{snapshot: harness.ViewSnapshotEvent(view)}
	if doc, ok := view.Docs.Get("pi.inbox"); ok {
		inbox, err := durable.FromJsonValue[harness.InboxState](doc)
		if err != nil {
			return Event{}, fmt.Errorf("native inbox: %w", err)
		}
		p.inbox = inbox.Items
	}
	return p.event(), nil
}
