package app_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/app"
)

// builtinCodingMarker is text of the embedded coding skill, not of a user skill.
const builtinCodingMarker = "Use this skill only for coding work"

func TestUserCodingSkillReplacesTheBuiltInOne(t *testing.T) {
	t.Parallel()
	// A dex-level or agent-level coding skill replaces the built-in section.
	// A project-level one does not, and with none the built-in section stays.
	cases := []struct {
		name, root  string
		wantBuiltIn bool
	}{
		{"none", "", true},
		{"dex", "dex", false},
		{"agent", "agent", false},
		{"project", "project", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			roots := map[string]string{
				"dex":     filepath.Join(data, ".agents", "skills"),
				"agent":   filepath.Join(data, "agents", app.DefaultAgentID, ".agents", "skills"),
				"project": filepath.Join(data, "agents", app.DefaultAgentID, "workspace", ".agents", "skills"),
			}
			if tc.root != "" {
				writeSkill(t, roots[tc.root], "coding", "name: coding\ndescription: User coding skill")
			}
			d := startDex(t, data, "")
			d.setModel(t, "section")
			section := func(key string) string {
				w := d.watch(t, d.newChat(t, key))
				w.until(t, key, answeredWith("section: "))
				return strings.TrimPrefix(w.tr.items[len(w.tr.items)-1].Text, "section: ")
			}
			if got := strings.Contains(section("coding-skill"), builtinCodingMarker); got != tc.wantBuiltIn {
				t.Fatalf("built-in coding section present = %v, want %v", got, tc.wantBuiltIn)
			}
			listed := strings.Contains(section("skills"), "- coding: User coding skill (")
			if listed != (tc.root != "") {
				t.Fatalf("user coding skill listed = %v, want %v", listed, tc.root != "")
			}
		})
	}
}

func TestUserChildRulesAllowSupervisedProcesses(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	d := startDex(t, data, "")
	conn := taskResultDB(t, data)
	parent := newParent(t, d)
	var ref childRef
	d.setModel(t, "spawn")
	toolJSON(t, callSessionTool(t, d, parent, "session_new", map[string]any{"title": "Rules", "message": "child-rules", "provider": "faux", "modelId": "section"}), &ref)
	rows := waitResults(t, conn, parent, 1)
	got := resultBody(t, rows[0].content)["answer"].(string)
	if strings.Contains(got, "Do not detach") || !strings.Contains(got, "A process that you start and supervise in the same turn") || !strings.Contains(got, "Rex tab") {
		t.Fatalf("child rules: %s", got)
	}
}
