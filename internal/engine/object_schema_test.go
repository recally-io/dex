package engine_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/agent"
	"github.com/recally-io/dex/internal/artifact"
	"github.com/recally-io/dex/internal/automation"
	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/engine"
	worktasktools "github.com/recally-io/dex/internal/worktask/tools"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestObjectSchemaWithoutRequiredEncodesEmptyArray(t *testing.T) {
	data, err := json.Marshal(engine.ObjectSchema(nil, map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"required":[]`) {
		t.Fatalf("required must be an empty array: %s", data)
	}
}

// Providers reject the whole request when one tool schema is invalid, so check every built-in tool.
func TestBuiltInToolSchemasEncodeValidJSONSchema(t *testing.T) {
	link := worktasktools.WorkTaskLinkTool{Name: "task_link_test", Properties: map[string]any{"n": map[string]any{"type": "integer"}}}
	const id = "bot"
	runtime := engine.ExtensionRuntime{AgentID: id}
	extensions := append(engine.BuiltInSchemaExtensions(),
		chat.CheckExtension(id, nil), automation.RoutineExtension(id, automation.RoutineTools{}),
		chat.SessionExtension(id, chat.SessionHandlers{}), chat.SessionReadExtension(id, chat.SessionHandlers{}),
		chat.WorkLoopExtension(), artifact.Extension(runtime, nil), chat.FrontDeskExtension(),
		chat.OutboxExtension(id, chat.OutboxTools{}), chat.ChildOutboxExtension(id, chat.OutboxTools{}), chat.MonitorExtension(runtime, chat.MonitorTools{}),
		chat.AskUserExtension(id, nil), agent.BotReadExtension(id, agent.BotHandlers{}), agent.BotManageExtension(id, agent.BotHandlers{}),
		worktasktools.WorkTaskExtension(id, worktasktools.WorkTaskTools{Links: []worktasktools.WorkTaskLinkTool{link}}),
		chat.ChildExtension(), chat.DreamMessageExtension(id, nil), chat.RoutineCheckInstructions(),
	)
	extensions = append(extensions, agent.ChannelExtensions(id, agent.ChannelTools{})...)
	count := 0
	for _, extension := range extensions {
		for _, tool := range extension.Tools {
			count++
			if err := checkToolSchema(tool.Parameters); err != nil {
				data, _ := json.Marshal(tool.Parameters)
				t.Errorf("%s: %v\n%s", tool.Name, err, data)
			}
		}
	}
	if count == 0 {
		t.Fatal("no tools were checked")
	}
	t.Logf("checked %d tool schemas", count)
}

// checkToolSchema checks the encoded schema that a provider receives.
func checkToolSchema(parameters map[string]any) error {
	data, err := json.Marshal(parameters)
	if err != nil {
		return err
	}
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return err
	}
	// The compiler validates the document against the draft 2020-12 metaschema.
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("tool.json", doc); err != nil {
		return err
	}
	if _, err := compiler.Compile("tool.json"); err != nil {
		return fmt.Errorf("metaschema: %w", err)
	}
	root := doc.(map[string]any)
	if root["type"] != "object" {
		return fmt.Errorf("the root type must be object")
	}
	// OpenAI rejects these keywords at the root of function parameters.
	for _, key := range []string{"oneOf", "anyOf", "allOf", "not", "enum"} {
		if _, ok := root[key]; ok {
			return fmt.Errorf("the root has %s", key)
		}
	}
	return checkSchemaNode(doc, "$")
}

// checkSchemaNode adds rules that the metaschema does not state. It rejects null values,
// and required names that have no property.
func checkSchemaNode(node any, path string) error {
	switch v := node.(type) {
	case nil:
		return fmt.Errorf("%s is null", path)
	case []any:
		for i, item := range v {
			if err := checkSchemaNode(item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case map[string]any:
		if required, ok := v["required"].([]any); ok {
			properties, _ := v["properties"].(map[string]any)
			for _, name := range required {
				if _, ok := properties[name.(string)]; !ok {
					return fmt.Errorf("%s.required has %q, which is not in properties", path, name)
				}
			}
		}
		for key, value := range v {
			if err := checkSchemaNode(value, path+"."+key); err != nil {
				return err
			}
		}
	}
	return nil
}

func TestCheckToolSchemaRejectsInvalidSchemas(t *testing.T) {
	for name, schema := range map[string]map[string]any{
		"required null":    {"type": "object", "properties": map[string]any{}, "required": nil},
		"unknown type":     {"type": "object", "properties": map[string]any{"a": map[string]any{"type": "strin"}}},
		"enum not array":   {"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string", "enum": "x"}}},
		"unknown required": {"type": "object", "properties": map[string]any{}, "required": []string{"a"}},
		"root not object":  {"type": "string"},
		"root oneOf":       {"type": "object", "oneOf": []any{map[string]any{"type": "object"}}},
		"nested null":      {"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string", "description": nil}}},
	} {
		if err := checkToolSchema(schema); err == nil {
			t.Errorf("%s: the check accepted an invalid schema", name)
		} else {
			t.Logf("%s: %v", name, strings.SplitN(err.Error(), "\n", 2)[0])
		}
	}
}
