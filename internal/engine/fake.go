package engine

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"regexp"
	"strings"

	"github.com/MichaelKinsy/PiG/ai"
)

func fakeProvider() *ai.ModelsProvider {
	defs := []ai.FauxModelDefinition{}
	for _, id := range []string{"echo", "context", "prompt", "direct", "direct-content", "shout", "bash", "memory", "section", "tool", "tool-report-fail", "spawn", "spawn-notify", "spawn-monitor", "think", "mixed", "files"} {
		defs = append(defs, ai.FauxModelDefinition{ID: id, Reasoning: id == "think"})
	}
	defs = append(defs, ai.FauxModelDefinition{ID: "section-small", ContextWindow: 36000})
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
		case "prompt":
			systems := []ai.Message{}
			for _, message := range t.Messages() {
				if message.MessageRole() == "system" {
					systems = append(systems, message)
				}
			}
			body, err := json.Marshal(systems)
			if err != nil {
				return ai.AssistantMessage{}, err
			}
			pattern, err := regexp.Compile(text)
			if err != nil {
				return ai.AssistantMessage{}, err
			}
			return fakeText(strings.Join(pattern.FindAllString(string(body), -1), ",")), nil
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
		case "section", "section-small":
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
			return fakeCall("bash", map[string]any{"command": fakeBashCommand(strings.TrimSuffix(text, boundRoutineCheckSuffix))}), nil
		case "tool", "tool-report-fail", "direct", "direct-content":
			// The fixture parses the user prompt as literal arguments, not natural language.
			text = strings.TrimSuffix(text, boundRoutineCheckSuffix)
			if strings.HasPrefix(text, "Dex Task: ") {
				if _, body, ok := strings.Cut(text, "\n\n"); ok {
					text = body
				}
			}
			name, argsText, hasArgs := strings.Cut(text, " ")
			if result, ok := last.(ai.ToolResultMessage); ok && result.ToolName == name {
				if m.ID == "tool-report-fail" && name == "session_report" {
					return ai.AssistantMessage{}, fmt.Errorf("faux scripted failure after session_report")
				}
				prefix := "tool: "
				if result.IsError {
					prefix = "tool error: "
				}
				if m.ID == "direct-content" {
					body, err := json.Marshal(result.Content)
					return fakeText(prefix + string(body)), err
				}
				return fakeText(prefix + strings.TrimSpace(ai.ContentText(result.Content, ""))), nil
			}
			_, afterResult := last.(ai.ToolResultMessage)
			if (m.ID == "tool" || m.ID == "tool-report-fail") && !afterResult && !fakeExposed(t, name) {
				return fakeCall("tool_search", map[string]any{"query": name}), nil
			}
			args := map[string]any{}
			if hasArgs {
				if err := json.Unmarshal([]byte(argsText), &args); err != nil {
					return ai.AssistantMessage{}, fmt.Errorf("faux/tool requires JSON arguments")
				}
			}
			return fakeCall(name, args), nil
		case "files":
			if result, ok := last.(ai.ToolResultMessage); ok && result.ToolName == "deliver_file" {
				return fakeText("files: done"), nil
			}
			if !fakeExposed(t, "deliver_file") {
				return fakeCall("tool_search", map[string]any{"query": "deliver_file"}), nil
			}
			var paths []string
			if err := json.Unmarshal([]byte(text), &paths); err != nil {
				return ai.AssistantMessage{}, err
			}
			calls := ai.FauxContentBlocks{}
			for _, path := range paths {
				calls = append(calls, ai.FauxToolCall("deliver_file", map[string]any{"path": path}, nil))
			}
			return ai.FauxAssistantMessage(calls, ai.FauxAssistantMessageOptions{StopReason: "toolUse"}), nil
		case "mixed":
			// One message with two tool calls: ask_user from the JSON in the message, and bash.
			for _, message := range t.Messages() {
				if result, ok := message.(ai.ToolResultMessage); ok && result.ToolName == "ask_user" {
					return fakeText("mixed: " + strings.TrimSpace(ai.ContentText(result.Content, ""))), nil
				}
			}
			_, argsText, _ := strings.Cut(text, " ")
			args := map[string]any{}
			if err := json.Unmarshal([]byte(argsText), &args); err != nil {
				return ai.AssistantMessage{}, fmt.Errorf("faux/mixed requires ask_user JSON arguments")
			}
			return ai.FauxAssistantMessage(ai.FauxContentBlocks{ai.FauxToolCall("ask_user", args, nil), ai.FauxToolCall("bash", map[string]any{"command": "echo mixed"}, nil)}, ai.FauxAssistantMessageOptions{StopReason: "toolUse"}), nil
		case "spawn", "spawn-notify", "spawn-monitor":
			if result, ok := last.(ai.ToolResultMessage); ok && (m.ID != "spawn-notify" || result.ToolName != "tool_search") {
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
				if m.ID == "spawn-notify" && args["channel-follow-up"] != nil {
					if !fakeExposed(t, "notify_user") {
						return fakeCall("tool_search", map[string]any{"query": "notify_user"}), nil
					}
					return fakeCall("notify_user", map[string]any{"content": "Reviewed channel conclusion"}), nil
				}
				data, err := json.Marshal(result, json.Deterministic(true))
				return fakeText("task-result: " + string(data)), err
			}
			if result, ok := args["background-result"]; ok {
				if wake, ok := result.(map[string]any); m.ID == "spawn-monitor" && ok && wake["event"] == "exited" {
					switch wake["label"] {
					case "chain fixture":
						return fakeCall("monitor_set_command", map[string]any{"command": "printf ready", "wake": "exit", "label": "chain fixture"}), nil
					case "check fixture":
						return fakeCall("monitor_set_schedule", map[string]any{"delaySeconds": 60, "maxRuns": 2, "intervalSeconds": 60, "prompt": "fixture ready", "checkCommand": "exit 10"}), nil
					}
				}
				data, err := json.Marshal(result, json.Deterministic(true))
				return fakeText("background-result: " + string(data)), err
			}
			// The optional "tool" field names a session tool; the default creates a child session.
			name := "session_new"
			if tool, ok := args["tool"].(string); ok {
				name = tool
				delete(args, "tool")
			}
			return fakeCall(name, args), nil
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
