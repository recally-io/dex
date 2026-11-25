package app

import (
	"strings"

	"github.com/recally-io/dex/internal/engine"
)

func inputExtensions(input engine.Input) engine.ExtensionEdit {
	dream := input.Origin == "dream" || strings.Contains(input.Source, "/dream/")
	interactive := !input.Automation && !input.Child && !dream
	var edit engine.ExtensionEdit
	if input.Automation {
		edit = engine.ExtensionEdit{Remove: []string{"routines"}, Add: []string{"automation-check", "bots"}}
	}
	if input.Main {
		edit = engine.ExtensionEdit{Remove: []string{"work-loop"}, Add: []string{"front-desk"}}
	}
	if interactive {
		edit.Add = append(edit.Add, "channel-read", "channel-manage", "ask-user", "outbox", "bots", "bot-manage", "work-tasks")
	}
	switch {
	case input.Child:
		// Repeat this edit for a child that still has extensions from an older release.
		edit = engine.ExtensionEdit{
			Remove: []string{"sessions", "work-loop", "front-desk", "routines", "outbox", "work-tasks"},
			Add:    []string{"child-rules", "child-outbox"},
		}
		if input.ChildHasWorkTask {
			edit.Add = append(edit.Add, "child-work-tasks")
		} else {
			edit.Remove = append(edit.Remove, "child-work-tasks")
		}
		return edit
	case dream:
		return engine.ExtensionEdit{
			Remove: []string{"sessions", "session-reading", "monitors", "work-loop", "routines", "artifact-delivery"},
			Add:    []string{"dream-message"},
		}
	}
	return edit
}
