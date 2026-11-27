package main

import (
	"syscall/js"

	"github.com/nsgo-dev/nsgo/core"
)

// The web input owns Enter and IME handling. It does not expose a cancellable
// callback for slash navigation.
func composerInputKeys(cx *core.Scope, ref *core.Ref, navigate func(string) bool) {
	ctx := cx.Context()
	doc := js.Global().Get("document")
	keyDown := js.FuncOf(func(_ js.Value, args []js.Value) any {
		e := args[0]
		key := e.Get("key").String()
		switch key {
		case "ArrowUp", "ArrowDown", "Tab", "Escape":
		default:
			return nil
		}
		if e.Get("defaultPrevented").Bool() || e.Get("isComposing").Bool() || e.Get("keyCode").Int() == 229 ||
			e.Get("shiftKey").Bool() || e.Get("ctrlKey").Bool() || e.Get("altKey").Bool() || e.Get("metaKey").Bool() {
			return nil
		}
		field := e.Get("target")
		if field.Get("localName").String() != "textarea" || field.Call("closest", `[data-testid="draft"]`).IsNull() {
			return nil
		}
		// Wait for the graph to decide before the browser performs its default action.
		consumed := make(chan bool, 1)
		cx.Post(func() { consumed <- ref.HoldsFocus() && navigate(key) })
		select {
		case handled := <-consumed:
			if handled {
				e.Call("preventDefault")
				e.Call("stopPropagation")
			}
		case <-ctx.Done():
		}
		return nil
	})
	doc.Call("addEventListener", "keydown", keyDown, true)
	cx.Cleanup(func() {
		doc.Call("removeEventListener", "keydown", keyDown, true)
		keyDown.Release()
	})
}
