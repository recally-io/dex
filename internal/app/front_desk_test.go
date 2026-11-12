package app_test

import (
	"strings"
	"testing"
)

func TestUserGetsFrontDeskRulesInMainAndWorkLoopRulesInChat(t *testing.T) {
	t.Parallel()
	// Given a real app whose model answers with the system prompt section that the message names.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	d.setModel(t, "section")

	// When the user writes in main, the agent acts as a front desk that delegates long work.
	mainID := d.mainChat(t)
	w := d.watch(t, mainID)
	d.send(t, mainID, "front-desk")
	w.until(t, "front desk rules in main", answeredWith("section: You are the front desk"))
	d.send(t, mainID, "work-loop")
	// Then main has no rule that tells it to do long work itself.
	w.until(t, "no work loop rules in main", answered("section: "))

	// Coding guidance must not tell main to do the work that its front desk delegates.
	d.send(t, mainID, "coding-skill")
	w.until(t, "coding skill respects the front desk", answeredWith("section: ---\nname: coding"))
	w.until(t, "coding skill delegates main work", answeredWith("When the front desk handles a request"))
	if answer := w.tr.items[len(w.tr.items)-1].Text; strings.Contains(answer, "The main agent implements changes") {
		t.Fatalf("coding skill contradicts the front desk: %s", answer)
	}

	// When the user writes in a Chat session, the agent keeps doing the work in its own loop.
	chatID := d.newChat(t, "work-loop")
	c := d.watch(t, chatID)
	c.until(t, "work loop rules in Chat", answeredWith("section: You are the main agent loop"))
	d.send(t, chatID, "front-desk")
	// Then the Chat session has no front desk rule.
	c.until(t, "no front desk rules in Chat", answered("section: "))

	// When a background task reads either rule, then it has neither, because it cannot delegate.
	for _, section := range []string{"work-loop", "front-desk"} {
		spawnExecution(t, d, chatID, "Read "+section, section, "section")
		if got := waitExecution(t, conn, chatID, "Read "+section, "done").Report; got != "section: " {
			t.Fatalf("background task has %s rules: %q", section, got)
		}
	}
}
