package main

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

type botSettingsModel struct {
	Provider string
	ID       string
}

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

var botSettingsThinkingOptions = []ui.Option[string]{
	{Value: "off", Label: "关闭"}, {Value: "minimal", Label: "极低"}, {Value: "low", Label: "低"},
	{Value: "medium", Label: "中"}, {Value: "high", Label: "高"}, {Value: "xhigh", Label: "很高"}, {Value: "max", Label: "最高"},
}

func botSettingsPage(cx *core.Scope, id string, onSaved func(name string)) core.Node {
	path := botPath(id)
	base := core.NewSignal(cx, botSettingsValues{})
	draft := core.NewSignal(cx, botSettingsValues{})
	ready := core.NewSignal(cx, false)
	loading := core.NewSignal(cx, false)
	saving := core.NewSignal(cx, false)
	composingName := core.NewSignal(cx, false)
	composingInstructions := core.NewSignal(cx, false)
	loadError := core.NewSignal(cx, "")
	status := core.NewSignal(cx, "")
	catalog := core.NewSignalFunc(cx, []botSettingsCatalogItem(nil), func(_, _ []botSettingsCatalogItem) bool { return false })
	catalogLoading := core.NewSignal(cx, false)
	catalogError := core.NewSignal(cx, "")

	change := func(edit func(*botSettingsValues)) {
		if !saving.Get() && !loading.Get() {
			value := draft.Get()
			edit(&value)
			draft.Set(value)
			status.Set("")
		}
	}
	load := func() {
		if loading.Get() || saving.Get() {
			return
		}
		loading.Set(true)
		loadError.Set("")
		work(cx, func(ctx context.Context) (botSettingsValues, error) {
			var value botSettingsValues
			err := requestJSON(ctx, http.MethodGet, path, nil, &value)
			return value, err
		}, func(value botSettingsValues, err error) {
			loading.Set(false)
			if err != nil {
				loadError.Set("读取设置失败：" + err.Error())
				return
			}
			cx.Batch(func() {
				base.Set(value)
				draft.Set(value)
				ready.Set(true)
				status.Set("")
			})
		})
	}
	loadCatalog := func() {
		if catalogLoading.Get() {
			return
		}
		catalogLoading.Set(true)
		catalogError.Set("")
		work(cx, func(ctx context.Context) ([]botSettingsCatalogItem, error) {
			var values struct {
				Models []botSettingsCatalogItem `json:"models"`
			}
			err := requestJSON(ctx, http.MethodGet, "/api/models", nil, &values)
			return values.Models, err
		}, func(values []botSettingsCatalogItem, err error) {
			catalogLoading.Set(false)
			if err != nil {
				catalogError.Set("读取模型失败：" + err.Error())
				return
			}
			catalog.Set(values)
		})
	}
	load()
	loadCatalog()

	normalized := func() botSettingsValues {
		v := draft.Get()
		v.Name = strings.TrimSpace(v.Name)
		return v
	}
	canSave := func() bool {
		v := normalized()
		return ready.Get() && !loading.Get() && !saving.Get() && !composingName.Get() && !composingInstructions.Get() &&
			v != base.Get() && botsValidName(v.Name) && (v.ModelID != "" || v.FallbackModelID == "")
	}
	save := func() {
		if !canSave() {
			return
		}
		before, edited := base.Get(), normalized()
		saving.Set(true)
		status.Set("")
		work(cx, func(ctx context.Context) (botSettingsValues, error) {
			return botSettingsSave(ctx, path, before, edited)
		}, func(saved botSettingsValues, err error) {
			saving.Set(false)
			if err != nil {
				status.Set("保存失败：" + err.Error())
				return
			}
			cx.Batch(func() {
				base.Set(saved)
				draft.Set(saved)
				status.Set("设置已保存")
			})
			onSaved(saved.Name)
		})
	}

	fieldWidth := core.Style{Width: core.Pct(100)}
	locked := func() bool { return saving.Get() || loading.Get() }
	colors := ui.ColorsOf(cx)
	section := func(title string, nodes ...core.Node) core.Node {
		return core.View{Style: core.Style{Gap: 8, Padding: core.All(12), BorderWidth: 1, CornerRadius: 10},
			StyleFunc: func() core.Style { return core.Style{BorderColor: colors.Border()} },
			Children:  append([]core.Node{core.Text{Content: title, Role: core.RoleHeader, Style: core.Style{FontSize: 16, FontWeight: core.FontWeightSemibold}}}, nodes...)}
	}
	modelPicker := func(label, testID, none string, pick func(botSettingsValues) botSettingsModel, update func(*botSettingsValues, botSettingsModel)) core.Node {
		value := func() botSettingsModel { return pick(draft.Get()) }
		return core.View{Style: core.Style{Gap: 4}, Children: []core.Node{
			core.Text{Content: label},
			ui.Select[botSettingsModel]{Label: label, TestID: testID, Style: fieldWidth, Searchable: true,
				SearchPlaceholder: "搜索模型", Empty: "没有匹配的模型", Value: value, Disabled: locked,
				OnChange: func(model botSettingsModel) { change(func(v *botSettingsValues) { update(v, model) }) },
				OptionsFunc: func() []ui.Option[botSettingsModel] {
					options := []ui.Option[botSettingsModel]{{Label: none}}
					selected := value()
					found := selected.ID == ""
					for _, model := range catalog.Get() {
						key := botSettingsModel{model.Provider, model.ID}
						found = found || key == selected
						name := botModelLabel(model.Provider, model.ID)
						if model.Name != "" && model.Name != model.ID {
							name = model.Name + " · " + name
						}
						options = append(options, ui.Option[botSettingsModel]{Value: key, Label: name})
					}
					if !found {
						options = append(options, ui.Option[botSettingsModel]{Value: selected, Label: botModelLabel(selected.Provider, selected.ID) + " · 不在目录"})
					}
					return options
				}},
		}}
	}

	return core.Scroll{TestID: "bot-settings-page", Style: core.Style{FlexGrow: 1, MinHeight: core.Pt(0), Gap: 14, Padding: core.All(16)}, Children: []core.Node{
		core.Text{Content: "Bot 设置", Role: core.RoleHeader, Style: core.Style{FontSize: 22, FontWeight: core.FontWeightSemibold}},
		core.Text{Role: core.RoleStatus, ContentFunc: func() string {
			if loading.Get() {
				return "正在读取设置…"
			}
			if loadError.Get() != "" {
				return loadError.Get()
			}
			return "改动从下一条消息起生效。"
		}},
		ui.Button{TextFunc: func() string {
			if normalized() != base.Get() {
				return "重新读取（放弃草稿）"
			}
			return "重新读取设置"
		}, TestID: "bot-settings-reload", Variant: ui.ButtonSecondary, Busy: loading.Get, Disabled: saving.Get, OnPress: load},
		core.Show{When: ready.Get, Then: func(*core.Scope) core.Node {
			return core.View{Style: core.Style{Gap: 12}, Children: []core.Node{
				section("资料", ui.Input{TestID: "bot-settings-name", Label: "Bot 名称", Value: func() string { return draft.Get().Name }, ReadonlyFunc: locked,
					OnChange:            func(name string) { change(func(v *botSettingsValues) { v.Name = name }) },
					OnCompositionChange: func(c core.Composition) { composingName.Set(c.Composing) }},
					core.Show{When: func() bool { return !botsValidName(normalized().Name) }, Then: func(*core.Scope) core.Node {
						return core.Text{Content: "名称须为 1 至 40 个字符。"}
					}}),
				section("回复模型",
					modelPicker("主模型", "bot-settings-model", "未选择主模型", botSettingsValues.model,
						func(v *botSettingsValues, m botSettingsModel) { v.Provider, v.ModelID = m.Provider, m.ID }),
					core.Text{Content: "思考级别"},
					ui.Select[string]{Label: "思考级别", TestID: "bot-settings-thinking", Style: fieldWidth, Disabled: locked,
						Value:    func() string { return draft.Get().ThinkingLevel },
						OnChange: func(level string) { change(func(v *botSettingsValues) { v.ThinkingLevel = level }) },
						OptionsFunc: func() []ui.Option[string] {
							v := draft.Get()
							models := catalog.Get()
							i := slices.IndexFunc(models, func(m botSettingsCatalogItem) bool { return m.Provider == v.Provider && m.ID == v.ModelID })
							var supported []string
							if i >= 0 && models[i].Reasoning {
								supported = models[i].ThinkingLevels
							}
							var options []ui.Option[string]
							for _, option := range botSettingsThinkingOptions {
								if option.Value == "off" || slices.Contains(supported, option.Value) {
									options = append(options, option)
								} else if option.Value == v.ThinkingLevel {
									option.Label += "（当前设置）"
									options = append(options, option)
								}
							}
							return options
						}},
					modelPicker("备用模型", "bot-settings-fallback", "不使用备用模型", botSettingsValues.fallback,
						func(v *botSettingsValues, m botSettingsModel) {
							v.FallbackProvider, v.FallbackModelID = m.Provider, m.ID
						}),
					core.Text{Role: core.RoleStatus, ContentFunc: func() string {
						v := draft.Get()
						switch {
						case v.ModelID == "" && v.FallbackModelID != "":
							return "请先选择主模型，或取消备用模型。"
						case catalogLoading.Get():
							return "正在读取可用模型…"
						case catalogError.Get() != "":
							return catalogError.Get()
						case len(catalog.Get()) == 0:
							return "没有可用模型。请配置供应商后刷新。"
						default:
							return "主模型不可用时，Dex 可使用备用模型。"
						}
					}},
					ui.Button{Text: "刷新模型", TestID: "bot-settings-models-reload", Variant: ui.ButtonGhost, Busy: catalogLoading.Get, Disabled: locked, OnPress: loadCatalog}),
				section("指令", core.Text{Content: "为此 Bot 设置行为和回复要求。"},
					ui.Input{TestID: "bot-settings-instructions", Label: "Bot 指令", Multiline: true, SubmitBehavior: core.SubmitBehaviorNewline,
						Style: core.Style{Height: core.Pt(120)}, ReadonlyFunc: locked, Value: func() string { return draft.Get().Instructions },
						OnChange:            func(text string) { change(func(v *botSettingsValues) { v.Instructions = text }) },
						OnCompositionChange: func(c core.Composition) { composingInstructions.Set(c.Composing) }}),
				ui.Button{Text: "保存设置", TestID: "bot-settings-save", Busy: saving.Get, Disabled: func() bool { return !canSave() }, OnPress: save},
				core.Text{TestID: "bot-settings-status", Role: core.RoleStatus, ContentFunc: status.Get},
			}}
		}},
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
	var okName, okThinking, okInstructions bool
	next.Name, okName = botSettingsMerge(before.Name, edited.Name, latest.Name)
	next.ThinkingLevel, okThinking = botSettingsMerge(before.ThinkingLevel, edited.ThinkingLevel, latest.ThinkingLevel)
	next.Instructions, okInstructions = botSettingsMerge(before.Instructions, edited.Instructions, latest.Instructions)
	if !okName || !okModel || !okFallback || !okThinking || !okInstructions {
		return latest, errors.New("其他客户端已修改相同的字段。请重新读取设置后再编辑。")
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
