package app_test

import (
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/app"
)

// The values below are fake. The first is the real mistake from production: a whole line
// pasted as the value of one key.
const (
	pastedValue  = "ANTHROPIC_BASE_URL=https://proxy.example CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-fake"
	base64Value  = "dGVzdA=="
	fakeFragment = "sk-ant-oat01-fake"
)

type envKeyHints struct {
	Env []struct {
		Key      string   `json:"key"`
		Scope    string   `json:"scope"`
		Shadowed bool     `json:"shadowed"`
		Hints    []string `json:"hints"`
	} `json:"env"`
}

func (e envKeyHints) agentHints(key string) []string {
	for _, entry := range e.Env {
		if entry.Scope == "agent" && entry.Key == key {
			return entry.Hints
		}
	}
	return nil
}

func TestUserSeesShapeHintsForAPastedAssignmentWithoutValues(t *testing.T) {
	t.Parallel()
	// Given a bot .env with the pasted line, a base64 value, an empty value, a padded value,
	// and a 1Password reference.
	data := t.TempDir()
	path := filepath.Join(data, "agents", "default", ".agents", ".env")
	writeFile(t, path, strings.Join([]string{
		"CLAUDE_CODE_OAUTH_TOKEN=" + pastedValue,
		"GOOD_BASE64_KEY=" + base64Value,
		"EMPTY_KEY=",
		"PADDED_KEY=' padded '",
		"REFERENCE_KEY=op://vault/item/field",
		"",
	}, "\n"))
	d := startDexHome(t, app.Config{DataDir: data}, t.TempDir())
	d.setModel(t, "section")
	// When the user reads the extensions API.
	status, body := d.call(t, http.MethodGet, "/api/agents/default/extensions", nil)
	if status != http.StatusOK {
		t.Fatalf("extensions: %d", status)
	}
	var hints envKeyHints
	d.must(t, http.MethodGet, "/api/agents/default/extensions", nil, &hints)
	// Then each shape has its hint, and a base64 padding is not an assignment.
	for key, want := range map[string]string{
		"CLAUDE_CODE_OAUTH_TOKEN": "assignment",
		"EMPTY_KEY":               "empty",
		"PADDED_KEY":              "whitespace",
		"REFERENCE_KEY":           "op-reference",
	} {
		if got := hints.agentHints(key); !slices.Equal(got, []string{want}) {
			t.Fatalf("hints of %s = %v, want [%s]", key, got, want)
		}
	}
	if got := hints.agentHints("GOOD_BASE64_KEY"); len(got) != 0 {
		t.Fatalf("base64 value got hints %v", got)
	}
	// And the system prompt names the key and the problem, never a value.
	w := d.watch(t, d.newChat(t, "extension-guide"))
	w.until(t, "extension guide", answeredWith("section: "))
	guide := w.tr.items[len(w.tr.items)-1].Text
	for _, text := range []string{"CLAUDE_CODE_OAUTH_TOKEN: the value looks like an assignment", "EMPTY_KEY: the value is empty", "PADDED_KEY: the value has leading or trailing whitespace",
		"only in tools that dex starts", "env KEY=\"$KEY\""} {
		if !strings.Contains(guide, text) {
			t.Fatalf("guide missing %q", text)
		}
	}
	// A key with no hint, or with only the op:// hint, is not a problem for the prompt.
	for _, key := range []string{"GOOD_BASE64_KEY", "REFERENCE_KEY"} {
		if strings.Contains(guide, key) {
			t.Fatalf("guide lists %s, which has no problem hint", key)
		}
	}
	for name, text := range map[string]string{"api": body, "guide": guide} {
		for _, secret := range []string{fakeFragment, "proxy.example", "dGVzdA", "padded", "vault/item"} {
			if strings.Contains(text, secret) {
				t.Fatalf("%s disclosed %q", name, secret)
			}
		}
	}
	t.Log("artifact: hints for assignment, empty, whitespace and op-reference; none for base64; prompt lists key names and hints only")
}
