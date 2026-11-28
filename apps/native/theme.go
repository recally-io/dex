package main

import "github.com/nsgo-dev/nsgo/core"

// dexTheme follows the achromatic ledger tokens in apps/web/src/styles.css.
func dexTheme() *core.Theme {
	theme := core.DefaultTheme().Clone()
	theme.Light = core.Palette{
		Background:     core.RGB(0xffffff),
		Surface:        core.RGB(0xffffff),
		SurfaceHover:   core.RGB(0xf5f4f3),
		SurfacePressed: core.RGB(0xeceae8),
		Border:         core.RGB(0xe7e5e4),
		Text:           core.RGB(0x1c1917),
		TextMuted:      core.RGB(0x78716c),
		Accent:         core.RGB(0x292524),
		AccentHover:    core.RGB(0x3a3533),
		AccentPressed:  core.RGB(0x1c1917),
		AccentText:     core.RGB(0xfafaf9),
		Danger:         core.RGB(0xc4242a),
		Warning:        core.RGB(0xb45309),
		Success:        core.RGB(0x2e7d32),
		Focus:          core.RGB(0xa8a29e),
		Colors:         theme.Light.Colors,
	}
	theme.Dark = core.Palette{
		Background:     core.RGB(0x141210),
		Surface:        core.RGB(0x1e1c1a),
		SurfaceHover:   core.RGB(0x292624),
		SurfacePressed: core.RGB(0x322f2c),
		Border:         core.RGBA(0xffffff, 0.10),
		Text:           core.RGB(0xfafaf9),
		TextMuted:      core.RGB(0xa8a29e),
		Accent:         core.RGB(0xe7e5e4),
		AccentHover:    core.RGB(0xf5f4f3),
		AccentPressed:  core.RGB(0xd6d3d1),
		AccentText:     core.RGB(0x1c1917),
		Danger:         core.RGB(0xe5566f),
		Warning:        core.RGB(0xe0a25a),
		Success:        core.RGB(0x3fc07a),
		Focus:          core.RGB(0x78716c),
		Colors:         theme.Dark.Colors,
	}
	for _, p := range []*core.Palette{&theme.Light, &theme.Dark} {
		p.Selection = p.Accent.Faded(0.22)
		p.Scrollbar = p.Text.Faded(0.30)
		p.SetColor("field", p.Surface)
		p.SetColor("fieldBorder", p.Border)
		p.SetColor("faint", p.TextMuted)
		// NSGo's ui buttons read info for their keyboard focus ring.
		p.SetColor("info", p.Focus)
	}
	theme.Dark.SetColor("fieldBorder", core.RGBA(0xffffff, 0.15))
	return theme
}
