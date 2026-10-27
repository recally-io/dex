package engine

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

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
	native, err := conversation.Watch(watchCtx)
	if err != nil {
		cancel()
		a.mu.Lock()
		a.releaseActivityWatch(ref)
		a.mu.Unlock()
		return nil, err
	}
	update := func(view harness.ConversationView) {
		event, err := projectView(view)
		if err == nil {
			a.updateActivity(ref, event, harness.ViewSnapshotEvent(view), activityNow())
		}
	}
	update(native.Value())
	done := make(chan struct{})
	native.Start(func(_ context.Context, view harness.ConversationView, _ []durable.Op) error {
		update(view)
		return nil
	})
	go func() {
		defer close(done)
		defer func() {
			a.mu.Lock()
			a.releaseActivityWatch(ref)
			a.mu.Unlock()
		}()
		<-watchCtx.Done()
		_, _ = native.Stop()
	}()
	return func() {
		cancel()
		<-done
		a.mu.Lock()
		delete(a.activity, ref)
		a.mu.Unlock()
	}, nil
}

// releaseActivityWatch requires a.mu.
func (a *Agents) releaseActivityWatch(ref Ref) {
	if a.activityWatches[ref]--; a.activityWatches[ref] <= 0 {
		delete(a.activityWatches, ref)
	}
}

func (a *Agents) updateActivity(ref Ref, event Event, snapshot harness.SnapshotEvent, observedAt int64) {
	if !event.Running {
		a.mu.Lock()
		delete(a.activity, ref)
		a.mu.Unlock()
		return
	}
	current := map[string]int64{}
	a.mu.Lock()
	prior := a.activity[ref]
	if prior != nil {
		for _, slot := range snapshot.Tools {
			if slot.Status == harness.ToolSlotRunning {
				if started, ok := prior.toolStarted[slot.CallId]; ok {
					current[slot.CallId] = started
				}
			}
		}
	}
	for _, slot := range snapshot.Tools {
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
