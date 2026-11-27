package main

import (
	"strings"
	"syscall/js"
	"testing"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
	"github.com/nsgo-dev/nsgo/ui"
)

func TestUserKeepsBrowserIMEAndSlashKeysSeparate(t *testing.T) {
	// Given a draft with slash suggestions and a focusable control after it.
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		draft := core.NewSignal(cx, "")
		last := core.NewSignal(cx, "未导航")
		sent := core.NewSignal(cx, "未发送")
		return core.View{Style: core.Style{Padding: core.All(20), Gap: 8}, Children: []core.Node{
			composerInput(cx, draft.Get, nil, nil, draft.Set, nil, func() { sent.Set("已发送") }, func(key string) bool {
				if draft.Get() != "/" {
					return false
				}
				last.Set(key)
				return true
			}),
			ui.Button{Text: "下一个控件", TestID: "composer-test-next"},
			core.Text{ContentFunc: last.Get},
			core.Text{ContentFunc: sent.Get},
		}}
	})
	app.Press("id:draft")
	app.Commit("id:draft", "/")

	// When the browser reports keyCode 229 without isComposing, it is still an IME key.
	field := js.Global().Get("document").Call("querySelector", `textarea[data-testid="draft"]`)
	if field.IsNull() {
		t.Fatalf("composer textarea not found in the browser; focus: %s", js.Global().Get("document").Get("activeElement").Get("outerHTML").String())
	}
	for _, key := range []string{"Enter", "ArrowDown"} {
		event := js.Global().Get("KeyboardEvent").New("keydown", map[string]any{
			"key": key, "keyCode": 229, "bubbles": true, "cancelable": true,
		})
		field.Call("dispatchEvent", event)
	}
	app.Settle()
	app.Find("text:未发送")
	app.Find("text:未导航")

	// Then handled keys reach the slash menu and Tab moves focus only when the menu declines it.
	for _, step := range []struct{ key, label string }{{"down", "ArrowDown"}, {"up", "ArrowUp"}, {"Escape", "Escape"}, {"Tab", "Tab"}} {
		app.Key(step.key)
		app.Find("text:" + step.label)
	}
	if !app.Find("id:draft").Focused {
		t.Fatal("handled Tab moved focus away from the draft")
	}
	app.Clear("id:draft")
	app.Key("Tab")
	if !app.Find("id:composer-test-next").Focused {
		t.Fatal("unhandled Tab did not move focus")
	}

	// An overlong IME commit must not leave half of a character in the draft.
	app.Press("id:draft")
	app.Commit("id:draft", strings.Repeat("狐", 31999)+"🦊")
	if got := app.Find("id:draft").Text; core.UTF16Len(got) != 31999 || strings.HasSuffix(got, "�") {
		t.Fatalf("overlong commit left %d UTF-16 units, want 31999 complete units", core.UTF16Len(got))
	}
}
