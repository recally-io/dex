package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/app"
)

type apiExtensionsPending struct {
	ReloadPending bool `json:"reloadPending"`
}

// applied waits until dex has reloaded the default agent after a change.
func (d *dex) applied(t *testing.T) {
	t.Helper()
	eventually(t, "automatic reload", func() (bool, any) {
		var got apiExtensionsPending
		d.must(t, http.MethodGet, "/api/agents/default/integrations", nil, &got)
		return !got.ReloadPending, got
	})
}

func TestUserInjectsVaultValueOnlyForOneCall(t *testing.T) {
	t.Parallel()
	// Given a value saved by the user.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "echo")
	w := d.watch(t, d.newChat(t, "hello"))
	w.until(t, "agent open", answered("echo: hello"))
	saveVault(t, d, "default", "agent", "GITHUB_TOKEN", "github-test-vault-value")
	d.applied(t)
	// When the model calls bash, then only the call with a reference gets the value.
	d.setModel(t, "tool")
	w = d.watch(t, d.newChat(t, `bash {"command":"printf %s \"${GITHUB_TOKEN-unset}/${GH_TOKEN-unset}\""}`))
	w.until(t, "no injection", answered("tool: unset/unset"))
	chat := d.newSession(t).ID
	assertVaultCommand(t, d, chat)
	w = d.watch(t, chat)
	w.until(t, "settled transcript", answered("tool: [vault:GITHUB_TOKEN]"))
	snapshot := taskJSON(t, w.tr.items)
	found := false
	for _, entry := range w.tr.items {
		if entry.Name == "bash" {
			env, _ := entry.Args["env"].(map[string]any)
			found = env["GH_TOKEN"] == "vault:GITHUB_TOKEN" && entry.Output == "[vault:GITHUB_TOKEN]"
		}
	}
	if !found || strings.Contains(snapshot, "github-test-vault-value") {
		t.Fatalf("transcript: %s", snapshot)
	}
	got := taskTool(t, d, chat, "bash", map[string]any{"command": "touch should-not-run", "env": map[string]string{"T": "vault:MISSING_SECRET"}})
	if !strings.Contains(got, "MISSING_SECRET") {
		t.Fatalf("missing vault error: %s", got)
	}
	if got := taskTool(t, d, chat, "bash", map[string]string{"command": "test ! -e should-not-run && printf absent"}); got != "tool: absent" {
		t.Fatalf("missing secret must prevent execution: %s", got)
	}
	got = taskTool(t, d, chat, "secret_list", map[string]any{})
	_, raw, ok := strings.Cut(got, "tool: ")
	var list []apiVaultEntry
	if !ok || json.Unmarshal([]byte(raw), &list) != nil || len(list) != 1 || list[0].Name != "GITHUB_TOKEN" || list[0].Scope != "agent" || list[0].UpdatedAt == "" || strings.Contains(got, "github-test-vault-value") {
		t.Fatalf("secret_list: %s", got)
	}
}

func assertVaultCommand(t *testing.T, d *dex, chat int64) {
	t.Helper()
	// Check the actual process arguments before printing the injected value.
	got := taskTool(t, d, chat, "bash", map[string]any{
		"command": `line=$(ps -o args= -p "$$") && test -n "$line" && test -n "$GH_TOKEN" || exit 22; case "$line" in *"$GH_TOKEN"*) exit 23;; esac; printf %s "$GH_TOKEN"`,
		"env":     map[string]string{"GH_TOKEN": "vault:GITHUB_TOKEN"},
	})
	if got != "tool: [vault:GITHUB_TOKEN]" {
		t.Fatalf("vault command %d: %q", chat, got)
	}
}

func TestUserShellEditWaitsForRestart(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	rc := filepath.Join(home, ".bashrc")
	writeFile(t, rc, "export DEX_TEST_RC=first\n")
	cfg := app.Config{DataDir: t.TempDir(), LoginEnv: func(ctx context.Context, base map[string]string) (map[string]string, error) {
		out, err := exec.CommandContext(ctx, "sh", "-c", `. "$1"; printf %s "$DEX_TEST_RC"`, "sh", rc).Output()
		base["DEX_TEST_RC"] = string(out)
		return base, err
	}}
	d := startDexHome(t, cfg, home)
	d.setModel(t, "bash")
	const command = `printf %s "$DEX_TEST_RC"`
	d.watch(t, d.newChat(t, command)).until(t, "initial shell value", answered("bash: first"))

	writeFile(t, rc, "export DEX_TEST_RC=after-restart\n")
	// The old scheduler could reload on its next 3-second tick.
	deadline := time.NewTimer(4 * time.Second)
	defer deadline.Stop()
	ticks := time.NewTicker(50 * time.Millisecond)
	defer ticks.Stop()
wait:
	for {
		var info apiExtensionsPending
		d.must(t, http.MethodGet, "/api/agents/default/integrations", nil, &info)
		if !info.ReloadPending {
			t.Fatal("dex reloaded after only a shell rc edit")
		}
		select {
		case <-deadline.C:
			break wait
		case <-ticks.C:
		}
	}
	d.stop()
	d = startDexHome(t, cfg, home)
	d.watch(t, d.newChat(t, command)).until(t, "new shell value after restart", answered("bash: after-restart"))
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
