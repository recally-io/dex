package main

import (
	"os"
	"testing"
	"time"
	"uuid"

	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestMain(m *testing.M) { nsgotest.Main(m) }

func TestUserSendsChineseAndSeesReplyAfterReconnect(t *testing.T) {
	if os.Getenv("DEX_NSGO_POC_TEST") != "1" {
		t.Skip("set DEX_NSGO_POC_TEST=1 to use the isolated faux/echo gateway")
	}
	const timeout = 20 * time.Second
	probe := "中文探针 " + uuid.New().String()
	reply := "text:echo: " + probe

	// Given the default bot chat with an isolated faux/echo gateway.
	app := nsgotest.Start(t, Root)
	app.Find("text:未连接")
	app.Press("id:connect")
	app.WaitFor("text:已连接", timeout)

	// When the user commits Chinese text and presses Send, the bot echoes the probe.
	// On iOS Simulator, Type uses the English software keyboard, which can rewrite the UUID.
	app.Press("id:draft")
	app.Commit("id:draft", probe)
	if got := app.Find("id:draft").Text; got != probe {
		t.Fatalf("committed draft = %q, want %q", got, probe)
	}
	app.Press("id:send")
	app.WaitUntil(timeout, func() bool { return len(app.QueryMounted(reply)) == 1 })
	app.Press("id:latest")
	app.WaitFor(reply, timeout)

	// Then the reply is still visible after the user reconnects.
	app.Press("id:connect")
	app.WaitFor("text:已连接", timeout)
	app.Press("id:latest")
	app.WaitFor(reply, timeout)
}
