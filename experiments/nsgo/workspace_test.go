package main

import (
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestUserMovesBetweenBoardAndBots(t *testing.T) {
	chatRequireTestGateway(t)
	const timeout = 15 * time.Second
	// Given the default bot's main conversation, with global and bot navigation apart.
	app := nsgotest.Start(t, Root, nsgotest.Size(1180, 720))
	app.WaitFor("id:page-main", timeout)
	openSidebar := func() {
		t.Helper()
		if app.Exists("id:workspace-menu") {
			app.Press("id:workspace-menu")
		}
	}
	openSidebar()
	app.WaitFor("id:nav-bot-default", timeout)
	// When the owner opens the global Board, then selects Dex from the same sidebar.
	app.Press("id:nav-board")
	app.WaitFor("id:page-board", timeout)
	openSidebar()
	app.Press("id:nav-bot-default")
	// Then the main conversation is back, without a detour through a bot directory.
	app.WaitFor("id:page-main", timeout)
}
