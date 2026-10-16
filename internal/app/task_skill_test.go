package app_test

import (
	"os"
	"testing"
)

func TestUserCanReadBuiltInCodingSkillInChatAndTaskStage(t *testing.T) {
	// Given a real app with no user-installed skills and the embedded worker.
	body, err := os.ReadFile("../../worker/skills/coding/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	want := "section: " + string(body)
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "section")

	// When the main chat reads the built-in section, it gets the full skill.
	chatID := d.newChat(t, "coding-skill")
	d.watch(t, chatID).until(t, "full coding skill in main chat", answered(want))

	// Then a new background stage receives the same full skill in its model context.
	spawnUserTask(t, d, chatID, "Read built-in coding skill", "coding-skill", "section", "")
	task := waitUserTask(t, d, chatID, "Read built-in coding skill", "done", 1)
	saved := getUserTask(t, d, chatID, task.ID)
	if got := saved.Stages[0].Report; got != want {
		t.Fatalf("background stage did not receive the full coding skill:\ngot: %q\nwant: %q", got, want)
	}
	t.Logf("coding-skill: main chat and saved stage %s report contain all %d bytes", saved.Stages[0].ID, len(body))
}
