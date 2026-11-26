package engine

import (
	"time"

	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

func (a *Bots) updateActivity(ref Ref, event Event, view harness.ConversationView, observedAt int64) {
	if !event.Running {
		a.mu.Lock()
		delete(a.activity, ref)
		a.mu.Unlock()
		return
	}
	var live harness.LiveState
	doc, ok := view.Docs.Get("pi.live")
	if !ok {
		a.mu.Lock()
		delete(a.activity, ref)
		a.mu.Unlock()
		return
	}
	live, err := durable.FromJsonValue[harness.LiveState](doc)
	if err != nil {
		a.mu.Lock()
		delete(a.activity, ref)
		a.mu.Unlock()
		return
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
	var running []RunningTool
	for _, slot := range live.Tools {
		if slot.Status == harness.ToolSlotRunning {
			running = append(running, RunningTool{Name: safeToolName(slot.Name), FirstObservedAt: current[slot.CallId]})
		}
	}
	a.activity[ref] = &activityRecord{lastActivityAt: observedAt, toolStarted: current, runningTools: running}
	a.mu.Unlock()
}

// Tool names from extensions and unoffered calls can contain private data.
func safeToolName(name string) string {
	switch name {
	case "bash", "read", "write", "edit", "grep", "find", "ls":
		return name
	default:
		return "other"
	}
}

func activityNow() int64 { return time.Now().UnixMilli() }
