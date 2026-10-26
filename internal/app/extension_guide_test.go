package app_test

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/app"
)

func TestUserGetsExtensionGuideWithoutSkillsOrSecretNames(t *testing.T) {
	t.Parallel()
	// Given three environment files and no skills.
	data, home := t.TempDir(), t.TempDir()
	for i, root := range []string{home, data, filepath.Join(data, "agents", "default")} {
		writeFile(t, filepath.Join(root, ".agents", ".env"), fmt.Sprintf("PROMPT_PRIVATE_KEY_%d=private-prompt-value\n", i))
	}
	d := startDexHome(t, app.Config{DataDir: data}, home)
	d.setModel(t, "section")
	catalog := d.extensions(t)
	if len(catalog.Skills) != 0 {
		t.Fatal("fixture has skills")
	}
	// When the model reads the extension guide, the section exists without skills.
	chatID := d.newChat(t, "extension-guide")
	w := d.watch(t, chatID)
	w.until(t, "extension guide", answeredWith("section: "))
	guide := w.tr.items[len(w.tr.items)-1].Text
	// Then paths, file formats, secrecy, and reload rules are present, without key names or values.
	for _, text := range []string{catalog.dir("home"), catalog.dir("dex"), catalog.dir("agent"), ".env",
		"Narrower levels win", "as a whole", "skills/<name>/SKILL.md", "name", "description", "next request",
		"mcp.json", "mcpServers", "command", "args", "env", "cwd", `"type": "http"`, "url", "headers", `{"disabled": true}`,
		"$KEY", "${KEY}", "Never print", "chat", "memory", "files", "Secrets page", "Reload", "Extensions tab", "Ask the user"} {
		if text == "" || !strings.Contains(guide, text) {
			t.Fatalf("guide missing %q", text)
		}
	}
	if strings.Contains(guide, "PROMPT_PRIVATE_KEY_") || strings.Contains(guide, "private-prompt-value") {
		t.Fatal("guide disclosed a key name or value")
	}
	// The guide stays identical when environment names and values change, even after Reload.
	writeFile(t, filepath.Join(data, "agents", "default", ".agents", ".env"), "PROMPT_NEW_PRIVATE_KEY=private-new-prompt-value\n")
	w = d.watch(t, d.newChat(t, "extension-guide"))
	w.until(t, "stable guide before reload", answered(guide))
	status, _ := d.reload(t)
	if status != http.StatusOK {
		t.Fatalf("reload: %d", status)
	}
	w = d.watch(t, d.newChat(t, "extension-guide"))
	w.until(t, "stable guide after reload", answered(guide))
	t.Log("artifact: guide present without skills; three absolute paths; no key names or values; stable across environment edits and Reload")
}
