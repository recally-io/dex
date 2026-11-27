package main

import "syscall/js"

// A preview and a deployed app each use the origin that served the UI.
func apiURL(path string) string { return js.Global().Get("location").Get("origin").String() + path }
