package app_test

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"uuid"
)

type apiVaultEntry struct {
	Name      string `json:"name"`
	Scope     string `json:"scope"`
	UpdatedAt string `json:"updatedAt"`
}

func saveVault(t *testing.T, d *dex, agentID, scope, name, value string) {
	t.Helper()
	d.must(t, http.MethodPut, "/api/agents/"+agentID+"/vault", map[string]string{"scope": scope, "name": name, "value": value}, nil)
}

func TestUserManagesWriteOnlyVaultAtBothLevels(t *testing.T) {
	t.Parallel()
	d := startDex(t, t.TempDir(), "")
	var other apiAgent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "Other bot", "provider": "faux", "modelId": "tool", "thinkingLevel": "off"}, &other, http.StatusCreated)
	d.setModel(t, "tool")
	const wide, narrow = "shared-test-secret", "agent-test-secret"
	const endpoint = "/api/agents/default/vault"
	for _, bad := range []any{map[string]any{"scope": "agent", "name": "TOKEN", "value": []string{narrow}}, map[string]any{"scope": "agent", "name": "TOKEN", "value": narrow + "\x00"}} {
		status, body := d.call(t, http.MethodPut, endpoint, bad)
		if status != http.StatusBadRequest || strings.Contains(body, narrow) {
			t.Fatalf("invalid write: %d %s", status, body)
		}
	}
	saveVault(t, d, "default", "dex", "TOKEN", wide)
	saveVault(t, d, "default", "agent", "TOKEN", narrow)
	status, body := d.call(t, http.MethodGet, endpoint, nil)
	var entries []apiVaultEntry
	if status != http.StatusOK || json.Unmarshal([]byte(body), &entries) != nil || len(entries) != 2 || entries[0].Scope != "dex" || entries[1].Scope != "agent" || entries[1].Name != "TOKEN" || entries[1].UpdatedAt == "" || strings.Contains(body, wide) || strings.Contains(body, narrow) {
		t.Fatalf("vault metadata: %d %s", status, body)
	}
	call := func(agentID string) string {
		var session sessionListItem
		base := "/api/agents/" + agentID + "/sessions"
		d.must(t, http.MethodGet, base+"/main", nil, &session)
		path := fmt.Sprintf("%s/%d", base, session.ID)
		w := d.watchPath(t, path+"/events")
		d.must(t, http.MethodPost, path+"/messages", map[string]string{"id": uuid.NewV7().String(), "text": `bash {"command":"case $TOKEN in agent-*) printf agent;; shared-*) printf shared;; esac","env":{"TOKEN":"vault:TOKEN"}}`}, nil, http.StatusAccepted)
		w.until(t, "bot secret", answeredWith("tool: "))
		return w.tr.items[len(w.tr.items)-1].Text
	}
	if got := call("default"); got != "tool: agent" {
		t.Fatalf("default: %q", got)
	}
	if got := call(other.ID); got != "tool: shared" {
		t.Fatalf("other bot: %q", got)
	}
	d.must(t, http.MethodDelete, endpoint+"?scope=agent&name=TOKEN", nil, nil)
	d.applied(t)
	// The next call uses the shared value again.
	w := d.watch(t, d.newSessionWithMessage(t, `bash {"command":"case $TOKEN in shared-*) printf shared;; esac","env":{"TOKEN":"vault:TOKEN"}}`))
	w.until(t, "shared fallback", answered("tool: shared"))
}
