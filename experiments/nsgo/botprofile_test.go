//go:build !js

package main

import (
	"os"
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func requireBotProfileGateway(t *testing.T) {
	t.Helper()
	if os.Getenv("DEX_NSGO_POC_TEST") != "1" || os.Getenv("DEX_NSGO_TEST_URL") != "http://127.0.0.1:19545" {
		t.Skip("requires the disposable Dex on port 19545")
	}
}

func TestUserFindsProfileAndInstructionsInGeneral(t *testing.T) {
	t.Parallel()
	requireBotProfileGateway(t)
	// Given a Bot in the disposable workspace, when the user opens its settings.
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		return botSettingsPage(cx, "default", func(string) {})
	})
	// Then General shows the Profile rows before the separate Instructions editor.
	const timeout = 6 * time.Second
	app.WaitFor("text:General", timeout)
	app.WaitFor("text:Profile", timeout)
	app.WaitFor("id:bot-settings-name", timeout)
	app.WaitFor("text:Default Runner", timeout)
	app.WaitFor("text:Instructions", timeout)
	app.WaitFor("id:bot-settings-instructions", timeout)
}

func TestUserOpensBotSettingsDeepLinksAndSwitchesPages(t *testing.T) {
	t.Parallel()
	requireBotProfileGateway(t)
	// Given a link to the MCP section, the user lands on Integrations.
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		tab := core.NewSignal(cx, "integrations?section=mcp")
		return core.Dynamic(func(cx *core.Scope) core.Node {
			current := tab.Get()
			return core.Untracked(cx, func() core.Node {
				return botProfileScreen(cx, "default", current, tab.Set, nil)
			})
		})
	})
	const timeout = 6 * time.Second
	app.WaitFor("id:tools-reload", timeout)
	app.WaitForGone("id:bot-settings-name", timeout)

	// When the user opens a channel or switches to another settings page,
	// only that page's controls are present.
	app.Press("id:bot-settings-tab-channels")
	app.WaitFor("id:tools-notification-primary", timeout)
	app.Press("id:tools-channel-telegram")
	app.WaitFor("text:Connection", timeout)
	app.WaitForGone("id:tools-notification-primary", timeout)
	app.Press("id:bot-settings-tab-secrets")
	app.WaitFor("id:tools-vault-value", timeout)
	app.WaitForGone("id:tools-channel-telegram", timeout)
	app.Press("id:bot-settings-tab-bot")
	app.WaitFor("id:bot-settings-name", timeout)
	app.WaitForGone("id:tools-vault-value", timeout)
}
