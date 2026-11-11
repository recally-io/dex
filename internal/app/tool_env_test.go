package app_test

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type apiExtensionsPending struct {
	ReloadPending bool `json:"reloadPending"`
}

// applied waits until dex has reloaded the default agent after a change.
func (d *dex) applied(t *testing.T) {
	t.Helper()
	eventually(t, "automatic reload", func() (bool, any) {
		var got apiExtensionsPending
		d.must(t, http.MethodGet, "/api/agents/default/extensions", nil, &got)
		return !got.ReloadPending, got
	})
}

func TestUserEnvironmentChangeAppliesWithoutReload(t *testing.T) {
	t.Parallel()
	// Given an agent that has loaded its environment.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "bash")
	command := `printf '%s' "${DEX_AUTO-unset}"`
	w := d.watch(t, d.newChat(t, command))
	w.until(t, "first answer", answered("bash: unset"))
	// When the user saves a key and does not press Reload.
	d.must(t, http.MethodPut, "/api/agents/default/extensions/env", map[string]string{"scope": "agent", "key": "DEX_AUTO", "value": "applied"}, nil)
	// Then dex reloads the idle agent, and the next command sees the value.
	d.applied(t)
	w = d.watch(t, d.newChat(t, command))
	w.until(t, "new value", answered("bash: applied"))
	t.Log("artifact: a saved key reaches tools without a manual reload")
}

func TestUserSetsUpOnePasswordForTools(t *testing.T) {
	t.Parallel()
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "bash")
	w := d.watch(t, d.newChat(t, "true"))
	w.until(t, "agent open", answered("bash: "))
	// When the user sends incomplete settings, then dex refuses them.
	for _, input := range []map[string]string{
		{"mode": "service-account"},
		{"mode": "connect", "connectHost": "not a url", "connectToken": "private-op-token"},
		{"mode": "unknown"},
	} {
		status, body := d.call(t, http.MethodPut, "/api/settings/onepassword", input)
		if status != http.StatusBadRequest || strings.Contains(body, "private-op-token") {
			t.Fatalf("invalid input: %d %s", status, body)
		}
	}
	// When the user selects a service account with a token.
	const token = "ops_private-op-token"
	var saved map[string]any
	d.must(t, http.MethodPut, "/api/settings/onepassword", map[string]string{"mode": "service-account", "serviceAccountToken": token}, &saved)
	_, body := d.call(t, http.MethodGet, "/api/settings/onepassword", nil)
	if saved["mode"] != "service-account" || saved["serviceAccountTokenSet"] != true || strings.Contains(body, token) {
		t.Fatalf("settings: %v %s", saved, body)
	}
	// Then the open agent reloads, and tools get the token.
	d.applied(t)
	check := fmt.Sprintf(`test "$(printf %%s "$OP_SERVICE_ACCOUNT_TOKEN" | shasum -a 256 | cut -d " " -f 1)" = %x && printf ready`, sha256.Sum256([]byte(token)))
	w = d.watch(t, d.newChat(t, check))
	w.until(t, "token in tools", answered("bash: ready"))
	// When the user turns 1Password off, then the saved token stays for later.
	d.must(t, http.MethodPut, "/api/settings/onepassword", map[string]string{"mode": "off"}, &saved)
	if saved["mode"] != "off" || saved["serviceAccountTokenSet"] != true {
		t.Fatalf("off: %v", saved)
	}
	t.Log("artifact: 1Password credentials reach tools after an automatic reload; responses hold no token")
}
