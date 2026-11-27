//go:build !js

package main

import "github.com/nsgo-dev/nsgo/core"

// NSGo text inputs do not expose a cancellable hardware key event on native targets.
func composerInputKeys(_ *core.Scope, _ *core.Ref, _ func(string) bool) {}
