package engine

import (
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/durable"
)

func TestSessionRulesTellMainWhatTheUserSees(t *testing.T) {
	// Given the session extension that Main gets.
	ext := sessionExtension("a", TaskTools{})
	rules := ext.Sections[0]
	text, err := rules.Render(t.Context(), durable.PromptInput{})
	if err != nil || text == nil {
		t.Fatalf("render = %v, %v", text, err)
	}
	// Then the rules say where the answer shows and who still depends on Main.
	for _, want := range []string{"open the child session as a thread", "see only your reply", "Do not copy the whole answer", "End every result turn with a short text reply"} {
		if !strings.Contains(*text, want) {
			t.Errorf("rules lack %q", want)
		}
	}
	if strings.Contains(*text, "cannot see this input") {
		t.Error("rules still say the user cannot see the result")
	}
}
