package main

import "testing"

func TestUserReopensWorkspacePagesFromTheirAddress(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		address, page, key string
		botScoped          bool
	}{
		{"/settings/providers", "global-settings", "providers", false},
		{"/settings/secrets", "global-settings", "secrets", false},
		{"/settings/integrations", "global-settings", "integrations", false},
		{"/settings/archived", "global-settings", "archived", false},
		{"/integrations?add=mcp&target=design", "integrations", "", false},
		{"/board?bot=design", "board", "", false},
		{"/board/results/run-7", "board-result", "run-7", false},
		{"/b/design/channels/telegram", "channels", "telegram", true},
		{"/b/design/secrets", "secrets", "", true},
		{"/b/design/integrations?section=skills", "tools", "", true},
		{"/b/design/automations/nightly", "automation", "nightly", true},
	} {
		// A bookmark must open the same page as its navigation control.
		got := workspaceLink(tc.address)
		if got.page != tc.page || got.key != tc.key || got.botScoped() != tc.botScoped || got.address() != tc.address {
			t.Errorf("%s opened %+v at %s", tc.address, got, got.address())
		}
	}
	for _, bad := range []string{"/settings/unknown", "/b/design/channels/unknown", "/b/design/s/0"} {
		if got := workspaceLink(bad); got.page != "missing" {
			t.Errorf("unexpected page for %q: %+v", bad, got)
		}
	}
}
