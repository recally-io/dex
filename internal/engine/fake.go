package engine

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"strings"

	"github.com/MichaelKinsy/PiG/ai"
)

func fakeProvider() *ai.ModelsProvider {
	defs := []ai.FauxModelDefinition{}
	for _, id := range []string{"echo", "context", "shout", "bash", "memory", "section", "tool", "spawn", "think"} {
		defs = append(defs, ai.FauxModelDefinition{ID: id, Reasoning: id == "think"})
	}
	faux := ai.NewFauxProvider(ai.FauxConfig{ProviderID: "faux", Models: defs, TokensPerSecond: 200})
	reply := ai.FauxResponseFactory(func(t ai.TranscriptContext, o ai.StreamOptions, _ *ai.FauxProviderState, m *ai.Model) (ai.AssistantMessage, error) {
		text := lastUserText(t)
		var last ai.Message
		for _, message := range t.Messages() {
			if message.MessageRole() != "system" {
				last = message
			}
		}
		switch m.ID {
		case "context":
			users := []ai.UserContent{}
			for _, message := range t.Messages() {
				if user, ok := message.(ai.UserMessage); ok {
					users = append(users, user.Content)
				}
			}
			data, err := json.Marshal(users)
			return fakeText("context: " + string(data)), err
		case "memory":
			return fakeText("memory: " + fakeSection(t, "memory")), nil
		case "section":
			return fakeText("section: " + fakeSection(t, text)), nil
		case "think":
			level := string(o.Thinking)
			if level == "" {
				level = "off"
			}
			return fakeText("think " + level + ": " + text), nil
		case "bash":
			if result, ok := last.(ai.ToolResultMessage); ok {
				return fakeText("bash: " + strings.TrimSpace(ai.ContentText(result.Content, ""))), nil
			}
			return fakeCall("bash", map[string]any{"command": fakeBashCommand(text)}), nil
		case "tool":
			name, argsText, hasArgs := strings.Cut(text, " ")
			if result, ok := last.(ai.ToolResultMessage); ok && result.ToolName == name {
				prefix := "tool: "
				if result.IsError {
					prefix = "tool error: "
				}
				return fakeText(prefix + strings.TrimSpace(ai.ContentText(result.Content, ""))), nil
			}
			_, afterResult := last.(ai.ToolResultMessage)
			if !afterResult && !fakeExposed(t, name) {
				return fakeCall("tool_search", map[string]any{"query": name}), nil
			}
			args := map[string]any{}
			if hasArgs {
				if err := json.Unmarshal([]byte(argsText), &args); err != nil {
					return ai.AssistantMessage{}, fmt.Errorf("faux/tool requires JSON arguments")
				}
			}
			return fakeCall(name, args), nil
		case "spawn":
			if result, ok := last.(ai.ToolResultMessage); ok {
				prefix := "spawn: "
				if result.IsError {
					prefix = "spawn error: "
				}
				return fakeText(prefix + strings.TrimSpace(ai.ContentText(result.Content, ""))), nil
			}
			var args map[string]any
			if err := json.Unmarshal([]byte(text), &args); err != nil || args == nil {
				return ai.AssistantMessage{}, fmt.Errorf("faux/spawn requires a JSON object")
			}
			if result, ok := args["task-result"]; ok {
				next := args["nextTask"]
				if report, ok := result.(map[string]any); ok {
					if value, ok := report["report"].(string); ok && strings.HasPrefix(value, "echo: next-task: ") {
						if err := json.Unmarshal([]byte(strings.TrimPrefix(value, "echo: next-task: ")), &next); err != nil {
							return ai.AssistantMessage{}, fmt.Errorf("invalid next task")
						}
					}
				}
				if next != nil {
					task, ok := next.(map[string]any)
					if !ok {
						return ai.AssistantMessage{}, fmt.Errorf("invalid next task")
					}
					return fakeCall("spawn_task", task), nil
				}
				data, err := json.Marshal(result, json.Deterministic(true))
				return fakeText("task-result: " + string(data)), err
			}
			return fakeCall("spawn_task", args), nil
		default:
			return fakeText(m.ID + ": " + text), nil
		}
	})
	provider := faux.Provider()
	stream := provider.Stream
	provider.Stream = func(ctx context.Context, model *ai.Model, transcript ai.TranscriptContext, options ai.StreamOptions) (*ai.AssistantMessageEventStream, error) {
		// Queue each response before Stream consumes it; factories run asynchronously.
		faux.AppendResponses([]ai.FauxResponseStep{reply})
		return stream(ctx, model, transcript, options)
	}
	provider.StreamSimple = provider.Stream
	return provider
}
func fakeText(text string) ai.AssistantMessage {
	return ai.FauxAssistantMessage(ai.FauxContentString(text), ai.FauxAssistantMessageOptions{})
}
func fakeCall(name string, args map[string]any) ai.AssistantMessage {
	return ai.FauxAssistantMessage(ai.FauxContentBlocks{ai.FauxToolCall(name, args, nil)}, ai.FauxAssistantMessageOptions{StopReason: "toolUse"})
}
func lastUserText(t ai.TranscriptContext) string {
	for i := t.Len() - 1; i >= 0; i-- {
		if m, ok := t.At(i).(ai.UserMessage); ok {
			switch c := m.Content.(type) {
			case ai.UserText:
				return string(c)
			case ai.UserContentBlocks:
				return ai.ContentText(c, "")
			}
		}
	}
	return ""
}
func fakeSection(t ai.TranscriptContext, key string) string {
	text := ""
	for _, message := range t.Messages() {
		if m, ok := message.(ai.SystemMessage); ok {
			for _, section := range m.Sections {
				if section.Name == key {
					text = ""
					if section.Value != nil {
						text = *section.Value
					}
				}
			}
		}
	}
	return text
}
func fakeExposed(t ai.TranscriptContext, name string) bool {
	exposed := false
	for _, message := range t.Messages() {
		if m, ok := message.(ai.SystemMessage); ok {
			for _, tool := range m.ToolsRemoved {
				if tool.Name == name {
					exposed = false
				}
			}
			for _, tool := range m.ToolsAdded {
				if tool.Name == name {
					exposed = true
				}
			}
		}
	}
	return exposed
}
func fakeBashCommand(text string) string {
	if !strings.HasPrefix(text, "Chats since the previous dream") {
		return text
	}
	lines := []string{}
	for line := range strings.SplitSeq(text, "\n") {
		if strings.HasPrefix(line, "{") {
			lines = append(lines, "'"+strings.ReplaceAll(line, "'", "'\\''")+"'")
		}
	}
	return "printf '%s\\n' " + strings.Join(lines, " ") + " >> \"$DEX_DATA_DIR/memory/dreams.md\" && echo dreamed"
}
