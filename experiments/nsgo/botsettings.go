package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode/utf16"

	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

type botSettingsModel struct{ Provider, ID string }

type botSettingsValues struct {
	Name             string `json:"name"`
	Provider         string `json:"provider"`
	ModelID          string `json:"modelId"`
	ThinkingLevel    string `json:"thinkingLevel"`
	FallbackProvider string `json:"fallbackProvider"`
	FallbackModelID  string `json:"fallbackModelId"`
	Instructions     string `json:"instructions"`
	RunnerID         string `json:"runnerId"`
}

func (v botSettingsValues) model() botSettingsModel { return botSettingsModel{v.Provider, v.ModelID} }
func (v botSettingsValues) fallback() botSettingsModel {
	return botSettingsModel{v.FallbackProvider, v.FallbackModelID}
}

type botSettingsCatalogItem struct {
	Provider       string   `json:"provider"`
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Reasoning      bool     `json:"reasoning"`
	ThinkingLevels []string `json:"thinkingLevels"`
}

type botSettingsModelList struct {
	Models []botSettingsCatalogItem `json:"models"`
}

type botSettingsRunner struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type botSettingsRunnerList struct {
	Runners []botSettingsRunner `json:"runners"`
}

var botSettingsThinkingOptions = []ui.Option[string]{
	{Value: "off", Label: "Off"}, {Value: "minimal", Label: "Minimal"}, {Value: "low", Label: "Low"},
	{Value: "medium", Label: "Medium"}, {Value: "high", Label: "High"}, {Value: "xhigh", Label: "Extra high"}, {Value: "max", Label: "Max"},
}

// Keep the value from the start of an edit even when a different row saves first.
type botSettingsDraft struct{ original, text string }

func botSettingsPage(cx *core.Scope, id string, onSaved func(name string)) core.Node {
	return botProfileGeneral(cx, id, nil, onSaved)
}

func botProfileGeneral(cx *core.Scope, id string, navigate func(string), onSaved func(string)) core.Node {
	path := botPath(id)
	current := core.NewSignal(cx, botSettingsValues{})
	name, instructions := core.NewSignal(cx, botSettingsDraft{}), core.NewSignal(cx, botSettingsDraft{})
	composingName, composingInstructions := core.NewSignal(cx, false), core.NewSignal(cx, false)
	saving := core.NewSignal(cx, false)
	savedSettings := toolsNewFeed[botSettingsValues](cx)
	status, source := core.NewSignal(cx, ""), core.NewSignal(cx, "")
	setDraft := func(s *core.Signal[botSettingsDraft], value string) { s.Set(botSettingsDraft{value, value}) }
	load := func() {
		status.Set("")
		savedSettings.run("Loading settings…", "", "Could not load settings.", func(ctx context.Context) (botSettingsValues, error) {
			return toolsGet[botSettingsValues](ctx, path)
		})
	}
	cx.Effect(func() {
		if value := savedSettings.value.Get(); value != nil {
			current.Set(*value)
			setDraft(name, value.Name)
			setDraft(instructions, value.Instructions)
		}
	})
	load()
	locked := func() bool { return !savedSettings.fresh.Get() || savedSettings.busy.Get() || saving.Get() }
	edit := func(s *core.Signal[botSettingsDraft], text string) {
		if locked() {
			return
		}
		d := s.Get()
		d.text = text
		s.Set(d)
	}
	save := func(field string, change func(before, edited *botSettingsValues)) {
		if locked() {
			return
		}
		before, edited := current.Get(), current.Get()
		change(&before, &edited)
		if before == edited {
			return
		}
		saving.Set(true)
		source.Set(field)
		status.Set("Saving…")
		work(cx, func(ctx context.Context) (botSettingsValues, error) {
			return botSettingsSave(ctx, path, before, edited)
		}, func(saved botSettingsValues, err error) {
			saving.Set(false)
			if err != nil {
				status.Set("Could not save " + field + ". " + err.Error())
				return
			}
			cx.Batch(func() {
				current.Set(saved)
				if field == "name" || name.Get().text == name.Get().original {
					setDraft(name, saved.Name)
				}
				if field == "instructions" || instructions.Get().text == instructions.Get().original {
					setDraft(instructions, saved.Instructions)
				}
				status.Set("Settings saved.")
			})
			if onSaved != nil && saved.Name != before.Name {
				onSaved(saved.Name)
			}
		})
	}
	models := toolsNewFeed[botSettingsModelList](cx)
	loadModels := func() {
		models.run("Loading models…", "", "Could not load models.", func(ctx context.Context) (botSettingsModelList, error) {
			return toolsGet[botSettingsModelList](ctx, "/api/models")
		})
	}
	runners := toolsNewFeed[botSettingsRunnerList](cx)
	loadRunners := func() {
		runners.run("Loading Runners…", "", "Could not load Runners.", func(ctx context.Context) (botSettingsRunnerList, error) {
			return toolsGet[botSettingsRunnerList](ctx, "/api/settings/runners")
		})
	}
	loadModels()
	loadRunners()
	noProviders := core.NewSignal(cx, false)
	work(cx, func(ctx context.Context) (bool, error) {
		var settings struct {
			Providers []struct{} `json:"providers"`
		}
		err := requestJSON(ctx, http.MethodGet, "/api/settings", nil, &settings)
		return len(settings.Providers) == 0, err
	}, func(empty bool, err error) {
		noProviders.Set(err == nil && empty)
	})
	catalog := func() []botSettingsCatalogItem {
		if list := models.value.Get(); list != nil {
			return list.Models
		}
		return nil
	}
	modelItem := func(v botSettingsModel) botSettingsCatalogItem {
		list := catalog()
		if i := slices.IndexFunc(list, func(m botSettingsCatalogItem) bool { return m.Provider == v.Provider && m.ID == v.ID }); i >= 0 {
			return list[i]
		}
		return botSettingsCatalogItem{}
	}
	modelLabel := func(v botSettingsModel) string {
		return cmp.Or(modelItem(v).Name, v.ID)
	}
	modelPicker := func(fallback bool) core.Node {
		pick, id, name, choose, change := botSettingsValues.model, "bot-settings-model", "models", "Choose model", "Change model"
		if fallback {
			pick, id, name, choose, change = botSettingsValues.fallback, "bot-settings-fallback", "fallback models", "Choose fallback", "Change fallback"
		}
		caption := func() string {
			if pick(current.Get()).ID == "" {
				return choose
			}
			return change
		}
		return botProfilePicker(cx, id, name, caption, func() core.Points {
			if fallback {
				return 143
			}
			return 133.2
		}, func() bool { return locked() || !models.fresh.Get() || (fallback && current.Get().ModelID == "") }, func() []base.CommandItem {
			selected := pick(current.Get())
			var items []base.CommandItem
			if fallback {
				items = append(items, base.CommandItem{ID: "none", Label: "No fallback model", Checked: selected.ID == ""})
			}
			for _, m := range catalog() {
				label, detail := cmp.Or(m.Name, m.ID), m.ID
				if label == detail {
					detail = ""
				}
				items = append(items, base.CommandItem{ID: m.Provider + "/" + m.ID, Label: label,
					Description: detail, Keywords: []string{m.ID, m.Provider}, Group: m.Provider, Checked: selected == (botSettingsModel{m.Provider, m.ID})})
			}
			return items
		}, func(key string) {
			var m botSettingsModel
			if fallback && key == "none" {
				m = botSettingsModel{}
			} else if provider, id, ok := strings.Cut(key, "/"); ok {
				m = botSettingsModel{provider, id}
			} else {
				return
			}
			if fallback {
				save("fallback model", func(_, v *botSettingsValues) { v.FallbackProvider, v.FallbackModelID = m.Provider, m.ID })
			} else {
				save("model", func(_, v *botSettingsValues) { v.Provider, v.ModelID = m.Provider, m.ID })
			}
		})
	}
	thinking := func() string { return cmp.Or(current.Get().ThinkingLevel, "off") }
	thinkingName := func() string {
		for _, option := range botSettingsThinkingOptions {
			if option.Value == thinking() {
				return option.Label
			}
		}
		return thinking()
	}
	nameValid := func() bool { return botsValidName(strings.TrimSpace(name.Get().text)) }
	canSaveName := func() bool {
		d := name.Get()
		return !locked() && !composingName.Get() && nameValid() && strings.TrimSpace(d.text) != d.original
	}
	saveName := func() {
		if d := name.Get(); canSaveName() {
			save("name", func(before, v *botSettingsValues) { before.Name, v.Name = d.original, strings.TrimSpace(d.text) })
		}
	}
	profile := botProfileSection(cx, "Profile", "A change applies from the next message.",
		botProfilePanel(cx,
			botProfileRow(cx, true, botProfileCopy("Name"), botProfileCopy("Shown in the sidebar and the composer."),
				core.View{Style: core.Style{MaxWidth: core.Pct(100), Gap: 6}, StyleFunc: func() core.Style { return botProfileFieldWidth(cx) }, Children: []core.Node{
					ui.Input{TestID: "bot-settings-name", Label: "Name", Style: core.Style{Height: core.Pt(38)},
						Value: func() string { return name.Get().text }, ReadonlyFunc: locked,
						OnChange:            func(text string) { edit(name, text) },
						OnSubmit:            func(string) { saveName() },
						OnCompositionChange: func(c core.Composition) { composingName.Set(c.Composing) }},
					core.Show{When: func() bool { return name.Get().text != name.Get().original }, Then: func(cx *core.Scope) core.Node {
						return core.View{Style: core.Style{Gap: 6, AlignItems: core.AlignEnd}, Children: []core.Node{
							core.Show{When: func() bool { return !nameValid() }, Then: func(cx *core.Scope) core.Node {
								return toolsCaption(cx, "Use 1 to 40 characters.")
							}},
							toolsButton("bot-settings-save", "Save name", ui.ButtonSecondary, func() bool { return !canSaveName() }, saveName),
						}}
					}},
				}}),
			botProfileRow(cx, false, func() string { return cmp.Or(modelLabel(current.Get().model()), "No model chosen") }, func() string {
				v := current.Get().model()
				if v.ID == "" {
					return "dex cannot answer until you choose one."
				}
				if models.fresh.Get() && modelItem(v).ID == "" {
					return v.Provider + "/" + v.ID + " (unavailable)"
				}
				return v.Provider + "/" + v.ID
			}, modelPicker(false)),
			core.Group{botProfileRow(cx, false, func() string { return "Thinking: " + thinkingName() }, func() string {
				m := modelItem(current.Get().model())
				switch {
				case m.ID == "":
					return "Choose an available model to set the thinking level."
				case m.Reasoning:
					return "The provider uses the nearest level that the model supports."
				default:
					return cmp.Or(m.Name, m.ID) + " does not support thinking."
				}
			}, botProfilePicker(cx, "bot-settings-thinking", "thinking levels", botProfileCopy("Change thinking"), func() core.Points { return 123 },
				func() bool { return locked() || !models.fresh.Get() || !modelItem(current.Get().model()).Reasoning }, func() []base.CommandItem {
					levels := modelItem(current.Get().model()).ThinkingLevels
					var items []base.CommandItem
					for _, o := range botSettingsThinkingOptions {
						if o.Value == "off" || slices.Contains(levels, o.Value) {
							items = append(items, base.CommandItem{ID: o.Value, Label: o.Label, Checked: o.Value == thinking()})
						}
					}
					return items
				}, func(level string) {
					save("thinking level", func(_, v *botSettingsValues) { v.ThinkingLevel = level })
				})),
				core.Show{When: noProviders.Get, Then: func(cx *core.Scope) core.Node {
					colors := ui.ColorsOf(cx)
					return core.Group{botProfileDivider(cx), core.View{Style: core.Style{Padding: core.Symmetric(11, 15)}, Children: []core.Node{
						core.View{Style: core.Style{JustifyContent: core.JustifyCenter, Padding: core.Symmetric(0, 14), MinHeight: core.Pt(42), CornerRadius: 8, BorderWidth: 1},
							StyleFunc: func() core.Style { return core.Style{Background: colors.Hover(), BorderColor: colors.Border()} },
							Children:  []core.Node{toolsCaption(cx, "Add a provider first.")}},
					}}}
				}},
			},
			botProfileRow(cx, false, func() string {
				if v := current.Get().fallback(); v.ID != "" {
					return "Fallback: " + modelLabel(v)
				}
				return "No fallback model"
			}, func() string {
				if v := current.Get().fallback(); v.ID != "" {
					return v.Provider + "/" + v.ID
				}
				return "Used automatically when the primary model is unavailable or fails before answering."
			}, modelPicker(true)),
			botProfileRow(cx, true, botProfileCopy("Default Runner"), func() string {
				if id := current.Get().RunnerID; id != "" {
					if list := runners.value.Get(); list != nil && !slices.ContainsFunc(list.Runners, func(r botSettingsRunner) bool { return r.ID == id }) {
						return "Configured Runner (" + id + ") is missing."
					}
					return "Runs tools and workspaces on the selected Runner."
				}
				return "Runs tools and workspaces on this computer."
			}, core.View{StyleFunc: func() core.Style { return botProfileFieldWidth(cx) }, Children: []core.Node{ui.Select[string]{Label: "Default Runner", TestID: "bot-settings-runner", Style: core.Style{Width: core.Pct(100), Height: core.Pt(38)},
				Value: func() string { return current.Get().RunnerID }, Disabled: func() bool { return locked() || !runners.fresh.Get() },
				OptionsFunc: func() []ui.Option[string] {
					options := []ui.Option[string]{{Label: "Local"}}
					if list := runners.value.Get(); list != nil {
						for _, r := range list.Runners {
							options = append(options, ui.Option[string]{Value: r.ID, Label: r.Name})
						}
					}
					if id := current.Get().RunnerID; id != "" && !slices.ContainsFunc(options, func(o ui.Option[string]) bool { return o.Value == id }) {
						options = append(options, ui.Option[string]{Value: id, Label: "Missing (" + id + ")"})
					}
					return options
				}, OnChange: func(id string) { save("Runner", func(_, v *botSettingsValues) { v.RunnerID = id }) }}}}),
		),
		botProfileRetry(models, "bot-settings-models-reload", loadModels),
		botProfileRetry(runners, "bot-settings-runners-reload", loadRunners),
		core.Show{When: func() bool { return source.Get() != "instructions" && status.Get() != "" }, Then: func(*core.Scope) core.Node {
			return core.Text{TestID: "bot-settings-status", Role: core.RoleStatus, ContentFunc: status.Get}
		}},
	)
	const instructionsLimit = 32000
	instructionsLength := func() int {
		n := 0
		for _, r := range instructions.Get().text {
			n += utf16.RuneLen(r)
		}
		return n
	}
	instructionsDirty := func() bool { d := instructions.Get(); return d.text != d.original }
	canSaveInstructions := func() bool {
		return !locked() && !composingInstructions.Get() && instructionsDirty() && instructionsLength() <= instructionsLimit
	}
	instructionEditor := botProfileSection(cx, "Instructions", "Adds to this bot's system prompt. Changes apply from the next message.",
		botProfilePanel(cx, ui.Input{TestID: "bot-settings-instructions", Label: "Bot instructions", Multiline: true,
			SubmitBehavior: core.SubmitBehaviorNewline, Placeholder: "For example: Reply in the language I write in.",
			Style: core.Style{Height: core.Pt(356), Padding: core.All(16), BorderWidth: 0, CornerRadius: 8}, ReadonlyFunc: locked,
			Value: func() string { return instructions.Get().text }, OnChange: func(text string) { edit(instructions, text) },
			OnCompositionChange: func(c core.Composition) { composingInstructions.Set(c.Composing) }},
			core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, AlignItems: core.AlignCenter, Gap: 8, Padding: core.Symmetric(8, 12)}, Children: []core.Node{
				core.Text{ContentFunc: func() string { return fmt.Sprintf("%d / 32,000", instructionsLength()) }, StyleFunc: func() core.Style {
					return core.Style{FontSize: 11, Color: ui.ColorsOf(cx).Muted(), FlexGrow: 1}
				}},
				ui.Button{TestID: "bot-settings-instructions-discard", Text: "Discard", Label: "Discard draft", Variant: ui.ButtonGhost,
					Disabled: func() bool { return locked() || !instructionsDirty() }, OnPress: func() { setDraft(instructions, current.Get().Instructions) }},
				ui.Button{TestID: "bot-settings-instructions-save", Text: "Save", Label: "Save instructions", Size: ui.SizeSmall,
					Disabled: func() bool { return !canSaveInstructions() }, OnPress: func() {
						if d := instructions.Get(); canSaveInstructions() {
							save("instructions", func(before, v *botSettingsValues) { before.Instructions, v.Instructions = d.original, d.text })
						}
					}},
			}},
		),
		core.Show{When: instructionsDirty, Then: func(cx *core.Scope) core.Node {
			return toolsCaption(cx, "Unsaved draft. Runs use the saved instructions until you save.")
		}},
		core.Show{When: func() bool { return source.Get() == "instructions" && status.Get() != "" }, Then: func(*core.Scope) core.Node {
			return core.Text{TestID: "bot-settings-instructions-status", Role: core.RoleStatus, ContentFunc: status.Get}
		}},
	)
	archive := "The default bot cannot be archived."
	if id != "default" {
		archive = "To archive this bot, ask in Main and approve its confirmation question."
	}
	return botProfileFrame(cx, "bot", "General", func() string {
		return "The name and the model of " + cmp.Or(current.Get().Name, "dex") + ". The timeline, its channels and its automations use them."
	}, navigate, savedSettings.status("bot-settings-load-status"),
		core.Show{When: func() bool { return savedSettings.value.Get() != nil }, Then: func(cx *core.Scope) core.Node {
			return core.View{Style: core.Style{Gap: 28}, Children: []core.Node{profile, instructionEditor,
				botProfileSection(cx, "Archive", "Archived bots keep their sessions and settings.", toolsCaption(cx, archive))}}
		}},
		core.Show{When: func() bool { return !savedSettings.busy.Get() && (!savedSettings.fresh.Get() || status.Get() != "") }, Then: func(*core.Scope) core.Node {
			return toolsButton("bot-settings-reload", "Reload settings", ui.ButtonOutline, saving.Get, load)
		}},
	)
}

func botProfileRetry[T any](feed toolsFeed[T], id string, load func()) core.Node {
	return core.Show{When: func() bool { return !feed.fresh.Get() }, Then: func(*core.Scope) core.Node {
		return core.View{Style: core.Style{Gap: 6}, Children: []core.Node{feed.status(id + "-status"),
			core.Show{When: func() bool { return !feed.busy.Get() }, Then: func(*core.Scope) core.Node {
				return toolsButton(id, "Try again", ui.ButtonOutline, nil, load)
			}},
		}}
	}}
}

func botSettingsSave(ctx context.Context, path string, before, edited botSettingsValues) (botSettingsValues, error) {
	var latest botSettingsValues
	if err := requestJSON(ctx, http.MethodGet, path, nil, &latest); err != nil {
		return latest, err
	}
	// PUT replaces all settings. Check edited fields against the last read and keep the latest values for the rest.
	model, okModel := botSettingsMerge(before.model(), edited.model(), latest.model())
	fallback, okFallback := botSettingsMerge(before.fallback(), edited.fallback(), latest.fallback())
	next := latest
	next.Provider, next.ModelID = model.Provider, model.ID
	next.FallbackProvider, next.FallbackModelID = fallback.Provider, fallback.ID
	var okName, okThinking, okInstructions, okRunner bool
	next.Name, okName = botSettingsMerge(before.Name, edited.Name, latest.Name)
	next.ThinkingLevel, okThinking = botSettingsMerge(before.ThinkingLevel, edited.ThinkingLevel, latest.ThinkingLevel)
	next.Instructions, okInstructions = botSettingsMerge(before.Instructions, edited.Instructions, latest.Instructions)
	next.RunnerID, okRunner = botSettingsMerge(before.RunnerID, edited.RunnerID, latest.RunnerID)
	if !okName || !okModel || !okFallback || !okThinking || !okInstructions || !okRunner {
		return latest, errors.New("Another client changed the same field. Reload settings before you edit again.")
	}
	if next.ModelID == "" && next.FallbackModelID != "" {
		return latest, errors.New("Choose a primary model or clear the fallback model.")
	}
	if next == latest {
		return latest, nil
	}
	var saved botSettingsValues
	err := requestJSON(ctx, http.MethodPut, path, next, &saved)
	return saved, err
}

func botSettingsMerge[T comparable](before, edited, latest T) (T, bool) {
	if before == edited {
		return latest, true
	}
	return edited, latest == before || latest == edited
}
