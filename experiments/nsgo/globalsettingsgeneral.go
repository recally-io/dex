package main

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

type settingsDefaults struct {
	bot                 botSettingsValues
	models              []botSettingsCatalogItem
	runners             []settingsRunner
	modelsOK, runnersOK bool
}

func settingsGeneralContent(cx *core.Scope, navigate func(string)) core.Node {
	defaultPath := botPath("default")
	f := newSettingsFeed(cx, func(ctx context.Context) (settingsDefaults, error) {
		var d settingsDefaults
		var err error
		var reads sync.WaitGroup
		reads.Go(func() { err = requestJSON(ctx, http.MethodGet, defaultPath, nil, &d.bot) })
		reads.Go(func() {
			var list struct {
				Models []botSettingsCatalogItem `json:"models"`
			}
			e := requestJSON(ctx, http.MethodGet, "/api/models", nil, &list)
			d.models = list.Models
			d.modelsOK = e == nil
		})
		reads.Go(func() {
			list, e := settingsGET[settingsRunnerList]("/api/settings/runners")(ctx)
			d.runners = list.Runners
			d.runnersOK = e == nil
		})
		reads.Wait()
		return d, err
	})
	edit := func(change func(*botSettingsValues)) {
		f.run(func(ctx context.Context) error {
			var latest botSettingsValues
			if err := requestJSON(ctx, http.MethodGet, defaultPath, nil, &latest); err != nil {
				return err
			}
			change(&latest)
			return requestJSON(ctx, http.MethodPut, defaultPath, latest, nil)
		}, "Bot defaults saved.", nil)
	}
	return settingsShow(f, "general", func(cx *core.Scope, d settingsDefaults) core.Node {
		modelOptions := func(selected botSettingsModel, label string) []ui.Option[botSettingsModel] {
			opts := []ui.Option[botSettingsModel]{{Label: label}}
			found := selected.ID == ""
			for _, m := range d.models {
				key := botSettingsModel{m.Provider, m.ID}
				found = found || key == selected
				opts = append(opts, ui.Option[botSettingsModel]{Label: settingsModelName(key), Value: key})
			}
			if !found {
				opts = append(opts, ui.Option[botSettingsModel]{Label: settingsModelName(selected) + " · unavailable", Value: selected})
			}
			return opts
		}
		primary := d.bot.model()
		fallback := d.bot.fallback()
		primaryName, fallbackName := d.bot.ModelID, d.bot.FallbackModelID
		thinking := []ui.Option[string]{{Value: "off", Label: "Off"}}
		reasoning := false
		thinkingHelp := "Choose an available default model to set the thinking level."
		for _, m := range d.models {
			if fallback == (botSettingsModel{m.Provider, m.ID}) {
				fallbackName = m.Name
			}
			if primary != (botSettingsModel{m.Provider, m.ID}) {
				continue
			}
			primaryName = m.Name
			if !m.Reasoning {
				thinkingHelp = m.Name + " does not support thinking."
				continue
			}
			reasoning = true
			thinkingHelp = "New bots start with this reasoning level."
			for _, level := range m.ThinkingLevels {
				if level != "off" {
					thinking = append(thinking, ui.Option[string]{Value: level, Label: settingsThinkingName(level)})
				}
			}
		}
		if !slices.ContainsFunc(thinking, func(o ui.Option[string]) bool { return o.Value == d.bot.ThinkingLevel }) {
			thinking = append(thinking, ui.Option[string]{Value: d.bot.ThinkingLevel, Label: settingsThinkingName(d.bot.ThinkingLevel)})
		}
		runners := []ui.Option[string]{{Value: "", Label: "Local"}}
		selectedRunner := d.bot.RunnerID
		if selectedRunner == "local" {
			selectedRunner = ""
		}
		for _, r := range d.runners {
			runners = append(runners, ui.Option[string]{Value: r.ID, Label: r.Name})
		}
		if selectedRunner != "" && !slices.ContainsFunc(runners, func(o ui.Option[string]) bool { return o.Value == selectedRunner }) {
			runners = append(runners, ui.Option[string]{Value: selectedRunner, Label: "Unavailable: " + selectedRunner})
		}
		primaryTitle, primaryDetail, primaryAction := "No default model", "New bots start without a primary model unless you choose one.", "Choose model"
		if primary.ID != "" {
			primaryTitle, primaryDetail, primaryAction = "Default model: "+primaryName, settingsModelName(primary), "Change model"
		}
		fallbackTitle, fallbackDetail, fallbackAction := "No fallback model", "Used automatically when a bot's primary model is unavailable or fails before answering.", "Choose fallback"
		if fallback.ID != "" {
			fallbackTitle, fallbackDetail, fallbackAction = "Fallback model: "+fallbackName, settingsModelName(fallback), "Change fallback"
		}
		rows := []core.Node{
			settingsDefaultRow(cx, primaryTitle, primaryDetail, primary.ID != "", false, settingsDefaultPicker(cx, "global-default-model", "Default model", primaryAction, 133, true,
				modelOptions(primary, "No default model"), primary, func() bool { return f.busy.Get() || !d.modelsOK }, func(m botSettingsModel) {
					edit(func(b *botSettingsValues) {
						b.Provider, b.ModelID = m.Provider, m.ID
						if m.ID == "" {
							b.FallbackProvider, b.FallbackModelID = "", ""
						}
					})
				})),
			settingsDefaultRow(cx, "Thinking: "+settingsThinkingName(d.bot.ThinkingLevel), thinkingHelp, false, true,
				settingsDefaultPicker(cx, "global-default-thinking", "Default thinking", "Change thinking", 123, false, thinking, d.bot.ThinkingLevel,
					func() bool { return f.busy.Get() || !reasoning }, func(level string) { edit(func(b *botSettingsValues) { b.ThinkingLevel = level }) })),
			settingsDefaultRow(cx, fallbackTitle, fallbackDetail, fallback.ID != "", true, settingsDefaultPicker(cx, "global-default-fallback", "Default fallback model", fallbackAction, 143, true,
				modelOptions(fallback, "No fallback model"), fallback, func() bool { return f.busy.Get() || !d.modelsOK || primary.ID == "" }, func(m botSettingsModel) {
					edit(func(b *botSettingsValues) { b.FallbackProvider, b.FallbackModelID = m.Provider, m.ID })
				})),
			settingsDefaultRow(cx, "Default Runner", "Runs tools and workspaces on this computer.", false, true,
				ui.Select[string]{TestID: "global-default-runner", Label: "Default Runner", Style: core.Style{Width: core.Pt(222), MaxWidth: core.Pct(100), Height: core.Pt(38)}, Options: runners, Value: func() string { return selectedRunner }, Disabled: func() bool { return f.busy.Get() || !d.runnersOK }, OnChange: func(id string) { edit(func(b *botSettingsValues) { b.RunnerID = id }) }},
				ui.Button{Text: "Manage Runners", Variant: ui.ButtonGhost, OnPress: func() { navigate("runners") }}),
		}
		if !d.modelsOK || !d.runnersOK {
			rows = append(rows, globalMuted(cx, "Could not load the model or Runner list. Refresh to try again."))
		}
		colors := ui.ColorsOf(cx)
		return core.View{TestID: "global-defaults", Style: core.Style{Gap: 16}, Children: []core.Node{
			core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, AlignItems: core.AlignBaseline, ColumnGap: 8, RowGap: 4}, Children: []core.Node{
				core.Text{Content: "Bot defaults", Role: core.RoleHeader, Style: core.Style{FontSize: 14, LineHeight: 21, FontWeight: ui.Semibold}},
				core.Text{Content: "The default bot uses them, and a new bot starts with a copy.", Style: core.Style{FontSize: 13, LineHeight: 19.5, MaxWidth: core.Pct(100)}, StyleFunc: ui.Colored(colors.Muted)},
			}},
			core.View{Style: core.Style{CornerRadius: 9}, StyleFunc: func() core.Style {
				return core.Style{Background: colors.Surface(), Shadows: []core.BoxShadow{core.Ring(1, colors.Border())}}
			}, Children: rows},
		}}
	})
}

func settingsDefaultRow(cx *core.Scope, title, detail string, mono, divider bool, controls ...core.Node) core.Node {
	colors := ui.ColorsOf(cx)
	detailStyle := core.Style{FontSize: 12.5, LineHeight: 20.3125}
	if mono {
		detailStyle = core.Style{FontFamily: ui.Mono, FontSize: 11.5, LineHeight: 17}
	}
	text := core.View{Style: core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(72), FlexBasis: core.Pt(72), Gap: 2}, Children: []core.Node{
		core.Text{Content: title, Style: core.Style{FontSize: 14, LineHeight: 21, FontWeight: ui.Medium}},
		core.Text{Content: detail, Style: detailStyle, StyleFunc: ui.Colored(colors.Muted)},
	}}
	style := core.Style{Direction: core.Row, Wrap: core.Wrap, AlignItems: core.AlignCenter, Gap: 12, Padding: core.Symmetric(14, 16)}
	if divider {
		style.Borders.Top = 1
	}
	return core.View{Style: style, StyleFunc: func() core.Style { return core.Style{BorderColor: colors.Border()} }, Children: append([]core.Node{text}, controls...)}
}

func settingsDefaultPicker[T comparable](cx *core.Scope, id, label, text string, width core.Points, searchable bool, options []ui.Option[T], selected T, disabled func() bool, choose func(T)) core.Node {
	open := core.NewSignal(cx, false)
	query := core.NewSignal(cx, "")
	button := ui.Button{TestID: id, Text: text, Label: label, Icon: lucide.ChevronDown, IconSize: 12, Variant: ui.ButtonSecondary,
		Style: core.Style{Width: core.Pt(width), Height: core.Pt(32), FlexShrink: 0}, Disabled: disabled}
	children := []core.Node{}
	if searchable {
		children = append(children, ui.Input{Label: "Search models", Placeholder: "Search models", Value: query.Get, OnChange: query.Set})
	}
	children = append(children, core.Scroll{Style: core.Style{MaxHeight: core.Pt(260), Gap: 2}, Children: []core.Node{
		core.Each(func() []ui.Option[T] {
			term := strings.ToLower(strings.TrimSpace(query.Get()))
			return slices.DeleteFunc(slices.Clone(options), func(o ui.Option[T]) bool { return !strings.Contains(strings.ToLower(o.Label), term) })
		}, func(o ui.Option[T]) T { return o.Value }, func(_ *core.Scope, item func() ui.Option[T], _ func() int) core.Node {
			o := item()
			variant := ui.ButtonGhost
			if o.Value == selected {
				variant = ui.ButtonSecondary
			}
			return ui.Button{Text: o.Label, Variant: variant, OnPress: func() {
				open.Set(false)
				if !disabled() && o.Value != selected {
					choose(o.Value)
				}
			}}
		}),
	}})
	return core.Show{When: disabled, Then: func(*core.Scope) core.Node { return button }, Else: func(*core.Scope) core.Node {
		return ui.Popover{TestID: id, Label: label, Align: core.AlignEnd, Open: open.Get, Trigger: button,
			OnOpenChange: func(next bool, _ base.Reason) {
				if next {
					query.Set("")
				}
				open.Set(next)
			}, Children: children}
	}}
}

func settingsModelName(m botSettingsModel) string {
	if m.ID == "" {
		return "None"
	}
	if m.Provider == "" {
		return m.ID
	}
	return m.Provider + "/" + m.ID
}
func settingsThinkingName(level string) string {
	if name := map[string]string{"off": "Off", "minimal": "Minimal", "low": "Low", "medium": "Medium", "high": "High", "xhigh": "Extra high", "max": "Max"}[level]; name != "" {
		return name
	}
	return level
}
