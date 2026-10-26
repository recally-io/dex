package app_test

import (
	"os"
	"testing"
)

func TestUserCanReadBuiltInCodingSkillInChatAndBackgroundExecution(t *testing.T) {
	t.Parallel()
	// Given a real app with no user-installed skills and the native runtime.
	body, err := os.ReadFile("../engine/skills/coding/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	want := "section: " + string(body)
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	d.setModel(t, "section")

	// When the main chat reads the built-in section, it gets the full skill.
	chatID := d.newChat(t, "coding-skill")
	d.watch(t, chatID).until(t, "full coding skill in main chat", answered(want))

	// Then a new background execution receives the same full skill in its model context.
	spawnExecution(t, d, chatID, "Read built-in coding skill", "coding-skill", "section")
	task := waitExecution(t, conn, chatID, "Read built-in coding skill", "done")
	saved := task
	if got := saved.Report; got != want {
		t.Fatalf("background execution did not receive the full coding skill:\ngot: %q\nwant: %q", got, want)
	}
	t.Logf("coding-skill: main chat and saved execution %s report contain all %d bytes", saved.ID, len(body))
}
