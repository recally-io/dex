package engine

import (
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

func (a *Agents) updateActivity(ref Ref, event Event, view harness.ConversationView, observedAt int64) {
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
	lastAction := ""
	for _, item := range slices.Backward(event.Items) {
		if item.Kind == "tool" {
			lastAction = shortAction(item.Name, item.Args)
			break
		}
	}
	var running []RunningTool
	for _, slot := range live.Tools {
		if slot.Status == harness.ToolSlotRunning {
			running = append(running, RunningTool{Name: safeToolName(slot.Name), FirstObservedAt: current[slot.CallId]})
		}
	}
	a.activity[ref] = &activityRecord{lastAction: lastAction, lastActivityAt: observedAt, toolStarted: current, runningTools: running}
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
