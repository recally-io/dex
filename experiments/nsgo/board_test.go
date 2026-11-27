package main

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"
	"uuid"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestUserCreatesAndUpdatesTaskFromBotBoard(t *testing.T) {
	if os.Getenv("DEX_NSGO_BOARD_TEST") != "1" || os.Getenv("DEX_NSGO_POC_TEST") != "1" {
		t.Skip("set DEX_NSGO_BOARD_TEST=1 and DEX_NSGO_POC_TEST=1 only with the dedicated test gateway")
	}
	target, err := url.Parse(os.Getenv("DEX_NSGO_TEST_URL"))
	if err != nil || target.Hostname() != "127.0.0.1" || target.Port() == "" || target.Port() == "19487" {
		t.Fatal("DEX_NSGO_TEST_URL must point to a dedicated loopback gateway on a port other than 19487")
	}
	t.Parallel()
	const timeout = 20 * time.Second
	const botID = "default"
	title := "任务探针 " + uuid.New().String()
	note := "保留这条中文备注"
	message := "发送到主会话并保留任务关联"

	// Given the default Bot's board connected to an isolated dex.
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		key := core.NewSignal(cx, "")
		chat := core.NewSignal(cx, "")
		return core.View{Style: core.Style{FlexGrow: 1, MinHeight: core.Pt(0)}, Children: []core.Node{
			core.Show{When: func() bool { return key.Get() == "" }, Then: func(scope *core.Scope) core.Node {
				return boardPage(scope, botID, func(value string) { key.Set(value) })
			}},
			core.Show{When: func() bool { return key.Get() != "" && chat.Get() == "" }, Then: func(scope *core.Scope) core.Node {
				return taskPage(scope, botID, key.Get(), func(id string, session int, _ string) {
					if id == botID && session == 0 {
						chat.Set("负责 Bot 的 Main")
					}
				})
			}},
			core.Text{ContentFunc: chat.Get},
		}}
	})
	app.WaitFor("id:board-ready", timeout)

	// When the user creates a task, its detail opens and accepts a note.
	app.Press("id:board-new")
	app.Press("id:board-task-title")
	app.Commit("id:board-task-title", title)
	app.Press("id:board-create")
	app.WaitFor("text:"+title, timeout)
	app.WaitFor("text:状态：待开始", timeout)
	app.Press("id:task-composer")
	app.Commit("id:task-composer", note)
	app.Press("id:task-note")
	app.WaitFor("text:备注已保存", timeout)
	app.WaitUntil(timeout, func() bool { return len(app.QueryMounted("text:"+note)) == 1 })

	// A change from another client makes the first start attempt stale.
	key := app.Find("id:task-key").Text
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	renamed := title + " · 已改名"
	if err := requestJSON(ctx, http.MethodPatch, "/api/tasks/"+url.PathEscape(key), map[string]string{"title": renamed}, nil); err != nil {
		t.Fatal(err)
	}
	app.Press("id:task-status-in_progress")
	app.WaitUntil(timeout, func() bool { return app.Find("id:task-title").Text == renamed })
	if got := app.Find("id:task-status").Text; got != "状态：待开始" {
		t.Fatalf("stale status update changed the task to %q", got)
	}
	app.WaitFor("id:task-transition-status", timeout)

	// Then the user can start the refreshed task and send a message that appears in its records.
	app.Press("id:task-status-in_progress")
	app.WaitFor("text:状态：进行中", timeout)
	app.Press("id:task-composer")
	app.Commit("id:task-composer", message)
	app.Press("id:task-send")
	app.WaitFor("text:消息已发送到负责 Bot 的主会话", timeout)
	app.WaitUntil(timeout, func() bool { return len(app.QueryMounted("text:"+message)) == 1 })

	// Opening Main does not create a linked session.
	app.WaitFor("text:暂无关联会话。打开主会话不会建立关联。", timeout)
	app.Press("id:task-open-main")
	app.WaitFor("text:负责 Bot 的 Main", timeout)
}
