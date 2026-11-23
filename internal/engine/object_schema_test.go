package engine

import (
	"encoding/json"
	"strings"
	"testing"
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
