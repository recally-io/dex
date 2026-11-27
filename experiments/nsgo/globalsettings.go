package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

var settingsSections = []struct{ key, label, title, description string }{
	{"general", "General", "General", "New bots copy these settings from the default bot. Existing bots keep their own settings."},
	{"providers", "Providers", "Providers", "A provider is a saved connection to a model service. Each bot chooses its model from one of them."},
	{"runners", "Runners", "Runners", "Choose where bots run tools: locally, on a machine, or in a sandbox."},
	{"secrets", "Secrets", "Secrets", "Manage shared Vault secrets and 1Password. Set values for one bot in its own Secrets page."},
	{"integrations", "Integrations", "Integrations", "Manage the external sources that send events to Dex. Configure channels and MCP servers in each bot's settings."},
	{"archived", "Archived", "Archived bots", "An archived bot is hidden. Its channels are stopped and its automations are paused. Its sessions, memory and settings stay. Restore a bot to use it again."},
	{"about", "About", "About", "Server status, update channel, and database backups."},
}

func globalSettingsScreen(cx *core.Scope, section string, navigate func(string), openBot func(string)) core.Node {
	active := settingsSections[0]
	for _, tab := range settingsSections {
		if tab.key == section {
			active = tab
			break
		}
	}
	var body core.Node
	switch active.key {
	case "general":
		body = settingsGeneralContent(cx, navigate)
	case "providers":
		body = settingsProviders(cx)
	case "runners":
		body = settingsRunners(cx)
	case "secrets":
		body = settingsSecrets(cx)
	case "integrations":
		body = settingsSources(cx)
	case "archived":
		body = settingsArchived(cx, openBot)
	case "about":
		body = settingsAbout(cx)
	}
	colors := ui.ColorsOf(cx)
	tabs := make([]core.Node, 0, len(settingsSections))
	for _, tab := range settingsSections {
		selected := tab.key == active.key
		ink, weight := colors.Muted, ui.Regular
		if selected {
			ink, weight = colors.Text, ui.Medium
		}
		tabs = append(tabs, base.Button{TestID: "global-settings-tab-" + tab.key, Label: tab.label,
			Role: core.RoleLink, Selected: func() bool { return selected },
			Style: core.Style{FlexShrink: 0, Height: core.Pt(28), Padding: core.Symmetric(0, 11.7), JustifyContent: core.JustifyCenter, CornerRadius: 6},
			StyleFunc: func() core.Style {
				return core.Style{FocusVisible: ui.FocusRing(cx, -2), Selected: &core.Style{Background: colors.Surface(), Shadows: []core.BoxShadow{core.Ring(1, colors.Border())}}}
			}, Children: []core.Node{ui.Text(tab.label, 12.5, weight, ink)},
			OnPress: func(core.PressEvent) { navigate(tab.key) }})
	}
	return core.Scroll{TestID: "global-settings-" + active.key, Style: ui.FillRest, Children: []core.Node{
		core.View{Style: core.Style{Width: core.Pct(100), MaxWidth: core.Pt(816), Padding: core.Symmetric(32, 24)}, Children: []core.Node{
			core.View{Style: core.Style{Gap: 8, Margin: core.Edges{Bottom: 32}}, Children: []core.Node{
				core.Text{Content: "Settings for this computer", Style: core.Style{FontSize: 12, LineHeight: 18}, StyleFunc: ui.Colored(colors.Muted)},
				core.Text{Content: active.title, Role: core.RoleHeader, Style: core.Style{FontSize: 24, LineHeight: 30, LetterSpacing: -0.36, FontWeight: core.FontWeightSemibold, AlignSelf: core.AlignStart, MaxWidth: core.Pct(100)}},
				core.Text{Content: active.description, Style: core.Style{FontSize: 14, LineHeight: 22.75, MaxWidth: core.Pt(640)}, StyleFunc: ui.Colored(colors.Muted)},
			}},
			core.Scroll{TestID: "global-settings-switcher", Horizontal: true, HideIndicator: true, Style: core.Style{AlignSelf: core.AlignStart, MaxWidth: core.Pct(100), Height: core.Pt(32), Padding: core.All(2), Gap: 2, CornerRadius: 8, Margin: core.Edges{Bottom: 24}}, StyleFunc: ui.Filled(colors.Hover), Children: tabs},
			body,
		}},
	}}
}

type settingsFeed[T any] struct {
	cx     *core.Scope
	get    func(context.Context) (T, error)
	data   *core.Signal[*T]
	busy   *core.Signal[bool]
	status *core.Signal[string]
}

func newSettingsFeed[T any](cx *core.Scope, get func(context.Context) (T, error)) *settingsFeed[T] {
	f := &settingsFeed[T]{cx, get, core.NewSignal(cx, (*T)(nil)), core.NewSignal(cx, false), core.NewSignal(cx, "")}
	f.run(nil, "", nil)
	return f
}

func (f *settingsFeed[T]) run(write func(context.Context) error, success string, done func()) {
	if f.busy.Get() {
		return
	}
	f.busy.Set(true)
	f.status.Set("Loading…")
	work(f.cx, func(ctx context.Context) (T, error) {
		if write != nil {
			if err := write(ctx); err != nil {
				var zero T
				return zero, err
			}
		}
		return f.get(ctx)
	}, func(data T, err error) {
		f.busy.Set(false)
		if err != nil {
			f.status.Set(err.Error())
			return
		}
		f.data.Set(new(data))
		f.status.Set(success)
		if done != nil {
			done()
		}
	})
}

func settingsGET[T any](path string) func(context.Context) (T, error) {
	return func(ctx context.Context) (T, error) {
		var value T
		err := requestJSON(ctx, http.MethodGet, path, nil, &value)
		return value, err
	}
}

func settingsShow[T any](f *settingsFeed[T], id string, content func(*core.Scope, T) core.Node) core.Node {
	refresh := ui.Button{Text: "Refresh", TestID: "global-" + id + "-refresh", Variant: ui.ButtonGhost, Busy: f.busy.Get, Style: core.Style{AlignSelf: core.AlignStart}, OnPress: func() { f.run(nil, "", nil) }}
	children := []core.Node{
		core.Show{When: func() bool { return f.status.Get() != "" }, Then: func(*core.Scope) core.Node {
			return core.Text{TestID: "global-" + id + "-status", ContentFunc: f.status.Get, Role: core.RoleStatus}
		}},
		core.Dynamic(func(cx *core.Scope) core.Node {
			if d := f.data.Get(); d != nil {
				return core.Untracked(cx, func() core.Node { return content(cx, *d) })
			}
			return core.Group{}
		}),
	}
	if id == "general" {
		children = append(children, refresh)
	} else {
		children = append([]core.Node{refresh}, children...)
	}
	return core.View{Style: core.Style{Gap: 10}, Children: children}
}

type settingsSource struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Running bool   `json:"running"`
	Error   string `json:"error"`
}

func settingsSources(cx *core.Scope) core.Node {
	const endpoint = "/api/settings/sources"
	type list struct {
		Sources []settingsSource `json:"sources"`
	}
	f := newSettingsFeed(cx, settingsGET[list](endpoint))
	return settingsShow(f, "sources", func(cx *core.Scope, data list) core.Node {
		var rows []core.Node
		pending := false
		for _, s := range data.Sources {
			name := map[string]string{"github": "GitHub", "email": "Email", "webhook": "Webhook"}[s.Name]
			if name == "" {
				name = s.Name
			}
			state := "Not running"
			if s.Running {
				state = "Running"
			}
			if s.Error != "" {
				state = "Failed to start: " + s.Error
			}
			pending = pending || s.Error == "" && s.Enabled != s.Running
			action := "Enable"
			if s.Enabled {
				action = "Disable"
			}
			rows = append(rows, settingsRow(cx, name, state, ui.Button{Text: action, Label: action + " " + name, TestID: "global-source-" + s.Name, Variant: ui.ButtonSecondary, Disabled: f.busy.Get, OnPress: func() {
				f.run(func(ctx context.Context) error {
					return requestJSON(ctx, http.MethodPut, endpoint+"/"+url.PathEscape(s.Name), struct {
						Enabled bool `json:"enabled"`
					}{!s.Enabled}, nil)
				}, "Source setting saved.", nil)
			}}))
		}
		if len(rows) == 0 {
			rows = append(rows, globalMuted(cx, "No sources in this build."))
		}
		if pending {
			rows = append(rows, globalMuted(cx, "Restart Dex to apply source changes."))
		}
		return globalSection(cx, "global-sources", "Sources", "Optional sources that run inside Dex. Changes apply when Dex starts again.", rows)
	})
}

func settingsArchived(cx *core.Scope, openBot func(string)) core.Node {
	type archived struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		At   int64  `json:"archivedAt"`
	}
	type list struct {
		Bots []archived `json:"bots"`
	}
	f := newSettingsFeed(cx, settingsGET[list]("/api/archived-bots"))
	return settingsShow(f, "archived", func(cx *core.Scope, data list) core.Node {
		var rows []core.Node
		for _, b := range data.Bots {
			stamp := ""
			if b.At != 0 {
				stamp = "Archived " + time.UnixMilli(b.At).Local().Format("2 Jan 2006, 15:04")
			}
			rows = append(rows, settingsRow(cx, b.Name, stamp, ui.Button{Text: "Restore", Label: "Restore " + b.Name, TestID: "global-restore-" + b.ID, Variant: ui.ButtonSecondary, Disabled: f.busy.Get, OnPress: func() {
				if f.busy.Get() {
					return
				}
				f.busy.Set(true)
				f.status.Set("Restoring…")
				work(f.cx, func(ctx context.Context) (struct{}, error) {
					return struct{}{}, requestJSON(ctx, http.MethodDelete, botPath(b.ID)+"/archive", nil, nil)
				}, func(_ struct{}, err error) {
					f.busy.Set(false)
					if err != nil {
						f.status.Set(err.Error())
						return
					}
					openBot(b.ID)
				})
			}}))
		}
		if len(rows) == 0 {
			rows = []core.Node{globalRow(cx, "global-archived-empty", "No archived bots", "When the owner approves a bot_delete question, the bot is listed here.")}
		}
		return globalSection(cx, "global-archived-list", "Bots", fmt.Sprintf("%d archived", len(data.Bots)), rows)
	})
}

func settingsRow(cx *core.Scope, title, detail string, controls ...core.Node) core.Node {
	text := core.View{Style: core.Style{FlexGrow: 1, FlexShrink: 1, FlexBasis: core.Pt(220), MinWidth: core.Pt(0), Gap: 3}, Children: []core.Node{core.Text{Content: title, Style: core.Style{FontSize: 14, FontWeight: ui.Medium}}}}
	if detail != "" {
		text.Children = append(text.Children, globalMuted(cx, detail))
	}
	return core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, AlignItems: core.AlignCenter, Gap: 8, Padding: core.Symmetric(5, 3)}, Children: append([]core.Node{text}, controls...)}
}
