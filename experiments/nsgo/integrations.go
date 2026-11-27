package main

import (
	"cmp"
	"context"
	"net/http"
	"strings"

	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

type globalIntegrations struct {
	Skills  []globalSkill `json:"skills"`
	Servers []globalMCP   `json:"servers"`
	Errors  []string      `json:"errors"`
}

type globalSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Error       string `json:"error"`
}

type globalMCP struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	State     string `json:"state"`
	OAuth     string `json:"oauth"`
	Scope     string `json:"scope"`
}

// integrationsScreen uses the route's raw query. goQuery replaces only that query.
func integrationsScreen(cx *core.Scope, rawQuery string, goQuery func(string), openSession func(string, int, string)) core.Node {
	q := integrationsReadQuery(rawQuery)
	catalog := core.NewSignal[*globalIntegrations](cx, nil)
	problem, loading := core.NewSignal(cx, ""), core.NewSignal(cx, false)
	load := func() {
		if loading.Get() {
			return
		}
		loading.Set(true)
		problem.Set("")
		work(cx, func(ctx context.Context) (out globalIntegrations, err error) {
			err = requestJSON(ctx, http.MethodGet, "/api/integrations", nil, &out)
			return out, err
		}, func(result globalIntegrations, err error) {
			loading.Set(false)
			if err != nil {
				problem.Set(err.Error())
				return
			}
			catalog.Set(new(result))
		})
	}
	cx.Untrack(load)

	add := core.NewSignal[*integrationsMCPDraft](cx, nil)
	install := core.NewSignal[*integrationsSkillHit](cx, nil)
	closeAdd := func() {
		add.Set(nil)
		if q.values.Get("add") == "mcp" && goQuery != nil {
			goQuery(q.change(""))
		}
	}
	if q.values.Get("add") == "mcp" {
		add.Set(new(integrationsMCPDraft{Auth: "none"}))
	}
	openAdd := func(draft integrationsMCPDraft) { add.Set(new(draft)) }
	var skillsHeading, mcpHeading, skillInput core.Ref
	focusSkill := func() { skillInput.Focus(); skillInput.Reveal(core.RevealOptions{Vertical: core.ScrollAlignCenter}) }
	section := q.values.Get("section")
	openSkill := func() {
		if goQuery != nil && q.values.Get("add") != "skill" {
			goQuery(q.change("skill"))
			return
		}
		focusSkill()
	}
	openBlank := func() {
		if goQuery != nil && q.values.Get("add") != "mcp" {
			goQuery(q.change("mcp"))
			return
		}
		openAdd(integrationsMCPDraft{Auth: "none"})
	}
	return core.Scroll{TestID: "global-integrations-page", Style: ui.FillRest,
		Children: []core.Node{core.View{Style: core.Style{Width: core.Pct(100), MaxWidth: core.Pt(816), Padding: core.Symmetric(32, 24), Gap: 32}, Children: []core.Node{
			core.View{Style: core.Style{Gap: 8}, Children: []core.Node{
				core.Text{Content: "Integrations", Role: core.RoleHeader, Style: core.Style{AlignSelf: core.AlignStart,
					FontSize: 24, FontWeight: ui.Semibold, LineHeight: 30, LetterSpacingEm: -0.015}},
				core.Text{Content: "Find and add skills and MCP servers for all Bots or for one Bot.",
					Style: core.Style{MaxWidth: core.Pt(640), FontSize: 14, LineHeight: 22.75}, StyleFunc: ui.Colored(ui.ColorsOf(cx).Muted)},
			}},
			core.Show{When: func() bool { return catalog.Get() == nil && loading.Get() }, Then: func(cx *core.Scope) core.Node { return integrationsStatus(cx, "Loading…") }},
			core.Show{When: func() bool { return problem.Get() != "" }, Then: func(cx *core.Scope) core.Node {
				return integrationsFailure(cx, "global-integrations-error", "Could not load shared integrations", problem.Get, load)
			}},
			core.Show{When: func() bool { return catalog.Get() != nil }, Then: func(cx *core.Scope) core.Node {
				cx.Post(func() {
					switch {
					case q.values.Get("add") == "skill":
						focusSkill()
					case section == "skills":
						skillsHeading.Reveal(core.RevealOptions{})
					case section == "mcp":
						mcpHeading.Reveal(core.RevealOptions{})
					}
				})
				return core.View{Style: core.Style{Gap: 56}, Children: []core.Node{
					core.Dynamic(func(cx *core.Scope) core.Node {
						if e := catalog.Get().Errors; len(e) > 0 {
							return core.View{Style: core.Style{Gap: 8}, Children: []core.Node{integrationsHeading("Configuration"), integrationsPanel(cx, []core.Node{integrationsItem(integrationsMuted(cx, strings.Join(e, "\n")))})}}
						}
						return core.Group{}
					}),
					integrationsSection(cx, "global-skills", "Skills", "integrations-add-skill", "Add skill", &skillsHeading, openSkill,
						core.Dynamic(func(cx *core.Scope) core.Node {
							var items []core.Node
							for _, s := range catalog.Get().Skills {
								items = append(items, integrationsItem(integrationsNameLine(cx, s.Name, "Shared"), integrationsMuted(cx, cmp.Or(s.Error, s.Description))))
							}
							if len(items) == 0 {
								items = append(items, integrationsEmpty(cx, "No shared skills yet."))
							}
							return integrationsPanel(cx, items)
						}),
						integrationsCatalogSearch(cx, &skillInput, "skill-catalog", "Search skill catalog", "Find a skill, such as PDF", "Load more skills", integrationsSearchSkills,
							func(cx *core.Scope, s integrationsSkillHit) core.Node {
								return integrationsSkillRow(cx, s, func(s integrationsSkillHit) { install.Set(new(s)) })
							}),
					),
					integrationsSection(cx, "global-mcp", "MCP servers", "integrations-add-mcp", "Add MCP server", &mcpHeading, openBlank,
						core.Dynamic(func(cx *core.Scope) core.Node {
							var items []core.Node
							for _, s := range catalog.Get().Servers {
								items = append(items, integrationsServerRow(cx, s))
							}
							if len(items) == 0 {
								items = append(items, integrationsEmpty(cx, "No shared MCP servers yet."))
							}
							return integrationsPanel(cx, items)
						}),
						integrationsOfficialPresets(cx, openAdd),
						integrationsCatalogSearch(cx, nil, "mcp-catalog", "Search MCP Registry", "Find a Streamable HTTP server", "Load more MCP servers", integrationsSearchRegistry,
							func(cx *core.Scope, s integrationsRegistryHit) core.Node {
								return integrationsRegistryRow(cx, s, openAdd)
							}),
					),
				}}
			}},
			core.Dynamic(func(cx *core.Scope) core.Node {
				if d := add.Get(); d != nil {
					return integrationsAddDialog(cx, q, *d, closeAdd, load)
				}
				return core.Group{}
			}),
			core.Dynamic(func(cx *core.Scope) core.Node {
				if s := install.Get(); s != nil {
					return integrationsSkillDialog(cx, q, *s, func() { install.Set(nil) }, openSession)
				}
				return core.Group{}
			}),
		}}},
	}
}

func integrationsSection(cx *core.Scope, id, title, actionID, action string, heading *core.Ref, press func(), children ...core.Node) core.Node {
	colors := ui.ColorsOf(cx)
	// The heading row follows the text baseline. The action extends above it by 4 points.
	head := core.View{Ref: heading, Style: core.Style{Height: core.Pt(22)}, Children: []core.Node{
		core.Text{Content: title, Role: core.RoleHeader, Style: core.Style{FontSize: 14, FontWeight: ui.Semibold, LineHeight: 21,
			Margin: core.Edges{Top: 1}}},
		base.Button{TestID: actionID, Label: action, OnPress: func(core.PressEvent) { press() },
			Style: core.Style{Position: core.Absolute, Top: core.Pt(-4), Right: core.Pt(0), Height: core.Pt(28),
				Direction: core.Row, AlignItems: core.AlignCenter, Gap: 4, Padding: core.Symmetric(0, 11), CornerRadius: 6},
			StyleFunc: func() core.Style {
				return core.Style{Background: colors.Accent(), Color: colors.OnAccent(), FocusVisible: ui.FocusRing(cx, 1),
					Hover: &core.Style{Background: cx.Palette().AccentHover}}
			},
			Children: []core.Node{ui.Icon(lucide.Plus, 14, ui.Inherited), core.Text{Content: action,
				Style: core.Style{FontSize: 13, FontWeight: ui.Medium, LineHeight: 19.5}}},
		},
	}}
	return core.View{TestID: id, Style: core.Style{Gap: 16}, Children: append([]core.Node{head}, children...)}
}

func integrationsLabel(text string) core.Text {
	return core.Text{Content: text, Style: core.Style{FontSize: 14, FontWeight: ui.Medium}}
}

func integrationsHeading(text string) core.Text {
	return core.Text{Content: text, Role: core.RoleHeader, Style: core.Style{FontSize: 18, FontWeight: ui.Semibold}}
}

func integrationsMuted(cx *core.Scope, text string) core.Text {
	return core.Text{Content: text, Style: core.Style{FontSize: 13, LineHeightMultiple: 1.45}, StyleFunc: ui.Colored(ui.ColorsOf(cx).Muted)}
}

func integrationsStatus(cx *core.Scope, text string) core.Text {
	n := integrationsMuted(cx, text)
	n.Role = core.RoleStatus
	return n
}

func integrationsFailure(cx *core.Scope, id, title string, detail func() string, retry func()) core.Node {
	children := []core.Node{integrationsLabel(title), core.Text{ContentFunc: detail, Role: core.RoleStatus, Style: core.Style{FontSize: 13}, StyleFunc: ui.Colored(ui.ColorsOf(cx).Danger)}}
	if retry != nil {
		children = append(children, ui.Button{Text: "Try again", TestID: id + "-retry", Variant: ui.ButtonOutline, Style: core.Style{AlignSelf: core.AlignStart}, OnPress: retry})
	}
	return core.View{TestID: id, Style: core.Style{Gap: 6}, Children: children}
}

func integrationsPanel(cx *core.Scope, items []core.Node) core.Node {
	colors := ui.ColorsOf(cx)
	children := make([]core.Node, 0, 2*len(items))
	for i, item := range items {
		if i != 0 {
			children = append(children, core.View{Style: core.Style{Height: core.Pt(1)}, StyleFunc: ui.Filled(colors.Border)})
		}
		children = append(children, item)
	}
	return core.View{Style: core.Style{CornerRadius: 8}, StyleFunc: func() core.Style {
		return core.Style{Background: colors.Surface(), Shadows: []core.BoxShadow{core.Ring(1, colors.Border())}}
	}, Children: children}
}

func integrationsEmpty(cx *core.Scope, message string) core.Node {
	label := integrationsMuted(cx, message)
	label.Style = core.Style{FontSize: 12.5, LineHeight: 20.3125}
	return core.View{Style: core.Style{Height: core.Pt(48.3125), Padding: core.Symmetric(0, 16), JustifyContent: core.JustifyCenter}, Children: []core.Node{label}}
}

func integrationsItem(items ...core.Node) core.Node {
	return core.View{Style: core.Style{Gap: 5, Padding: core.Symmetric(12, 16)}, Children: items}
}

func integrationsNameLine(cx *core.Scope, name, detail string) core.Node {
	return core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, Gap: 8, AlignItems: core.AlignCenter}, Children: []core.Node{
		core.Text{Content: name, Style: core.Style{FontFamily: core.FontMonospace, FontWeight: ui.Medium, FontSize: 13}}, integrationsMuted(cx, detail),
	}}
}

func globalMCPLabel(server globalMCP) string {
	var labels []string
	if server.Transport == "http" {
		labels = append(labels, "Streamable HTTP")
	} else if server.Transport != "" {
		labels = append(labels, server.Transport)
	}
	switch server.State {
	case "disabled":
		labels = append(labels, "Off")
	case "connected":
		labels = append(labels, "Connected")
	case "failed":
		labels = append(labels, "Failed")
	case "idle":
		labels = append(labels, "Starts with the bot")
	}
	switch server.OAuth {
	case "authorize":
		labels = append(labels, "Not authorized")
	case "reauthorize":
		labels = append(labels, "Authorization required")
	case "authorized":
		labels = append(labels, "Authorized")
	}
	return strings.Join(labels, " · ")
}

func integrationsServerRow(cx *core.Scope, s globalMCP) core.Node {
	rows := []core.Node{integrationsNameLine(cx, s.Name, "Shared"), integrationsMuted(cx, globalMCPLabel(s))}
	if auth := integrationsAuthorize(cx, "", s.Name, s.OAuth, s.State); auth != nil {
		rows = append(rows, auth)
	}
	return integrationsItem(rows...)
}

func integrationsOfficialPresets(cx *core.Scope, open func(integrationsMCPDraft)) core.Node {
	colors := ui.ColorsOf(cx)
	rows := []core.Node{integrationsLabel("Official presets")}
	for _, g := range integrationsPresets() {
		var cards []core.Node
		for _, p := range g.entries {
			cards = append(cards, core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 8, Padding: core.Symmetric(8, 12), CornerRadius: 8, BorderWidth: 1, FlexBasis: core.Pt(212), FlexGrow: 1},
				StyleFunc: func() core.Style { return core.Style{Background: colors.Surface(), BorderColor: colors.Border()} }, Children: []core.Node{
					core.View{Style: core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0), Gap: 2}, Children: []core.Node{integrationsLabel(p.title), integrationsMuted(cx, integrationsPresetAuth(p.draft.Auth))}},
					ui.Button{TestID: "integrations-preset-" + p.draft.Name, Text: "Add", Label: "Add " + p.title, Variant: ui.ButtonOutline, Size: ui.SizeSmall, OnPress: func() { open(p.draft) }},
				}})
		}
		rows = append(rows, core.View{Style: core.Style{Gap: 8}, Children: []core.Node{integrationsMuted(cx, g.name), core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, Gap: 8}, Children: cards}}})
	}
	return core.View{Style: core.Style{Gap: 12}, Children: rows}
}
