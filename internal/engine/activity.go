package engine

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/MichaelKinsy/PiG/chord"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

const activityActionLimit = 80

// startActivityWatch observes one admitted background conversation without exposing a user stream.
// The caller owns the returned cleanup function and must call it when the submission settles.
// A Submit replay reuses the first watch; its cleanup is a no-op.
func (a *Agents) startActivityWatch(ctx context.Context, ref Ref, conversation harness.Conversation) (func(), error) {
	a.mu.Lock()
	if a.activityWatches[ref] > 0 {
		a.mu.Unlock()
		return func() {}, nil
	}
	a.activityWatches[ref]++
	a.mu.Unlock()
	watchCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	native, err := conversation.Watch(watchCtx, nil)
	if err != nil {
		cancel()
		a.mu.Lock()
		a.releaseActivityWatch(ref)
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
		return nil
	}
	if err := update(native.Value()); err != nil {
		cancel()
		_, _ = native.Stop()
		a.mu.Lock()
		a.releaseActivityWatch(ref)
		a.mu.Unlock()
		return nil, err
	}
	done := make(chan struct{})
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
			return watchCtx.Err()
		}
		return update(call.Value)
	})
	go func() {
		defer close(done)
		<-watchCtx.Done()
		_, _ = native.Stop()
		// Native Stop does not join a callback that has already started.
		delivery.Lock()
		defer delivery.Unlock()
		closed = true
		a.mu.Lock()
		delete(a.activity, ref)
		a.releaseActivityWatch(ref)
		a.mu.Unlock()
	}()
	return func() { cancel(); <-done }, nil
}

// releaseActivityWatch requires a.mu.
func (a *Agents) releaseActivityWatch(ref Ref) {
	if a.activityWatches[ref]--; a.activityWatches[ref] <= 0 {
		delete(a.activityWatches, ref)
	}
}

func (a *Agents) updateActivity(ref Ref, event Event, view harness.ConversationView, observedAt int64) {
	if !event.Running {
		a.mu.Lock()
		delete(a.activity, ref)
		a.mu.Unlock()
		return
	}
	var live harness.LiveState
	if doc, ok := view.Docs.Get("pi.live"); ok {
		var err error
		live, err = durable.FromJsonValue[harness.LiveState](doc)
		if err != nil {
			return
		}
	}
	current := map[string]int64{}
	a.mu.Lock()
	prior := a.activity[ref]
	if prior != nil {
		for _, slot := range live.Tools {
			if slot.Status == harness.ToolSlotRunning {
				if started, ok := prior.toolStarted[slot.CallId]; ok {
					current[slot.CallId] = started
				}
			}
		}
	}
	for _, slot := range live.Tools {
		if slot.Status == harness.ToolSlotRunning {
			if _, ok := current[slot.CallId]; !ok {
				current[slot.CallId] = observedAt
			}
		}
	}
	lastAction := ""
	for _, item := range slices.Backward(event.Items) {
		if item.Kind == "tool" {
			lastAction = shortAction(item.Name, item.Args)
			break
		}
	}
	a.activity[ref] = &activityRecord{lastAction: lastAction, lastActivityAt: observedAt, toolStarted: current}
	a.mu.Unlock()
}

func shortAction(name string, args map[string]any) string {
	if name == "" {
		return ""
	}
	text := ""
	for _, key := range []string{"command", "cmd", "query", "path"} {
		if value, ok := args[key]; ok {
			text = fmt.Sprint(value)
			break
		}
	}
	if text == "" && len(args) > 0 {
		if raw, err := json.Marshal(args); err == nil {
			text = string(raw)
		}
	}
	action := name
	if text != "" {
		action += ": " + strings.Join(strings.Fields(text), " ")
	}
	if utf8.RuneCountInString(action) <= activityActionLimit {
		return action
	}
	return string([]rune(action)[:activityActionLimit])
}

func activityNow() int64 { return time.Now().UnixMilli() }
