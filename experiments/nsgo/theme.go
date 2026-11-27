package main

import (
	_ "embed"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

//go:generate node theme.mjs
//go:embed theme.json
var themeData []byte

var workspaceTheme = func() *core.Theme {
	t, err := core.ParseTheme(themeData)
	if err != nil {
		panic(err)
	}
	// NSGo uses these two roles for a primary button under the pointer.
	for _, p := range []*core.Palette{&t.Light, &t.Dark} {
		p.AccentHover = ui.Mix(p.Accent, p.Background, 0.10)
		p.AccentPressed = ui.Mix(p.Accent, p.Background, 0.20)
		p.Selection = p.Focus.Faded(0.3)
	}
	return t
}()
