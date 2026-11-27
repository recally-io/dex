package main

import (
	"strings"
	"testing"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
	"github.com/nsgo-dev/nsgo/ui"
)

func TestUserCompletesCommandsAndKeepsUnknownDraft(t *testing.T) {
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		draft := core.NewSignal(cx, "/")
		setDraft := func(text string) { draft.Set(text) }
		no := func() bool { return false }
		commands := newComposerCommands(cx, "default", func() chatSession { return chatSession{} }, draft.Get, setDraft, no, no, no)
		return core.View{Children: []core.Node{
			commands.Panel(),
			ui.Input{TestID: "test-command-draft", Value: draft.Get, OnChange: setDraft},
			ui.Button{Text: "下一个命令", TestID: "test-command-down", OnPress: func() { commands.Navigate("down") }},
			ui.Button{Text: "补全命令", TestID: "test-command-tab", OnPress: func() { commands.Navigate("tab") }},
			ui.Button{Text: "关闭补全", TestID: "test-command-escape", OnPress: func() { commands.Navigate("escape") }},
			ui.Button{Text: "执行", TestID: "test-command-submit", OnPress: func() {
				if commands.Active() {
					commands.Submit()
				}
			}},
		}}
	})

	// Given the user typed a slash, Escape closes the list without changing the draft.
	app.Find("id:composer-command-clear")
	app.Press("id:test-command-escape")
	if got := app.QueryMounted("id:composer-commands-list"); len(got) != 0 {
		t.Fatal("the command list is still visible after Escape")
	}
	if got := app.Find("id:test-command-draft").Text; got != "/" {
		t.Fatalf("Escape changed the draft to %q", got)
	}

	// When the user selects a completion, only the draft changes.
	app.Commit("id:test-command-draft", "c")
	app.Press("id:test-command-down")
	app.Press("id:test-command-tab")
	if got := app.Find("id:test-command-draft").Text; got != "/compact " {
		t.Fatalf("completed draft = %q, want /compact", got)
	}
	app.Clear("id:test-command-draft")
	app.Commit("id:test-command-draft", "/ne")
	app.Press("id:test-command-submit")
	if got := app.Find("id:test-command-draft").Text; got != "/clear " {
		t.Fatalf("alias completion = %q, want /clear", got)
	}

	// Then an unknown command is not sent and its draft stays editable.
	app.Clear("id:test-command-draft")
	app.Commit("id:test-command-draft", "/wrong 参数")
	app.Press("id:test-command-submit")
	if got := app.Find("id:composer-command-hint").Text; !strings.Contains(got, "未知命令 /wrong") {
		t.Fatalf("unknown command hint = %q", got)
	}
	if got := app.Find("id:test-command-draft").Text; got != "/wrong 参数" {
		t.Fatalf("unknown command changed the draft to %q", got)
	}
	app.Clear("id:test-command-draft")
	app.Commit("id:test-command-draft", " /wrong 参数")
	app.Press("id:test-command-submit")
	if got := app.QueryMounted("id:composer-command-hint"); len(got) != 0 {
		t.Fatal("a leading space did not escape command mode")
	}
}

func TestUserCanCompactWhileRunningButCannotClear(t *testing.T) {
	t.Parallel()
	writable := chatSession{ID: 1, ReadOnly: new(false)}
	for _, tc := range []struct {
		command         string
		running, empty  bool
		wantUnavailable string
	}{
		{command: "clear", running: true, wantUnavailable: "正在运行"},
		{command: "compact", running: true},
		{command: "clear", empty: true, wantUnavailable: "已经为空"},
		{command: "compact", empty: true, wantUnavailable: "没有可压缩"},
	} {
		got := composerCommandUnavailable(tc.command, writable, tc.running, tc.empty, false)
		if tc.wantUnavailable == "" && got != "" || tc.wantUnavailable != "" && !strings.Contains(got, tc.wantUnavailable) {
			t.Errorf("/%s (running=%t, empty=%t): got %q, want %q", tc.command, tc.running, tc.empty, got, tc.wantUnavailable)
		}
	}
}
