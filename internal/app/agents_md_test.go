package app_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserKeepsLegacyBotInstructionsAfterUpgrade(t *testing.T) {
	t.Parallel()
	// Given two bots, with shared rules and a symlink to private rules for one bot.
	data, home, project := t.TempDir(), t.TempDir(), t.TempDir()
	cfg := app.Config{DataDir: data}
	conn, err := db.Open(t.Context(), filepath.Join(data, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)
	for _, id := range []string{"default", "other"} {
		if err := q.CreateAgentIfMissing(t.Context(), sqlc.CreateAgentIfMissingParams{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.UpdateAgentSettings(t.Context(), sqlc.UpdateAgentSettingsParams{
		ID: "default", Name: "Dex", ModelProvider: "faux", ModelID: "section", ThinkingLevel: "off", Instructions: "OWN-RULES",
	}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	dexFile := filepath.Join(data, ".agents", "AGENTS.md")
	agentFile := filepath.Join(data, "agents", "default", ".agents", "AGENTS.md")
	homeFile := filepath.Join(home, ".agents", "AGENTS.md")
	writeFile(t, dexFile, "DEX-RULES\n")
	writeFile(t, homeFile, "HOME-RULES\n@rules/style.md\n")
	writeFile(t, filepath.Join(home, ".agents", "rules", "style.md"), "STYLE-RULES\n")
	if err := os.MkdirAll(filepath.Dir(agentFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(homeFile, agentFile); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(project, "AGENTS.md"), "PROJECT-RULES\n@extra.md\n")
	writeFile(t, filepath.Join(project, "extra.md"), "PROJECT-IMPORT\n")

	// When dex starts, then the rules are stored on the intended bots and reach the next turn.
	d := startDexHome(t, cfg, home)
	instructions := func(d *dex, id string) string {
		t.Helper()
		var bot struct {
			Instructions string `json:"instructions"`
		}
		d.must(t, http.MethodGet, "/api/agents/"+id, nil, &bot)
		return bot.Instructions
	}
	saved := instructions(d, "default")
	for _, part := range []string{"OWN-RULES", "DEX-RULES", "HOME-RULES", "STYLE-RULES", dexFile, agentFile} {
		if !strings.Contains(saved, part) {
			t.Fatalf("default bot is missing %q: %q", part, saved)
		}
	}
	if got := instructions(d, "other"); !strings.Contains(got, "DEX-RULES") || strings.Contains(got, "HOME-RULES") {
		t.Fatalf("other bot instructions: %q", got)
	}
	assertPrompt := func(d *dex) {
		t.Helper()
		chat := d.newSession(t)
		d.must(t, http.MethodPut, projectURL(chat.ID), map[string]string{"cwd": project}, nil)
		w := d.watch(t, chat.ID)
		d.send(t, chat.ID, "instructions")
		w.until(t, "instructions", answeredWith("PROJECT-IMPORT"))
		answer := w.tr.items[len(w.tr.items)-1].Text
		if !strings.Contains(answer, saved) || strings.Contains(answer, "NEW-LEGACY-RULES") {
			t.Fatalf("prompt instructions: %q", answer)
		}
		guide := d.watch(t, d.newChat(t, "extension-guide"))
		guide.until(t, "extension guide", answeredWith("Extension levels"))
		if answer := guide.tr.items[len(guide.tr.items)-1].Text; strings.Contains(answer, ".agents/AGENTS.md") {
			t.Fatalf("extension guide names a legacy file: %q", answer)
		}
	}
	assertPrompt(d)
	d.stop()

	// Then dex keeps both legacy files as backups and ignores replacement files after restart.
	for _, legacy := range []string{dexFile, agentFile} {
		if _, err := os.ReadFile(legacy + ".migrated"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(legacy); !os.IsNotExist(err) {
			t.Fatalf("legacy file was not moved: %s: %v", legacy, err)
		}
		writeFile(t, legacy, "NEW-LEGACY-RULES\n")
	}
	d = startDexHome(t, cfg, home)
	if got := instructions(d, "default"); got != saved {
		t.Fatalf("restart changed instructions: %q, want %q", got, saved)
	}
	assertPrompt(d)

	// A person can edit the migrated instructions through bot_update.
	d.must(t, http.MethodPut, "/api/agents/default", map[string]string{
		"name": "Dex", "provider": "faux", "modelId": "tool", "thinkingLevel": "off", "instructions": saved,
	}, nil)
	edited := strings.Replace(saved, "HOME-RULES", "EDITED-RULES", 1)
	taskTool(t, d, d.newSession(t).ID, "bot_update", map[string]string{"bot": "default", "instructions": edited})
	if got := instructions(d, "default"); got != edited {
		t.Fatalf("bot_update instructions: %q, want %q", got, edited)
	}
}
