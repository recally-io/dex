package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/durable"
)

func TestObjectSchemaWithoutRequiredEncodesEmptyArray(t *testing.T) {
	data, err := json.Marshal(objectSchema(nil, map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"required":[]`) {
		t.Fatalf("required must be an empty array: %s", data)
	}
}

// Providers reject the whole request when one tool schema is invalid, so check every built-in tool.
func TestBuiltInToolSchemasEncodeValidJSONSchema(t *testing.T) {
	a := &Agents{}
	ref := Ref{AgentID: "bot"}
	link := WorkTaskLinkTool{Name: "task_link_test", Properties: map[string]any{"n": map[string]any{"type": "integer"}}}
	extensions := []*durable.Extension{
		checkExtension("bot", nil), telegramExtension("bot", nil), codingTools(nil, 0), routineExtension("bot", RoutineTools{}),
		sessionExtension("bot", TaskTools{}), sessionReadExtension("bot", TaskTools{}), backgroundExtension("bot", BackgroundTools{}, nil),
		workLoopExtension(), artifactExtension("bot", nil, nil), (&memoryRepo{}).Extension("bot", nil), frontDeskExtension(),
		outboxExtension("bot", OutboxTools{}), childOutboxExtension("bot", OutboxTools{}), monitorExtension("bot", MonitorTools{}),
		askUserExtension("bot", nil), botReadExtension("bot", BotTools{}), botManageExtension("bot", BotTools{}),
		workTaskExtension("bot", WorkTaskTools{Links: []WorkTaskLinkTool{link}}), childExtension(), dreamMessageExtension("bot", nil),
		routineCheckInstructions(), groupExtension(nil),
		{Name: "group-tools", Tools: []*durable.ToolRegistration{a.groupQuestionTool(ref), a.groupSessionTool(ref), a.groupDeliveryTool(ref, nil)}},
	}
	count := 0
	for _, extension := range extensions {
		for _, tool := range extension.Tools {
			count++
			data, err := json.Marshal(tool.Parameters)
			if err != nil {
				t.Errorf("%s: %v", tool.Name, err)
				continue
			}
			var schema any
			if err := json.Unmarshal(data, &schema); err != nil {
				t.Fatal(err)
			}
			if err := checkSchema(schema, tool.Name); err != nil {
				t.Errorf("%v\n%s", err, data)
			}
		}
	}
	if count == 0 {
		t.Fatal("no tools were checked")
	}
	t.Logf("checked %d tool schemas", count)
}

// checkSchema walks the encoded schema. It rejects null values and keywords with the wrong JSON type.
func checkSchema(node any, path string) error {
	switch v := node.(type) {
	case nil:
		return fmt.Errorf("%s is null", path)
	case []any:
		for i, item := range v {
			if err := checkSchema(item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case map[string]any:
		for key, value := range v {
			at := path + "." + key
			switch key {
			case "required", "enum":
				if _, ok := value.([]any); !ok {
					return fmt.Errorf("%s must be an array", at)
				}
			case "properties":
				if _, ok := value.(map[string]any); !ok {
					return fmt.Errorf("%s must be an object", at)
				}
			}
			if err := checkSchema(value, at); err != nil {
				return err
			}
		}
	}
	return nil
}
