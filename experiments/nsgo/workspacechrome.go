package main

import (
	"cmp"
	"context"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

func (w *workspaceView) sidebarColor(cx *core.Scope) core.Color {
	if cx.Env().Dark() {
		return core.RGB(0x1c1917)
	}
	return core.RGB(0xfbfaf9)
}

func botMark(cx *core.Scope, id string, name func() string) core.Node {
	hue := 0
	for _, c := range id {
		hue = (hue*31 + int(c)) % 360
	}
	tone := func(light, dark string) func() core.Color {
		suffix := " " + strconv.Itoa(hue) + ")"
		l, _ := core.ParseColor("oklch(" + light + suffix)
		d, _ := core.ParseColor("oklch(" + dark + suffix)
		return func() core.Color {
			if cx.Env().Dark() {
				return d
			}
			return l
		}
	}
	return core.View{Style: core.Style{Width: core.Pt(20), Height: core.Pt(20), FlexShrink: 0, CornerRadius: 6, AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter}, StyleFunc: ui.Filled(tone("0.955 0.022", "0.3 0.03")), Children: []core.Node{core.Text{ContentFunc: func() string { s := name(); _, n := utf8.DecodeRuneInString(s); return strings.ToUpper(s[:n]) }, Style: core.Style{FontWeight: ui.Semibold, FontSize: 11}, StyleFunc: ui.Colored(tone("0.42 0.06", "0.86 0.05"))}}}
}

func (w *workspaceView) toggle(cx *core.Scope) {
	v := !w.collapsed.Get()
	w.collapsed.Set(v)
	pref := "open"
	if v {
		pref = "collapsed"
	}
	_ = cx.Storage().SetItem("dex-sidebar", pref)
}
func chromeIcon(id, label string, icon func() *core.SVG, onPress func()) ui.Button {
	return ui.Button{TestID: id, Label: label, Icon: icon, Variant: ui.ButtonGhost, Side: 32, IconSize: 16, OnPress: onPress}
}
func (w *workspaceView) sidebar(cx *core.Scope, drawer bool) core.Node {
	colors := ui.ColorsOf(cx)
	lead := chromeIcon("workspace-collapse", "Hide sidebar", lucide.PanelLeft, func() { w.toggle(cx) })
	if drawer {
		lead = chromeIcon("workspace-close", "Close navigation", lucide.X, func() { w.overlay.Set("") })
	}
	top := core.View{Style: core.Style{Height: core.Pt(48), FlexShrink: 0, Direction: core.Row, AlignItems: core.AlignCenter, Padding: core.Symmetric(0, 8), Gap: 4}, Children: []core.Node{lead, core.View{Style: core.Style{FlexGrow: 1}, Children: []core.Node{ui.SidebarButton{Text: "Search", Icon: lucide.Search, TestID: "nav-search", Variant: ui.SidebarButtonField, Shortcut: "mod+k", OnPress: func() { w.overlay.Set("search") }}}}}}
	bots := core.Each(w.bots.Get, func(b botListItem) string { return b.ID }, func(cx *core.Scope, item func() botListItem, _ func() int) core.Node {
		id := item().ID
		name := func() string { return item().Name }
		selected := func() bool { r := w.route.Get(); return r.botScoped() && r.bot == id }
		mark := func() string {
			m := w.marks.Get()
			switch {
			case m.Needs[id]:
				return "Needs attention"
			case m.Working[id]:
				return "Working"
			default:
				return ""
			}
		}
		return base.Button{TestID: "nav-bot-" + id, LabelFunc: func() string {
			if s := mark(); s != "" {
				return name() + ", " + s
			}
			return name()
		}, Selected: selected, OnPress: func(core.PressEvent) { w.goTo(destination{page: "main", bot: id}, false) }, Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Height: core.Pt(36), FlexShrink: 0, Padding: core.Symmetric(0, 8), CornerRadius: 9, Gap: 10}, StyleFunc: func() core.Style {
			return core.Style{Hover: &core.Style{Background: colors.Hover()}, FocusVisible: ui.FocusRing(cx, -2)}
		}, Children: []core.Node{botMark(cx, id, name), core.Text{ContentFunc: name, MaxLines: 1, Style: core.Style{FontSize: 13.5, FlexGrow: 1, FlexShrink: 1}, StyleFunc: func() core.Style {
			if selected() {
				return core.Style{FontWeight: ui.Semibold, Color: colors.Text()}
			}
			return core.Style{FontWeight: ui.Medium, Color: colors.Muted()}
		}}, core.Show{When: func() bool { return mark() != "" }, Then: func(*core.Scope) core.Node {
			return core.View{Style: core.Style{Width: core.Pt(8), Height: core.Pt(8), CornerRadius: 4}, StyleFunc: func() core.Style {
				if w.marks.Get().Needs[id] {
					return core.Style{Background: colors.Warn()}
				}
				return core.Style{Background: colors.Accent()}
			}}
		}}}}
	})
	middle := core.Scroll{Style: ui.FillRest.Merge(core.Style{Padding: core.Symmetric(0, 8), Gap: 4}), Children: []core.Node{bots, ui.SidebarButton{Text: "New bot", TestID: "nav-new-bot", Icon: lucide.Plus, OnPress: func() { w.overlay.Set("create") }}, core.Show{When: func() bool { return w.listError.Get() != "" }, Then: func(*core.Scope) core.Node {
		return ui.Button{Text: "Could not load the bots. Retry", OnPress: w.reload}
	}}}}
	footer := []core.Node{}
	for _, entry := range []struct {
		page, label string
		icon        func() *core.SVG
	}{{"board", "Board", lucide.ListTodo}, {"integrations", "Integrations", lucide.Cable}, {"global-settings", "Settings", lucide.Settings}} {
		active := func() bool {
			p := w.route.Get().page
			return p == entry.page || entry.page == "board" && p == "task" || entry.page == "global-settings" && p == "archived"
		}
		badge := func() string {
			m := w.marks.Get()
			if entry.page == "board" && m.Count > 0 {
				return strconv.Itoa(m.Count)
			}
			if entry.page == "global-settings" && m.ProviderErrors > 0 {
				return "●"
			}
			return ""
		}
		footer = append(footer, core.View{Children: []core.Node{ui.SidebarButton{Text: entry.label, Icon: entry.icon, Selected: active, TestID: "nav-" + entry.page, OnPress: func() { w.visit(entry.page) }}, core.Text{ContentFunc: badge, Style: core.Style{Position: core.Absolute, Top: core.Pt(8), Right: core.Pt(9), FontSize: 12, PointerEvents: core.PointerEventsNone}, StyleFunc: ui.Colored(colors.Warn)}}})
	}
	width := core.Pt(260)
	if drawer {
		width = core.Pct(100)
	}
	return core.View{TestID: "sidebar", Style: core.Style{Width: width, Height: core.Pct(100), FlexShrink: 0, Borders: core.Edges{Right: 1}}, StyleFunc: func() core.Style { return core.Style{Background: w.sidebarColor(cx), BorderColor: colors.Line()} }, Children: []core.Node{top, middle, core.View{Style: core.Style{Padding: core.All(8), Gap: 4, FlexShrink: 0, Borders: core.Edges{Top: 1}}, StyleFunc: func() core.Style { return core.Style{BorderColor: colors.Line()} }, Children: footer}}}
}

func (w *workspaceView) header(cx *core.Scope, d destination, openChat func(string, int, string)) core.Node {
	colors := ui.ColorsOf(cx)
	compact := func() bool { return w.compact(cx) }
	sx := core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 2}
	left := core.View{Style: sx, Children: []core.Node{core.Show{When: compact, Then: func(*core.Scope) core.Node {
		return chromeIcon("workspace-menu", "Open navigation", lucide.Menu, func() { w.overlay.Set("drawer") })
	}, Else: func(cx *core.Scope) core.Node {
		return core.Show{When: w.collapsed.Get, Then: func(*core.Scope) core.Node {
			return chromeIcon("workspace-expand", "Show sidebar", lucide.PanelLeft, func() { w.toggle(cx) })
		}}
	}}, core.Show{When: w.parent.Get, Then: func(*core.Scope) core.Node { return chromeIcon("workspace-back", "Back", lucide.ChevronLeft, w.back) }}}}
	labels := map[string]string{"sessions": "Sessions", "automations": "Automations", "artifacts": "Files", "settings": "Settings", "tools": "Settings", "board": "Board", "task": "Board", "integrations": "Integrations", "global-settings": "Settings", "archived": "Settings"}
	sessionTitle := core.NewSignal(cx, "New session")
	if d.page == "session" {
		work(cx, func(ctx context.Context) (chatSession, error) { return chatGetSession(ctx, d.bot, d.session) }, func(s chatSession, err error) {
			if err == nil {
				sessionTitle.Set(cmp.Or(s.Title, "New session"))
			}
		})
	}
	title := core.Text{ContentFunc: func() string {
		switch d.page {
		case "main":
			return w.name()
		case "session":
			return sessionTitle.Get()
		}
		return cmp.Or(labels[d.page], "Dex")
	}, MaxLines: 1, Style: core.Style{FontSize: 13, FontWeight: ui.Medium, FlexShrink: 1}, StyleFunc: ui.Colored(colors.Text)}
	headStyle := sx.Merge(core.Style{Height: core.Pt(48), FlexShrink: 0, Padding: core.Symmetric(0, 8), Borders: core.Edges{Bottom: 1}})
	headColor := func() core.Style { return core.Style{BorderColor: colors.Line()} }
	if !d.botScoped() {
		return core.View{TestID: "workspace-header", Style: headStyle, StyleFunc: headColor, Children: []core.Node{left, title}}
	}
	busy := core.NewSignal(cx, false)
	problem := core.NewSignal(cx, "")
	create := func() {
		if busy.Get() {
			return
		}
		busy.Set(true)
		problem.Set("")
		work(cx, func(ctx context.Context) (chatSession, error) { return newWorkspaceSession(ctx, d.bot, d.session) }, func(s chatSession, err error) {
			busy.Set(false)
			if err != nil {
				problem.Set(err.Error())
				return
			}
			openChat(d.bot, s.ID, s.Title)
		})
	}
	var buttons, items []core.Node
	for _, item := range []struct {
		id, label string
		icon      func() *core.SVG
	}{{"new-session", "New session", lucide.Plus}, {"sessions", "Sessions", lucide.List}, {"automations", "Automations", lucide.CalendarClock}, {"settings", "Bot settings", lucide.Settings}} {
		action := func() { w.visit(item.id) }
		var disabled func() bool
		if item.id == "new-session" {
			action = create
			disabled = busy.Get
		}
		b := chromeIcon("nav-"+item.id, item.label, item.icon, action)
		b.Disabled = disabled
		b.Side = 28
		b.Tint = colors.Muted
		buttons = append(buttons, b)
		items = append(items, ui.MenuItem{Text: item.label, Icon: item.icon, TestID: b.TestID, OnSelect: action, Disabled: disabled})
	}
	actions := []core.Node{}
	if d.page == "main" || d.page == "session" {
		actions = append(actions, chromeIcon("nav-artifacts", "Artifacts", lucide.Files, func() { w.goTo(destination{page: "artifacts", bot: d.bot}, true) }))
	}
	actions = append(actions, core.Show{When: compact, Then: func(*core.Scope) core.Node {
		return ui.Menu{TestID: "bot-menu", Label: "Bot menu: " + w.name(), Trigger: ui.Button{Text: "Menu", Icon: lucide.ChevronDown, Variant: ui.ButtonGhost}, Align: core.AlignEnd, Children: items}
	}, Else: func(*core.Scope) core.Node { return core.View{Style: sx, Children: buttons} }})
	wing := sx.Merge(core.Style{FlexGrow: 1, FlexBasis: core.Pt(0), MinWidth: core.Pt(0)})
	head := core.View{TestID: "workspace-header", Style: headStyle, StyleFunc: headColor, Children: []core.Node{core.View{Style: wing, Children: []core.Node{left}}, core.View{Style: sx.Merge(core.Style{Gap: 8, MaxWidth: core.Pct(40)}), Children: []core.Node{botMark(cx, d.bot, w.name), title}}, core.View{Style: wing.Merge(core.Style{JustifyContent: core.JustifyEnd}), Children: actions}}}
	return core.View{Style: core.Style{FlexShrink: 0}, Children: []core.Node{head, core.Show{When: func() bool { return problem.Get() != "" }, Then: func(*core.Scope) core.Node {
		return core.Text{ContentFunc: problem.Get, Role: core.RoleStatus, StyleFunc: ui.Colored(colors.Danger)}
	}}}}
}
