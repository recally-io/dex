package main

import (
	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

func botProfilePicker(cx *core.Scope, id, name string, caption func() string, width func() core.Points,
	disabled func() bool, items func() []base.CommandItem, choose func(string)) core.Node {
	colors := ui.ColorsOf(cx)
	triggerStyle := func() core.Style {
		return core.Style{Width: core.Pt(width()), Height: core.Pt(32), CornerRadius: 6,
			Background: colors.Hover(), AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter,
			Hover: &core.Style{Background: colors.Pressed()}, FocusVisible: &core.Style{OutlineWidth: 2, OutlineColor: colors.Accent()}}
	}
	trigger := core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 8}, Children: []core.Node{
		core.Text{ContentFunc: caption, MaxLines: 1, Style: core.Style{FontSize: 13, FontWeight: core.FontWeightMedium}},
		core.Show{When: func() bool { return name != "thinking levels" || !disabled() }, Then: func(*core.Scope) core.Node {
			return ui.Icon(lucide.ChevronDown, 12, colors.Muted)
		}},
	}}
	open := core.NewSignal(cx, false)
	return core.Show{When: disabled, Then: func(*core.Scope) core.Node {
		return base.Button{TestID: id, LabelFunc: caption, Disabled: disabled,
			StyleFunc: triggerStyle, Style: core.Style{Disabled: &core.Style{Opacity: core.Alpha(0.5)}}, Children: []core.Node{trigger}}
	}, Else: func(cx *core.Scope) core.Node {
		return base.Popover{TestID: id, LabelFunc: caption, Open: open.Get,
			OnOpenChange: func(next bool, _ base.Reason) { open.Set(next && !disabled()) },
			Placement:    core.PlaceBottom, Align: core.AlignStart, Offset: 6, Margin: 8, StyleFunc: triggerStyle, Trigger: trigger,
			PopupStyle: core.Style{Height: core.Pt(300), Overflow: core.Hidden, CornerRadius: 12, BorderWidth: 1},
			PopupStyleFunc: func() core.Style {
				return core.Style{Width: core.Pt(min(560, cx.Env().Window().Width-16)), Background: colors.Surface(), BorderColor: colors.Border()}
			},
			Children: []core.Node{ui.Command{TestID: id + "-options", Label: name, SearchLabel: "Search " + name, Placeholder: "Search " + name,
				Empty: "No matching options", Style: core.Style{FlexGrow: 1, FlexBasis: core.Pt(0), MinHeight: core.Pt(0)}, Items: items, AutoFocus: true,
				OnCancel: func() { open.Set(false) }, OnSelect: func(key string) {
					open.Set(false)
					choose(key)
				}}},
		}
	}}
}
