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
		edit = engine.ExtensionEdit{Remove: []string{"automations"}, Add: []string{"automation-check", "bots"}}
	}
	if input.Main {
		edit = engine.ExtensionEdit{Remove: []string{"work-loop"}, Add: []string{"front-desk"}}
	}
	if interactive {
		edit.Add = append(edit.Add, "channel-read", "channel-manage", "ask-user", "outbox", "bots", "bot-manage", "tasks")
	}
	switch {
	case input.Child:
		edit = engine.ExtensionEdit{
			Remove: []string{"sessions", "work-loop", "front-desk", "automations", "outbox", "tasks"},
			Add:    []string{"child-rules", "child-outbox"},
		}
		if input.ChildHasTask {
			edit.Add = append(edit.Add, "child-tasks")
		} else {
			edit.Remove = append(edit.Remove, "child-tasks")
		}
		return edit
	case dream:
		return engine.ExtensionEdit{
			Remove: []string{"sessions", "session-reading", "monitors", "work-loop", "automations", "artifact-delivery"},
			Add:    []string{"dream-message"},
		}
	}
	return edit
}
