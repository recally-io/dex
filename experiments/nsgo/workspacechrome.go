package main

import (
	"cmp"
	"context"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

const chromeFont = `-apple-system, system-ui, "SF Pro Text", "PingFang SC", "Hiragino Sans GB", "Noto Sans SC", "Segoe UI", sans-serif`

func chromeText(size core.Points, weight core.FontWeight, drawer bool) core.Style {
	lineHeight := float32(1.5)
	if drawer {
		lineHeight = 1.625
	}
	return core.Style{FontFamily: chromeFont, FontSize: size, FontWeight: weight, LineHeightMultiple: lineHeight}
}

func (w *workspaceView) sidebarColor(cx *core.Scope) core.Color {
	return cx.Palette().Colors["sidebar"]
}

func botMark(cx *core.Scope, id string, name func() string, drawer bool) core.Node {
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
	return core.View{Style: core.Style{Width: core.Pt(20), Height: core.Pt(20), FlexShrink: 0, CornerRadius: 6, AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter}, StyleFunc: ui.Filled(tone("0.955 0.022", "0.3 0.03")), Children: []core.Node{core.Text{ContentFunc: func() string { s := name(); _, n := utf8.DecodeRuneInString(s); return strings.ToUpper(s[:n]) }, Style: chromeText(11, ui.Semibold, drawer), StyleFunc: ui.Colored(tone("0.42 0.06", "0.86 0.05"))}}}
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
func chromeIcon(cx *core.Scope, id, label string, icon func() *core.SVG, side, size core.Points, onPress func()) base.Button {
	colors := ui.ColorsOf(cx)
	return base.Button{TestID: id, Label: label, OnPress: func(core.PressEvent) { onPress() },
		Style: core.Style{Width: core.Pt(side), Height: core.Pt(side), FlexShrink: 0,
			AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter, CornerRadius: 6},
		StyleFunc: func() core.Style {
			highlight := core.Style{Background: colors.Hover(), Color: colors.Text()}
			return core.Style{Color: colors.Muted(), Hover: &highlight, Selected: &highlight, FocusVisible: ui.FocusRing(cx, -2)}
		},
		Children: []core.Node{ui.Icon(icon, size, ui.Inherited)},
	}
}

func (w *workspaceView) drawer(cx *core.Scope, closeOverlay func()) core.Node {
	colors := ui.ColorsOf(cx)
	return base.Dialog{Label: "Navigation", TestID: "workspace-drawer", DefaultOpen: true, Animation: core.ModalNone,
		OnOpenChange: func(open bool, _ base.Reason) {
			if !open {
				closeOverlay()
			}
		},
		BackdropStyle: core.Style{Background: ui.Black(0.10), BackdropFilters: []core.Filter{core.Blur(4)}},
		LayerStyle:    core.Style{AlignItems: core.AlignStart},
		StartingStyle: core.Style{Transform: core.Transform{TranslateXPercent: -100}},
		PopupStyle: core.Style{Height: core.Pct(100), Width: core.Pt(260), MaxWidth: core.Pct(85),
			Transition: core.Transition{Duration: 240 * time.Millisecond, Properties: core.TransitionTransform,
				Easing: core.Curve{X1: 0.23, Y1: 1, X2: 0.32, Y2: 1}}},
		PopupStyleFunc: func() core.Style {
			return core.Style{Background: w.sidebarColor(cx), Padding: core.Insets(cx), Color: colors.Text(),
				Shadows: []core.BoxShadow{
					core.Ring(1, ui.Alpha(colors.Text(), 0.10)),
					core.Ring(1, cx.Palette().Focus),
					{Offset: core.Point{Y: 12}, Blur: 32, Color: ui.Black(0.12)},
				}}
		},
		Children: []core.Node{w.sidebar(cx, true)},
	}
}
func (w *workspaceView) sidebar(cx *core.Scope, drawer bool) core.Node {
	colors := ui.ColorsOf(cx)
	lead := chromeIcon(cx, "workspace-collapse", "Hide sidebar", lucide.PanelLeft, 32, 16, func() { w.toggle(cx) })
	if drawer {
		lead = chromeIcon(cx, "workspace-close", "Close navigation", lucide.X, 32, 17, func() { w.overlay.Set("") })
		lead.AutoFocus = true
	}
	search := base.Button{TestID: "nav-search", Label: "Search bots and sessions", OnPress: func(core.PressEvent) { w.overlay.Set("search") },
		Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, FlexGrow: 1, MinWidth: core.Pt(0), Height: core.Pt(28), Gap: 8, Padding: core.Symmetric(0, 11), CornerRadius: 6},
		StyleFunc: func() core.Style {
			return core.Style{Color: colors.Muted(), Background: ui.Alpha(colors.Hover(), 0.6),
				Hover: &core.Style{Background: colors.Hover(), Color: colors.Text()}, FocusVisible: ui.FocusRing(cx, -2)}
		}, Children: []core.Node{
			ui.Icon(lucide.Search, 14, ui.Inherited),
			core.Text{Content: "Search", MaxLines: 1, Style: chromeText(13, ui.Medium, drawer).Merge(core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0)})},
			core.Text{Content: cx.KeyLabel("mod+k"), MaxLines: 1, Style: chromeText(10, ui.Medium, drawer).Merge(core.Style{FontFamily: core.FontMonospace, FlexShrink: 0})},
		}}
	top := core.View{Style: core.Style{Height: core.Pt(48), FlexShrink: 0, Direction: core.Row, AlignItems: core.AlignCenter, Padding: core.Symmetric(0, 8), Gap: 4}, Children: []core.Node{lead, search}}
	row := func(id, label string, icon func() *core.SVG, iconSize core.Points, selected func() bool, onPress func(), tail core.Node) base.Button {
		children := []core.Node{ui.Icon(icon, iconSize, ui.Inherited), core.Text{Content: label, MaxLines: 1, Style: chromeText(13, ui.Regular, drawer).Merge(core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0)})}}
		if tail != nil {
			children = append(children, tail)
		}
		return base.Button{TestID: id, Label: label, Selected: selected, OnPress: func(core.PressEvent) { onPress() },
			Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Height: core.Pt(32), FlexShrink: 0, MinWidth: core.Pt(0), Gap: 8, Padding: core.Symmetric(0, 8), CornerRadius: 9},
			StyleFunc: func() core.Style {
				return core.Style{Color: colors.Muted(), FocusVisible: ui.FocusRing(cx, -2),
					Hover:    &core.Style{Background: colors.Hover(), Color: colors.Text()},
					Selected: &core.Style{Background: colors.Surface(), Color: colors.Text(), Shadows: []core.BoxShadow{core.Ring(1, colors.Border())}}}
			}, Children: children}
	}
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
		}, Children: []core.Node{botMark(cx, id, name, drawer), core.Text{ContentFunc: name, MaxLines: 1, Style: chromeText(13.5, ui.Medium, drawer).Merge(core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0)}), StyleFunc: func() core.Style {
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
	newBot := row("nav-new-bot", "New bot", lucide.Plus, 14, nil, func() { w.overlay.Set("create") }, nil)
	newBot.Style.Margin.Top = 4
	middle := core.Scroll{Style: ui.FillRest.Merge(core.Style{Padding: core.Edges{Left: 8, Right: 8, Bottom: 12}, Gap: 4}), Children: []core.Node{bots, newBot, core.Show{When: func() bool { return w.listError.Get() != "" }, Then: func(*core.Scope) core.Node {
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
		var badge core.Node
		switch entry.page {
		case "board":
			badge = core.Show{When: func() bool { return w.marks.Get().Count > 0 }, Then: func(*core.Scope) core.Node {
				return core.Text{ContentFunc: func() string { return strconv.Itoa(w.marks.Get().Count) }, Style: core.Style{FontSize: 12, Padding: core.Symmetric(0, 6), CornerRadius: 10},
					StyleFunc: func() core.Style { return core.Style{Background: colors.Hover(), Color: colors.Warn()} }}
			}}
		case "global-settings":
			badge = core.Show{When: func() bool { return w.marks.Get().ProviderErrors > 0 }, Then: func(*core.Scope) core.Node {
				return core.View{Style: core.Style{Width: core.Pt(8), Height: core.Pt(8), CornerRadius: 4}, StyleFunc: ui.Filled(colors.Danger)}
			}}
		}
		footer = append(footer, row("nav-"+entry.page, entry.label, entry.icon, 14, active, func() { w.visit(entry.page) }, badge))
	}
	width := core.Pt(260)
	if drawer {
		width = core.Pct(100)
	}
	return core.View{TestID: "sidebar", Style: core.Style{Width: width, Height: core.Pct(100), FlexShrink: 0, Borders: core.Edges{Right: 1}}, StyleFunc: func() core.Style { return core.Style{Background: w.sidebarColor(cx), BorderColor: colors.Border()} }, Children: []core.Node{top, middle, core.View{Style: core.Style{Padding: core.All(8), Gap: 4, FlexShrink: 0, Borders: core.Edges{Top: 1}}, StyleFunc: func() core.Style { return core.Style{BorderColor: colors.Border()} }, Children: footer}}}
}

func (w *workspaceView) header(cx *core.Scope, d destination, openChat func(string, int, string)) core.Node {
	colors := ui.ColorsOf(cx)
	compact := func() bool { return w.compact(cx) }
	sx := core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 2}
	var drawerTrigger core.Ref
	wasOpen := false
	cx.Effect(func() {
		overlay := w.overlay.Get()
		if wasOpen && overlay == "" {
			cx.Post(func() {
				if w.overlay.Get() == "" {
					drawerTrigger.Focus()
				}
			})
		}
		wasOpen = overlay == "drawer"
	})
	attention := func() bool { return w.marks.Get().Count > 0 }
	navControl := func(button base.Button, offset core.Points) base.Button {
		button.Style.Margin.Left = -4
		label := button.Label
		button.Label = ""
		button.LabelFunc = func() string {
			if attention() {
				return label + ", Board needs attention"
			}
			return label
		}
		button.Children = append(button.Children, core.Show{When: attention, Then: func(*core.Scope) core.Node {
			return core.View{Style: core.Style{Position: core.Absolute, Top: core.Pt(offset), Right: core.Pt(offset),
				Width: core.Pt(8), Height: core.Pt(8), CornerRadius: 4}, StyleFunc: func() core.Style {
				return core.Style{Background: colors.Warn(), Shadows: []core.BoxShadow{core.Ring(2, colors.Background())}}
			}}
		}})
		return button
	}
	left := core.View{Style: sx.Merge(core.Style{Gap: 4}), Children: []core.Node{core.Show{When: compact, Then: func(*core.Scope) core.Node {
		menu := chromeIcon(cx, "workspace-menu", "Open navigation", lucide.Menu, 36, 18, func() { w.overlay.Set("drawer") })
		menu.Ref = &drawerTrigger
		return navControl(menu, 6)
	}, Else: func(cx *core.Scope) core.Node {
		return core.Show{When: w.collapsed.Get, Then: func(*core.Scope) core.Node {
			return navControl(chromeIcon(cx, "workspace-expand", "Show sidebar", lucide.PanelLeft, 32, 16, func() { w.toggle(cx) }), 4)
		}}
	}}, core.Show{When: w.parent.Get, Then: func(*core.Scope) core.Node {
		return chromeIcon(cx, "workspace-back", "Back", lucide.ChevronLeft, 32, 16, w.back)
	}}}}
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
	}, MaxLines: 1, Style: chromeText(13, ui.Medium, false).Merge(core.Style{FlexShrink: 1, MinWidth: core.Pt(0)}), StyleFunc: ui.Colored(colors.Text)}
	headStyle := sx.Merge(core.Style{Height: core.Pt(48), FlexShrink: 0, Gap: 4, Borders: core.Edges{Bottom: 1}})
	headColor := func() core.Style {
		left := core.Points(16)
		if cx.Env().Window().Width < 640 {
			left = 12
		}
		return core.Style{Padding: core.Edges{Left: left, Right: 8}, BorderColor: colors.Border()}
	}
	if !d.botScoped() {
		title.Style = chromeText(14, ui.Semibold, false).Merge(core.Style{MaxWidth: core.Pct(60), LetterSpacingEm: -0.01})
		return core.View{TestID: "workspace-header", Style: headStyle.Merge(core.Style{Gap: 8}), StyleFunc: headColor, Children: []core.Node{
			core.Show{When: func() bool { return compact() || w.collapsed.Get() || w.parent.Get() }, Then: func(*core.Scope) core.Node { return left }}, title,
		}}
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
		b := chromeIcon(cx, "nav-"+item.id, item.label, item.icon, 28, 16, action)
		b.Disabled = disabled
		if item.id != "new-session" {
			b.Selected = func() bool { return d.page == item.id || item.id == "settings" && d.page == "tools" }
		}
		buttons = append(buttons, b)
		items = append(items, base.MenuItem{Label: item.label, TestID: b.TestID, OnSelect: action, Disabled: disabled,
			Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 8,
				Height: core.Pt(32), Padding: core.Symmetric(0, 8), CornerRadius: 8, FlexShrink: 0},
			StyleFunc: func() core.Style {
				hi := core.Style{Background: colors.Hover(), Color: colors.Text()}
				return core.Style{Color: colors.Text(), Hover: &hi, FocusWithin: &hi, Disabled: &core.Style{Opacity: core.Alpha(0.5)}}
			},
			Children: []core.Node{ui.Icon(item.icon, 16, ui.Inherited),
				core.Text{Content: item.label, MaxLines: 1, Style: chromeText(12, ui.Regular, false).Merge(core.Style{LineHeight: 16, FlexShrink: 1})}},
		})
	}
	actions := []core.Node{}
	if d.page == "main" || d.page == "session" {
		files := chromeIcon(cx, "nav-artifacts", "Artifacts", lucide.Files, 28, 16, func() { w.goTo(destination{page: "artifacts", bot: d.bot}, true) })
		actions = append(actions, files)
	}
	actions = append(actions, core.Show{When: compact, Then: func(*core.Scope) core.Node {
		return base.Menu{TestID: "bot-menu", LabelFunc: func() string { return "Bot menu: " + w.name() },
			Style: core.Style{Height: core.Pt(28), Width: core.Pt(74), CornerRadius: 6, FlexShrink: 0},
			StyleFunc: func() core.Style {
				return core.Style{Color: colors.Muted(), FocusVisible: ui.FocusRing(cx, -2),
					Hover: &core.Style{Background: colors.Hover(), Color: colors.Text()}}
			},
			Trigger: core.View{Style: sx.Merge(core.Style{Height: core.Pct(100), Width: core.Pct(100), Gap: 4, JustifyContent: core.JustifyCenter}), Children: []core.Node{
				core.Text{Content: "Menu", Style: chromeText(13, ui.Medium, false)}, ui.Icon(lucide.ChevronDown, 14, ui.Inherited),
			}},
			Align: core.AlignEnd, Offset: 4, Margin: 8,
			PopupStyle: core.Style{Width: core.Pt(240), CornerRadius: 8, Overflow: core.Hidden},
			PopupStyleFunc: func() core.Style {
				return core.Style{MaxWidth: core.Pt(cx.Env().Window().Width - 16), Background: colors.Surface(),
					Shadows: []core.BoxShadow{
						core.Ring(1, ui.Alpha(colors.Text(), 0.10)),
						{Offset: core.Point{Y: 4}, Blur: 6, Spread: -1, Color: ui.Black(0.10)},
						{Offset: core.Point{Y: 2}, Blur: 4, Spread: -2, Color: ui.Black(0.10)},
					}}
			}, Children: items}
	}, Else: func(*core.Scope) core.Node { return core.View{Style: sx, Children: buttons} }})
	wing := sx.Merge(core.Style{FlexGrow: 1, FlexBasis: core.Pt(0), MinWidth: core.Pt(0)})
	head := core.View{TestID: "workspace-header", Style: headStyle, StyleFunc: headColor, Children: []core.Node{
		core.View{Style: wing, Children: []core.Node{left}},
		core.View{Style: sx.Merge(core.Style{Gap: 8, MaxWidth: core.Pct(50)}), Children: []core.Node{botMark(cx, d.bot, w.name, false), title}},
		core.View{Style: wing.Merge(core.Style{Gap: 4, JustifyContent: core.JustifyEnd}), Children: actions},
	}}
	return core.View{Style: core.Style{FlexShrink: 0}, Children: []core.Node{head, core.Show{When: func() bool { return problem.Get() != "" }, Then: func(*core.Scope) core.Node {
		return core.Text{ContentFunc: problem.Get, Role: core.RoleStatus, StyleFunc: ui.Colored(colors.Danger)}
	}}}}
}
