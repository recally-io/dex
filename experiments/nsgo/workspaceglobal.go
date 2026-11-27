package main

import (
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

func globalSection(cx *core.Scope, id, title, caption string, rows []core.Node) core.Node {
	colors := ui.ColorsOf(cx)
	children := []core.Node{core.Text{Content: title, Role: core.RoleHeader, Style: core.Style{FontSize: 14, FontWeight: ui.Semibold}}}
	if caption != "" {
		children = append(children, globalMuted(cx, caption))
	}
	children = append(children, core.View{Style: core.Style{Gap: 10, Padding: core.All(12), BorderWidth: 1, CornerRadius: 9},
		StyleFunc: func() core.Style { return core.Style{Background: colors.Surface(), BorderColor: colors.Border()} }, Children: rows})
	return core.View{TestID: id, Style: core.Style{Gap: 8}, Children: children}
}

func globalRow(cx *core.Scope, id, label, value string) core.Node {
	children := []core.Node{core.Text{Content: label, Style: core.Style{FontSize: 14, FontWeight: ui.Medium}}}
	if value != "" {
		children = append(children, globalMuted(cx, value))
	}
	return core.View{TestID: id, Style: core.Style{Gap: 4}, Children: children}
}

func globalMuted(cx *core.Scope, value string) core.Text {
	return ui.Text(value, 13, ui.Regular, ui.ColorsOf(cx).Muted)
}
