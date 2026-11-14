package engine

import (
	"context"
	"sync"

	"github.com/MichaelKinsy/PiG/chord"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

type childActivityWatch struct {
	requests          map[string]bool
	closing           bool
	cancel            context.CancelFunc
	done              chan struct{}
	running, observed bool
}

// startActivityWatch shares a child watch across queued inputs. Replays do not own a second
// reference. The first input's settlement must not stop observation of the next input.
func (a *Agents) startActivityWatch(ctx context.Context, ref Ref, conversation harness.Conversation, input Input) (func(), error) {
	for {
		a.mu.Lock()
		prior := a.activityWatches[ref]
		if prior != nil && prior.closing {
			a.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-prior.done:
				continue
			}
		}
		if prior != nil {
			if prior.requests[input.RequestID] {
				a.mu.Unlock()
				return func() {}, nil
			}
			prior.requests[input.RequestID] = true
			a.mu.Unlock()
			return func() { a.releaseActivityWatch(prior, input.RequestID) }, nil
		}
		watchCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		owned := &childActivityWatch{requests: map[string]bool{input.RequestID: true}, cancel: cancel, done: make(chan struct{})}
		a.activityWatches[ref] = owned
		a.mu.Unlock()
		native, err := conversation.Watch(context.WithoutCancel(watchCtx), nil)
		if err != nil {
			cancel()
			a.mu.Lock()
			delete(a.activityWatches, ref)
			close(owned.done)
			a.mu.Unlock()
			return nil, err
		}
		var active activeContext
		update := func(view harness.ConversationView) error {
			event, err := active.project(watchCtx, conversation, view)
			if err != nil {
				return err
			}
			a.updateActivity(ref, event, view, activityNow())
			a.mu.Lock()
			changed := !owned.observed || owned.running != event.Running
			owned.running, owned.observed = event.Running, true
			a.mu.Unlock()
			if changed {
				if input.Changed != nil {
					input.Changed()
				}
			}
			return nil
		}
		if err := update(native.Value()); err != nil {
			cancel()
			_, _ = native.Stop()
			a.mu.Lock()
			delete(a.activityWatches, ref)
			close(owned.done)
			a.mu.Unlock()
			return nil, err
		}
		var delivery sync.Mutex
		closed := false
		native.Start(func(_ context.Context, call chord.WatchCall[harness.ConversationView]) error {
			if call.Kind != chord.WatchCallValue {
				cancel()
				return call.Error
			}
			chord.DeliveryEntered()
			delivery.Lock()
			defer delivery.Unlock()
			if closed {
				return watchCtx.Err()
			}
			err := update(call.Value)
			if err != nil {
				cancel()
			}
			return err
		})
		go func() {
			defer close(owned.done)
			select {
			case <-watchCtx.Done():
			case <-native.Closed():
			}
			cancel()
			_, _ = native.Stop()
			delivery.Lock()
			closed = true
			delivery.Unlock()
			a.mu.Lock()
			delete(a.activity, ref)
			if a.activityWatches[ref] == owned {
				delete(a.activityWatches, ref)
			}
			a.mu.Unlock()
			if input.Changed != nil {
				input.Changed()
			}
		}()
		return func() { a.releaseActivityWatch(owned, input.RequestID) }, nil
	}
}

func (a *Agents) releaseActivityWatch(owned *childActivityWatch, request string) {
	a.mu.Lock()
	delete(owned.requests, request)
	stop := len(owned.requests) == 0
	if stop {
		owned.closing = true
	}
	a.mu.Unlock()
	if stop {
		owned.cancel()
		<-owned.done
	}
}

// ChildRunning reads the existing child observation. It never opens an execution or reads its history.
func (a *Agents) ChildRunning(ref Ref) (running, available bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w := a.activityWatches[ref]
	if w == nil || w.closing || !w.observed {
		return false, false
	}
	return w.running, true
}
