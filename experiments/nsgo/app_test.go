package main

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"
	"uuid"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
	"github.com/nsgo-dev/nsgo/ui"
)

func TestMain(m *testing.M) { nsgotest.Main(m) }

func chatRequireTestGateway(t *testing.T) {
	t.Helper()
	if os.Getenv("DEX_NSGO_POC_TEST") != "1" {
		t.Skip("set DEX_NSGO_POC_TEST=1 to use the isolated faux/echo gateway")
	}
	target, err := url.Parse(os.Getenv("DEX_NSGO_TEST_URL"))
	if err != nil || target.Scheme != "http" || target.Hostname() != "127.0.0.1" || target.Port() == "" || target.Port() == "19487" ||
		target.User != nil || target.Path != "" || target.RawQuery != "" || target.Fragment != "" {
		t.Fatal("DEX_NSGO_TEST_URL must be a dedicated loopback HTTP origin on a port other than 19487")
	}
}

func chatTestViewport(cx *core.Scope, page core.Node) core.Node {
	// The workspace supplies these insets to the same pages in the app.
	return core.View{Style: ui.FillRest, StyleFunc: func() core.Style {
		padding := core.Insets(cx)
		padding.Bottom = max(padding.Bottom, cx.Env().KeyboardHeight())
		return core.Style{Padding: padding}
	}, Children: []core.Node{page}}
}

func TestUserSendsChineseAndSeesReplyAfterReconnect(t *testing.T) {
	chatRequireTestGateway(t)
	const timeout = 20 * time.Second
	probe := "中文探针 " + uuid.New().String()
	reply := "text:echo: " + probe

	// Given the default bot chat with an isolated faux/echo gateway.
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		return chatTestViewport(cx, chatPage(cx, "default", 0, "主聊天", map[string]string{}))
	})
	app.WaitFor("text:已连接", timeout)

	// When the user commits Chinese text and presses Send, the bot echoes the probe.
	// On iOS Simulator, Type uses the English software keyboard, which can rewrite the UUID.
	app.Press("id:draft")
	app.Commit("id:draft", probe)
	if got := app.Find("id:draft").Text; got != probe {
		t.Fatalf("committed draft = %q, want %q", got, probe)
	}
	app.Press("id:send")
	app.WaitFor("text:已发送", timeout)
	app.WaitUntil(timeout, func() bool { return len(app.QueryMounted(reply)) == 1 })
	app.Press("id:latest")
	app.WaitFor(reply, timeout)

	// Then the reply is still visible after the user reconnects.
	app.Press("id:connect")
	app.WaitFor("text:已连接", timeout)
	app.Press("id:latest")
	app.WaitFor(reply, timeout)
}

func TestUserCreatesArchivesAndRestoresSession(t *testing.T) {
	chatRequireTestGateway(t)
	const timeout = 20 * time.Second
	probe := "归档探针 " + uuid.New().String()
	opened := make(chan int, 1)

	// Given the session list of an isolated bot.
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		id := core.NewSignal(cx, 0)
		drafts := map[string]string{}
		return chatTestViewport(cx, core.Show{When: func() bool { return id.Get() != 0 }, Then: func(cx *core.Scope) core.Node {
			return core.View{Style: core.Style{FlexGrow: 1}, Children: []core.Node{
				ui.Button{Text: "返回会话", TestID: "test-back", OnPress: func() { id.Set(0) }},
				chatPage(cx, "default", id.Get(), "新会话", drafts),
			}}
		}, Else: func(cx *core.Scope) core.Node {
			pending := core.NewSignal(cx, false)
			status := core.NewSignal(cx, "")
			return core.View{Style: ui.FillRest, Children: []core.Node{
				ui.Button{Text: "新建会话", TestID: "test-header-new", Disabled: pending.Get, OnPress: func() {
					pending.Set(true)
					work(cx, func(ctx context.Context) (chatSession, error) {
						return newWorkspaceSession(ctx, "default", 0)
					}, func(session chatSession, err error) {
						pending.Set(false)
						if err != nil {
							status.Set(err.Error())
							return
						}
						opened <- session.ID
						id.Set(session.ID)
					})
				}},
				core.Text{ContentFunc: status.Get},
				sessionsPage(cx, "default", func(sessionID int, _ string) { id.Set(sessionID) }),
			}}
		}})
	})

	// When the user creates a session and sends a message, it appears in the list.
	app.Press("id:test-header-new")
	app.WaitFor("text:已连接", timeout)
	var openedID string
	select {
	case sessionID := <-opened:
		openedID = strconv.Itoa(sessionID)
	default:
		t.Fatal("the session list did not open the new session")
	}
	app.Press("id:draft")
	app.Commit("id:draft", probe)
	if got := app.Find("id:draft").Text; got != probe {
		t.Fatalf("committed draft = %q, want %q", got, probe)
	}
	app.Press("id:send")
	app.WaitFor("text:已发送", timeout)
	reply := "text:echo: " + probe
	app.WaitUntil(timeout, func() bool { return len(app.QueryMounted(reply)) == 1 })
	app.Press("id:latest")
	app.WaitFor(reply, timeout)
	app.Press("id:test-back")
	app.WaitFor("id:sessions-archive-"+openedID, timeout)

	// Then the user can archive and restore that same session.
	app.Press("id:sessions-archive-" + openedID)
	app.WaitFor("text:已归档会话", timeout)
	app.Press("id:sessions-archived")
	app.WaitFor("id:sessions-restore-"+openedID, timeout)
	app.Press("id:sessions-restore-" + openedID)
	app.WaitFor("text:已恢复会话", timeout)
	app.Press("id:sessions-active")
	app.WaitFor("id:sessions-archive-"+openedID, timeout)
}
