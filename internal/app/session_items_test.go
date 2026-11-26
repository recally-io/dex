package app_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestUserOpensLongToolOutputOnRequest(t *testing.T) {
	t.Parallel()
	// Given a session where a real bash call printed 3000 bytes.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "bash")
	session := d.newSessionWithMessage(t, `head -c 3000 /dev/zero | tr '\0' x`)
	w := d.watch(t, session)
	w.until(t, "bash answer", answeredWith("bash: xxx"))
	// When the user opens the session again.
	fresh := d.watch(t, session)
	fresh.until(t, "snapshot", func(tr *transcript) bool { return tr.snapshots > 0 })
	// Then the stream and the snapshot carry the call without its output.
	var tool item
	for name, tr := range map[string]transcript{"stream": w.tr, "snapshot": fresh.tr} {
		tool = item{}
		for _, it := range tr.items {
			if it.Kind == "tool" {
				tool = it
			}
		}
		if tool.Status != "done" || tool.Output != "" || !tool.Truncated || tool.EntryID == 0 {
			t.Fatalf("%s tool = %+v", name, tool)
		}
	}
	// When the user opens the step, the item request returns the whole output.
	path := fmt.Sprintf("/api/agents/default/sessions/%d/items/%s?entry=%d", session, tool.ID, tool.EntryID)
	status, body := d.call(t, http.MethodGet, path, nil)
	var full item
	if status != http.StatusOK || json.Unmarshal([]byte(body), &full) != nil {
		t.Fatalf("item: %d %s", status, body)
	}
	if strings.TrimSpace(full.Output) != strings.Repeat("x", 3000) || full.Truncated || full.ID != tool.ID {
		t.Fatalf("item has %d bytes, truncated %v", len(full.Output), full.Truncated)
	}
	// And an unknown item is not found.
	status, body = d.call(t, http.MethodGet, fmt.Sprintf("/api/agents/default/sessions/%d/items/missing?entry=%d", session, tool.EntryID), nil)
	if status != http.StatusNotFound {
		t.Fatalf("missing item: %d %s", status, body)
	}
}
