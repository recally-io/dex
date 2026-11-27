//go:build js && wasm

package main

import "syscall/js"

func init() {
	document := js.Global().Get("document")
	if !document.Truthy() {
		return
	}
	// Match the inherited text rendering of the Dex web UI.
	style := document.Get("documentElement").Get("style")
	style.Call("setProperty", "-webkit-font-smoothing", "antialiased")
	style.Call("setProperty", "text-rendering", "optimizeLegibility")
}
