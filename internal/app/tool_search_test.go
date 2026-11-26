package app_test

import (
	"strings"
	"testing"
)

func TestUserFindsBuiltInToolsWithoutMCP(t *testing.T) {
	t.Parallel()
	// Given a bot with no MCP servers.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "tool")

	// When the model searches for a built-in tool.
	watch := d.watch(t, d.newSessionWithMessage(t, `tool_search {"query":"memory_search"}`))

	// Then it finds that tool without exposing retired tools.
	watch.until(t, "built-in tool search", answeredWith("memory_search:"))
	for _, name := range []string{"telegram_message", "stay_silent"} {
		watch := d.watch(t, d.newSessionWithMessage(t, `tool_search {"query":"`+name+`"}`))
		watch.until(t, "tool search completed", answeredWith("tool: "))
		if got := watch.tr.items[len(watch.tr.items)-1].Text; strings.Contains(got, "- "+name+":") {
			t.Fatalf("search found a retired tool: %s", got)
		}
	}
}
