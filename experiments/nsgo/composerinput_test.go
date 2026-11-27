package main

import (
	"runtime"
	"strings"
	"testing"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestUserSendsFromComposerWithoutSendingMarkedText(t *testing.T) {
	if nsgotest.Target() == "" {
		t.Skip("run on a native or browser target to test keyboard editing")
	}
	// Given a composer with an editable draft.
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		draft := core.NewSignal(cx, "")
		sent := core.NewSignal(cx, "未发送")
		marked := core.NewSignal(cx, false)
		return core.View{Style: core.Style{Padding: core.All(20), Gap: 8}, Children: []core.Node{
			composerInput(cx, draft.Get, func() string { return "输入消息" }, func() bool { return false }, draft.Set, marked.Set, func() {
				sent.Set("已发送：" + draft.Get())
				draft.Set("")
			}, nil),
			core.Text{ContentFunc: sent.Get, TestID: "composer-test-sent"},
			core.Text{ContentFunc: func() string {
				if marked.Get() {
					return "正在合成"
				}
				return "未合成"
			}},
		}}
	})

	// When an input method marks and commits the text, it does not send it.
	app.Press("id:draft")
	app.Compose("id:draft", "zhongwen")
	app.Find("text:正在合成")
	app.Commit("id:draft", "中文")
	app.Find("text:未合成")
	app.Find("text:未发送")
	app.Key("Shift+Enter")
	if got := app.Find("id:draft").Text; got != "中文\n" {
		t.Fatalf("draft after Shift+Enter = %q, want a line break", got)
	}
	app.Commit("id:draft", "第二行")
	app.Key("Enter")

	// Then Enter sends on desktop and Web. The iOS keyboard keeps Return for editing.
	if runtime.GOOS == "ios" {
		app.Find("text:未发送")
		if got := app.Find("id:draft").Text; got != "中文\n第二行\n" {
			t.Fatalf("draft after iOS Return = %q, want a line break", got)
		}
		return
	}
	if got := app.Find("id:composer-test-sent").Text; got != "已发送：中文\n第二行" {
		t.Fatalf("composer output after Enter = %q, want the two lines sent", got)
	}
}

func TestUserCanGrowComposerAndItsMessageHasALimit(t *testing.T) {
	// Given a composer on a wide window.
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		draft := core.NewSignal(cx, "")
		return composerInput(cx, draft.Get, func() string { return "输入消息" }, func() bool { return false }, draft.Set, func(bool) {}, func() {}, nil)
	})
	field := app.Find("id:draft")
	if field.Frame.Size.Height != 44 || field.Input.MaxLength != 32000 {
		t.Fatalf("empty composer height = %v, character limit = %d, want 44 and 32000", field.Frame.Size.Height, field.Input.MaxLength)
	}

	// When several lines are added, the composer grows only as far as 200 points.
	app.Press("id:draft")
	app.Commit("id:draft", "第一行\n第二行\n第三行")
	if got := app.Find("id:draft").Frame.Size.Height; got <= 44 || got >= 200 {
		t.Fatalf("three-line composer height = %v, want between 44 and 200", got)
	}
	app.Commit("id:draft", strings.Repeat("更多内容\n", 24))
	if got := app.Find("id:draft").Frame.Size.Height; got != 200 {
		t.Fatalf("long composer height = %v, want 200", got)
	}
	app.Clear("id:draft")
	if got := app.Find("id:draft").Frame.Size.Height; got != 44 {
		t.Fatalf("cleared composer height = %v, want 44", got)
	}
}
