package app

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/engine"
)

func TestUserChildSubmitStripsForegroundTools(t *testing.T) {
	home := t.TempDir()
	a, err := Start(t.Context(), Config{DataDir: t.TempDir(), Home: home, FakeModel: true,
		ToolEnv: map[string]string{"PATH": os.Getenv("PATH"), "HOME": home}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Error(err)
		}
	})
	options := engine.Options{Model: engine.ModelRef{Provider: "faux", ModelID: "direct"}, ThinkingLevel: "off"}
	ref, err := a.engine.Create(t.Context(), "default", options)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"session_new", "ask_user", "automation_create"} {
		sub, err := a.engine.Submit(t.Context(), ref, engine.Input{
			RequestID: "child-" + tool,
			Content:   tool + " {}",
			Kind:      "message",
			Origin:    "task",
			Source:    "dex:default/task/1",
			Child:     true,
			Options:   options,
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		outcome, err := sub.Wait(ctx)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(outcome.Answer, "tool error:") {
			t.Fatalf("child tool %s was unexpectedly available: %+v", tool, outcome)
		}
	}
}
