package app_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/app"
)

func TestUserAgentsMDReachesTheModelAroundTheAgentInstructions(t *testing.T) {
	t.Parallel()
	// Given a home AGENTS.md that one agent opts into with a symlink, a dex AGENTS.md, a project
	// AGENTS.md, and instructions stored for the agent. The home level also holds skills, mcp.json, and .env.
	data, home, project := t.TempDir(), t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(home, ".agents", "AGENTS.md"), "HOME-RULES\n@rules/style.md\n")
	writeFile(t, filepath.Join(home, ".agents", "rules", "style.md"), "STYLE-RULES\n")
	writeSkill(t, filepath.Join(home, ".agents", "skills"), "home-skill", "description: from home")
	writeMCP(t, filepath.Join(home, ".agents", "mcp.json"), map[string]any{"home-server": stdioServer(t, t.TempDir())})
	writeFile(t, filepath.Join(home, ".agents", ".env"), "HOME_ONLY=1\n")
	writeFile(t, filepath.Join(data, ".agents", "AGENTS.md"), "DEX-RULES\n")
	agentDir := filepath.Join(data, "agents", "default", ".agents")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, ".agents", "AGENTS.md"), filepath.Join(agentDir, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(project, "AGENTS.md"), "PROJECT-RULES\n")
	d := startDexHome(t, app.Config{DataDir: data}, home)
	d.must(t, http.MethodPut, "/api/agents/default",
		map[string]string{"name": "Dex", "provider": "faux", "modelId": "section", "thinkingLevel": "off", "instructions": "OWN-INSTRUCTIONS"}, nil)
	// When a chat works in the project directory and the model reads its instructions.
	chat := d.newSession(t)
	d.must(t, http.MethodPut, projectURL(chat.ID), map[string]string{"cwd": project}, nil)
	w := d.watch(t, chat.ID)
	d.send(t, chat.ID, "instructions")
	w.until(t, "instructions", answeredWith("PROJECT-RULES"))
	answer := w.tr.items[len(w.tr.items)-1].Text
	// Then the order is dex, agent (a symlink to the home file, with its import), own, project.
	last := -1
	for _, part := range []string{"DEX-RULES", "HOME-RULES", "STYLE-RULES", "OWN-INSTRUCTIONS", "PROJECT-RULES"} {
		at := strings.Index(answer, part)
		if at <= last {
			t.Fatalf("%s is missing or out of order in %q", part, answer)
		}
		last = at
	}
	// And nothing else from home reaches the agent: no skill, no server, no key.
	got := d.extensions(t)
	if len(got.Skills) != 0 || len(got.Servers) != 0 || len(got.Env) != 0 || got.dir("home") != "" {
		t.Fatalf("home files are read: %+v", got)
	}
	// And an edit applies to the next chat without a restart.
	writeFile(t, filepath.Join(home, ".agents", "rules", "style.md"), "NEW-STYLE\n")
	w = d.watch(t, d.newChat(t, "instructions"))
	w.until(t, "edited import", answeredWith("NEW-STYLE"))
}

func TestUserHomeAgentsMDIsNotReadWithoutALink(t *testing.T) {
	t.Parallel()
	// Given a home AGENTS.md and no AGENTS.md at the dex or agent level.
	data, home := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(home, ".agents", "AGENTS.md"), "HOME-RULES\n")
	d := startDexHome(t, app.Config{DataDir: data}, home)
	d.must(t, http.MethodPut, "/api/agents/default",
		map[string]string{"name": "Dex", "provider": "faux", "modelId": "section", "thinkingLevel": "off", "instructions": "OWN-INSTRUCTIONS"}, nil)
	// When the model reads its instructions, only the agent's own text is there.
	w := d.watch(t, d.newChat(t, "instructions"))
	w.until(t, "instructions", answeredWith("section: "))
	if answer := w.tr.items[len(w.tr.items)-1].Text; !strings.Contains(answer, "OWN-INSTRUCTIONS") || strings.Contains(answer, "HOME-RULES") {
		t.Fatalf("instructions: %q", answer)
	}
}
