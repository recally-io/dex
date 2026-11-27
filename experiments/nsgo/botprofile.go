package main

import (
	"net/url"
	"strings"

	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

// botProfileScreen owns the Bot settings page. The shell owns the URL and history.
func botProfileScreen(cx *core.Scope, botID, tab string, navigate func(string), onSaved func(string)) core.Node {
	base := botPath(botID)
	tab, rawQuery, _ := strings.Cut(tab, "?")
	query, _ := url.ParseQuery(rawQuery)
	switch tab {
	case "integrations":
		return botProfileFrame(cx, tab, "Integrations", func() string {
			return "Browse this Bot's effective skills and MCP servers. Bot entries can override shared entries."
		}, navigate, toolsCatalogSection(cx, base+"/integrations", query.Get("section")))
	case "channels":
		return botProfileFrame(cx, tab, "Channels", func() string {
			return "Connect this Bot to private channels. Channel chats share Main."
		}, navigate, toolsChannelsSection(cx, base, navigate))
	case "secrets":
		return botProfileFrame(cx, tab, "Secrets", nil, navigate,
			botProfilePanel(cx, toolsRow(toolsCaption(cx, "Put secrets in the vault. Do not paste secret values in sessions, instructions, or skill files. Use vault:NAME to share a reference with the bot."))),
			toolsVaultSection(cx, base+"/vault"))
	}
	if key, ok := strings.CutPrefix(tab, "channels/"); ok {
		for _, channel := range toolsChannelList {
			if channel.key == key {
				return botProfileFrame(cx, "channels", channel.name, botProfileCopy(channel.description), navigate,
					toolsButton("tools-channels-back", "Back to Channels", ui.ButtonGhost, nil, func() { navigate("channels") }),
					toolsChannelDetails(cx, base+"/channels", channel))
			}
		}
		return botProfileFrame(cx, "channels", "Channel not found", nil, navigate,
			toolsButton("tools-channels-back", "Back to Channels", ui.ButtonGhost, nil, func() { navigate("channels") }))
	}
	return botProfileGeneral(cx, botID, navigate, onSaved)
}

func botProfileFrame(cx *core.Scope, tab, title string, description func() string, navigate func(string), children ...core.Node) core.Node {
	colors := ui.ColorsOf(cx)
	heading := []core.Node{core.Text{Content: title, Role: core.RoleHeader, Style: core.Style{FontSize: 18, LineHeight: 22.5, LetterSpacingEm: -0.015, AlignSelf: core.AlignStart, FontWeight: core.FontWeightSemibold}}}
	if description != nil {
		heading = append(heading, core.Text{ContentFunc: description, StyleFunc: func() core.Style {
			return core.Style{FontSize: 13.5, LineHeight: 21, MaxWidth: core.Pt(640), Color: colors.Muted()}
		}})
	}
	body := []core.Node{core.View{Style: core.Style{Gap: 8, Margin: core.Edges{Bottom: 3}}, Children: heading}}
	if navigate != nil {
		var links []core.Node
		for _, p := range []struct{ path, title string }{
			{"bot", "General"}, {"integrations", "Integrations"}, {"channels", "Channels"}, {"secrets", "Secrets"},
		} {
			current := tab == p.path
			label := p.title
			if current {
				label += ", current page"
			}
			links = append(links, base.Button{TestID: "bot-settings-tab-" + p.path, Label: label,
				Selected: func() bool { return current },
				Style:    core.Style{Height: core.Pt(28), CornerRadius: 6, JustifyContent: core.JustifyCenter},
				StyleFunc: func() core.Style {
					pad := core.Points(11.625)
					if cx.Env().Window().Width < 370 {
						pad = 8
					}
					style := core.Style{Padding: core.Symmetric(0, pad), FocusVisible: &core.Style{OutlineWidth: 2, OutlineColor: colors.Accent()}}
					if current {
						style.Background = colors.Surface()
					} else {
						style.Hover = &core.Style{Background: colors.Surface()}
					}
					return style
				}, Children: []core.Node{core.Text{Content: p.title, StyleFunc: func() core.Style {
					if current {
						return core.Style{FontSize: 12.5, FontWeight: core.FontWeightMedium, Color: colors.Text()}
					}
					return core.Style{FontSize: 12.5, Color: colors.Muted()}
				}}}, OnPress: func(core.PressEvent) {
					if !current {
						navigate(p.path)
					}
				}})
		}
		body = append(body, core.View{TestID: "bot-settings-pages", Style: core.Style{Direction: core.Row, Wrap: core.Wrap,
			AlignSelf: core.AlignStart, MaxWidth: core.Pct(100), Gap: 2, CornerRadius: 8, Padding: core.All(2), Margin: core.Edges{Bottom: 4}},
			StyleFunc: func() core.Style { return core.Style{Background: colors.Hover()} }, Children: links})
	}
	return core.Scroll{TestID: "bot-settings-page", Style: core.Style{FlexGrow: 1, FlexBasis: core.Pt(0), MinHeight: core.Pt(0)},
		Children: []core.Node{core.View{Style: core.Style{Width: core.Pct(100), MaxWidth: core.Pt(816), Gap: 20,
			Padding: core.Edges{Top: 24, Bottom: 40, Left: 24, Right: 24}}, Children: append(body, children...)}}}
}

func botProfileSection(cx *core.Scope, title, caption string, children ...core.Node) core.Node {
	return botProfileSectionAt(cx, nil, title, caption, children...)
}

func botProfileSectionAt(cx *core.Scope, ref *core.Ref, title, caption string, children ...core.Node) core.Node {
	heading := []core.Node{core.Text{Ref: ref, Content: title, Role: core.RoleHeader, Style: core.Style{FontSize: 14, LineHeight: 18, FontWeight: core.FontWeightSemibold}}}
	if caption != "" {
		captionText := toolsCaption(cx, caption)
		captionText.Style = core.Style{FlexShrink: 1, MaxWidth: core.Pct(100)}
		heading = append(heading, captionText)
	}
	return core.View{Style: core.Style{Gap: 14}, Children: append([]core.Node{
		core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, Gap: 8, AlignItems: core.AlignCenter}, Children: heading},
	}, children...)}
}

func botProfilePanel(cx *core.Scope, rows ...core.Node) core.Node {
	colors := ui.ColorsOf(cx)
	children := make([]core.Node, 0, max(0, len(rows)*2-1))
	for i, row := range rows {
		if i > 0 {
			children = append(children, botProfileDivider(cx))
		}
		children = append(children, row)
	}
	return core.View{Style: core.Style{BorderWidth: 1, CornerRadius: 8}, StyleFunc: func() core.Style {
		return core.Style{BorderColor: colors.Border(), Background: colors.Surface()}
	}, Children: children}
}

func botProfileDivider(cx *core.Scope) core.Node {
	return core.View{Style: core.Style{Height: core.Pt(1), FlexShrink: 0}, StyleFunc: func() core.Style {
		return core.Style{Background: ui.ColorsOf(cx).Border()}
	}}
}

func botProfileRow(cx *core.Scope, stack bool, title, hint func() string, action core.Node) core.Node {
	labels := []core.Node{core.Text{ContentFunc: title, Style: core.Style{FontSize: 14, LineHeight: 20, FontWeight: core.FontWeightMedium}}}
	if hint != nil {
		labels = append(labels, core.Text{ContentFunc: hint, StyleFunc: func() core.Style {
			return core.Style{FontSize: 12.5, LineHeight: 19, Color: ui.ColorsOf(cx).Muted()}
		}})
	}
	children := []core.Node{core.View{Style: core.Style{Gap: 1, FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0)}, Children: labels}}
	if action != nil {
		children = append(children, action)
	}
	return core.View{Style: core.Style{Gap: 12, Padding: core.Symmetric(14, 16)}, StyleFunc: func() core.Style {
		width := cx.Env().Window().Width
		if width >= 640 || (!stack && width >= 350) {
			minHeight := core.Points(68)
			if stack {
				minHeight = 70
			}
			return core.Style{Direction: core.Row, AlignItems: core.AlignCenter, MinHeight: core.Pt(minHeight)}
		}
		return core.Style{}
	}, Children: children}
}

func botProfileFieldWidth(cx *core.Scope) core.Style {
	if cx.Env().Window().Width < 640 {
		return core.Style{Width: core.Pct(100)}
	}
	return core.Style{Width: core.Pt(220)}
}

func botProfileCopy(s string) func() string { return func() string { return s } }
