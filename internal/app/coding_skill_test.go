package app_test

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/app"
)

func TestUserCodingSkillIsAnOrdinarySkill(t *testing.T) {
	t.Parallel()
	for _, level := range []string{"none", "dex", "agent"} {
		t.Run(level, func(t *testing.T) {
			t.Parallel()
			// Given a coding skill at the dex level, at the agent level, or none.
			data := t.TempDir()
			roots := map[string]string{
				"dex":   filepath.Join(data, ".agents", "skills"),
				"agent": filepath.Join(data, "agents", app.DefaultAgentID, ".agents", "skills"),
			}
			if level != "none" {
				writeSkill(t, roots[level], "coding", "name: coding\ndescription: User coding skill")
			}
			d := startDex(t, data, "")
			d.setModel(t, "section")
			section := func(key string) string {
				w := d.watch(t, d.newChat(t, key))
				w.until(t, key, answeredWith("section: "))
				return strings.TrimPrefix(w.tr.items[len(w.tr.items)-1].Text, "section: ")
			}
			// Then the skills section lists it like any other skill, and no built-in coding section exists.
			if listed := strings.Contains(section("skills"), "- coding: User coding skill ("); listed != (level != "none") {
				t.Fatalf("user coding skill listed = %v", listed)
			}
			if got := section("coding-skill"); got != "" {
				t.Fatalf("coding-skill section = %q, want none", got)
			}
		})
	}
}

func TestUserConversationWithRemovedCodingSkillSectionStillRuns(t *testing.T) {
	t.Parallel()
	// Given the data of an older dex version. Its chat 1 and its task 2 stored the
	// built-in coding skill as a section of their system prompt.
	dir := legacyData(t)
	d := startDex(t, dir, "")
	for _, id := range []int64{1, 2} {
		d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/default/chats/%d/settings", id), map[string]string{"provider": "faux", "modelId": "section", "thinkingLevel": "off"}, nil)
		// When the user sends a message to the old conversation.
		w := d.watch(t, id)
		d.send(t, id, "coding-skill")
		// Then it answers, and the model sees no coding skill section.
		w.until(t, fmt.Sprintf("chat %d", id), answeredWith("section:"))
		got := w.tr.items[len(w.tr.items)-1].Text
		if got != "section: " {
			t.Fatalf("chat %d: model saw the removed section: %q", id, got)
		}
	}
}
