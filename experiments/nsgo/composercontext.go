package main

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

type composerSessionSettings struct {
	Provider      string `json:"provider,omitempty"`
	ModelID       string `json:"modelId,omitempty"`
	ThinkingLevel string `json:"thinkingLevel,omitempty"`
}

func (s composerSessionSettings) model() botSettingsModel {
	return botSettingsModel{s.Provider, s.ModelID}
}

type composerRunner struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Default bool   `json:"default"`
}

func (r composerRunner) label() string { return cmp.Or(r.Name, r.ID) }

func (r composerRunner) unavailable() string {
	switch r.Status {
	case "available":
		return ""
	case "offline":
		return "离线"
	default:
		if reason, ok := strings.CutPrefix(r.Status, "unavailable: "); ok {
			return "不可用：" + reason
		}
		return "不可用"
	}
}

type composerCatalog struct {
	bot                         botSettingsValues
	models                      []botSettingsCatalogItem
	runners                     []composerRunner
	botErr, modelErr, runnerErr error
}

func (v composerCatalog) model(key botSettingsModel) (botSettingsCatalogItem, bool) {
	if key.ID == "" {
		return botSettingsCatalogItem{}, false
	}
	for _, m := range v.models {
		if m.Provider == key.Provider && m.ID == key.ID {
			return m, true
		}
	}
	return botSettingsCatalogItem{}, false
}

func (v composerCatalog) primary(own composerSessionSettings) botSettingsModel {
	if own.ModelID != "" {
		return own.model()
	}
	return v.bot.model()
}

func (v composerCatalog) runner(id string) (composerRunner, bool) {
	var matchingDefault composerRunner
	for _, r := range v.runners {
		if id == "" && r.Default {
			return r, true
		}
		if id != "" && r.ID == id {
			if !r.Default {
				return r, true
			}
			matchingDefault = r
		}
	}
	// Dex can list an unavailable default and an explicit Local with the same ID.
	return matchingDefault, matchingDefault.ID != ""
}

func (v composerCatalog) reason(own composerSessionSettings, runnerID string) string {
	if v.botErr != nil || v.modelErr != nil {
		return "无法确认模型是否可用。"
	}
	_, primary := v.model(v.primary(own))
	_, fallback := v.model(v.bot.fallback())
	if !primary && !fallback {
		return "请先选择可用的模型。"
	}
	if v.runnerErr != nil {
		return "无法确认 Runner 是否可用。"
	}
	runner, found := v.runner(runnerID)
	if !found {
		return "选定的 Runner 不在可用目录中。"
	}
	if reason := runner.unavailable(); reason != "" {
		return "Runner「" + runner.label() + "」" + reason
	}
	return ""
}

// Start work on the page or pane, not on a picker row that closes after a press.
func composerContextWork[T any](cx *core.Scope, run func(context.Context) (T, error), done func(T, error)) {
	cx.Post(func() { work(cx, run, done) })
}

func composerLoadCatalog(ctx context.Context, bot string) composerCatalog {
	var snapshot composerCatalog
	var loads sync.WaitGroup
	loads.Go(func() {
		snapshot.botErr = requestJSON(ctx, http.MethodGet, bot, nil, &snapshot.bot)
	})
	loads.Go(func() {
		var result struct {
			Models []botSettingsCatalogItem `json:"models"`
		}
		snapshot.modelErr = requestJSON(ctx, http.MethodGet, "/api/models", nil, &result)
		snapshot.models = result.Models
	})
	loads.Go(func() {
		var result struct {
			Runners []composerRunner `json:"runners"`
		}
		snapshot.runnerErr = requestJSON(ctx, http.MethodGet, bot+"/runners", nil, &result)
		snapshot.runners = result.Runners
	})
	loads.Wait()
	return snapshot
}

type composerSessionConfig struct {
	ID            int
	Settings      composerSessionSettings
	RunnerID, Cwd string
}

func composerConfigOf(s chatSession) composerSessionConfig {
	return composerSessionConfig{s.ID, s.Settings, s.RunnerID, s.Cwd}
}

type composerContext struct {
	cx        *core.Scope
	botID     string
	session   func() chatSession
	started   func() bool
	config    *core.Signal[composerSessionConfig]
	catalog   *core.Signal[*composerCatalog]
	loading   *core.Signal[bool]
	panel     *core.Signal[string]
	saving    *core.Signal[string]
	status    *core.Signal[string]
	runnerTry *core.Signal[*string]
}

func newComposerContext(cx *core.Scope, botID string, session func() chatSession, started func() bool) *composerContext {
	c := &composerContext{
		cx: cx, botID: botID, session: session, started: started,
		config: core.NewSignal(cx, composerSessionConfig{}), catalog: core.NewSignal(cx, (*composerCatalog)(nil)),
		loading: core.NewSignal(cx, false), panel: core.NewSignal(cx, ""), saving: core.NewSignal(cx, ""),
		status: core.NewSignal(cx, ""), runnerTry: core.NewSignal(cx, (*string)(nil)),
	}
	// Busy and other session metadata updates must not overwrite a confirmed save.
	observed := core.NewMemo(cx, func() composerSessionConfig { return composerConfigOf(session()) })
	cx.Effect(func() { c.config.Set(observed.Get()) })
	var poll func()
	poll = func() {
		c.load()
		cx.AfterFunc(10*time.Second, poll)
	}
	cx.Post(poll)
	return c
}

func (c *composerContext) load() {
	if c.loading.Get() {
		return
	}
	c.loading.Set(true)
	bot := botPath(c.botID)
	composerContextWork(c.cx, func(ctx context.Context) (composerCatalog, error) {
		return composerLoadCatalog(ctx, bot), nil
	}, func(value composerCatalog, _ error) {
		c.catalog.Set(new(value))
		c.loading.Set(false)
	})
}

func (c *composerContext) current() composerSessionConfig {
	original := composerConfigOf(c.session())
	if current := c.config.Get(); current.ID == original.ID {
		return current
	}
	return original
}

func (c *composerContext) Reason() string {
	if c.saving.Get() == "runner" {
		return "正在保存 Runner，请稍候。"
	}
	if c.saving.Get() == "runner-check" {
		return "正在读取服务器保存的 Runner…"
	}
	if c.runnerTry.Get() != nil {
		return "Runner 尚未确认保存。请在会话设置中重试或读取已保存的 Runner。"
	}
	if c.saving.Get() != "" {
		return "正在保存会话设置，请稍候。"
	}
	if c.session().ID <= 0 {
		return "正在读取会话…"
	}
	catalog := c.catalog.Get()
	if catalog == nil {
		return "正在确认模型与 Runner…"
	}
	s := c.current()
	return catalog.reason(s.Settings, s.RunnerID)
}

func (c *composerContext) canEditModel() bool {
	s := c.session()
	return s.ID > 0 && s.ReadOnly != nil && (s.writable() || s.Channel == "automation")
}

func (c *composerContext) canEditRunner() bool {
	s := c.session()
	return s.ID > 0 && s.writable() && !c.started() && !s.Busy
}

func (c *composerContext) save(kind string, body any, done func()) {
	if c.saving.Get() != "" || c.session().ID <= 0 {
		return
	}
	id := c.session().ID
	endpoint := chatSessionPath(c.botID, id) + "/" + kind
	c.saving.Set(kind)
	c.status.Set("")
	composerContextWork(c.cx, func(ctx context.Context) (chatSession, error) {
		var saved chatSession
		err := requestJSON(ctx, http.MethodPut, endpoint, body, &saved)
		if err == nil && saved.ID != id {
			err = errors.New("响应没有确认会话设置")
		}
		return saved, err
	}, func(saved chatSession, err error) {
		c.saving.Set("")
		if err != nil {
			c.status.Set("保存未确认：" + err.Error())
			return
		}
		c.config.Set(composerConfigOf(saved))
		c.status.Set("已保存。改动从下一条消息起生效。")
		if done != nil {
			done()
		}
	})
}

func (c *composerContext) chooseRunner(id string) {
	if !c.canEditRunner() || c.saving.Get() != "" {
		return
	}
	catalog := c.catalog.Get()
	if catalog == nil || catalog.runnerErr != nil {
		return
	}
	runner, found := catalog.runner(id)
	if !found || runner.Status != "available" {
		return
	}
	if id == c.current().RunnerID && c.runnerTry.Get() == nil {
		return
	}
	c.runnerTry.Set(new(id))
	c.save("runner", struct {
		RunnerID string `json:"runnerId"`
	}{id}, func() { c.runnerTry.Set(nil) })
}

func (c *composerContext) readSavedRunner() {
	if c.runnerTry.Get() == nil || c.saving.Get() != "" {
		return
	}
	id := c.session().ID
	c.saving.Set("runner-check")
	c.status.Set("")
	composerContextWork(c.cx, func(ctx context.Context) (chatSession, error) {
		return chatGetSession(ctx, c.botID, id)
	}, func(s chatSession, err error) {
		c.saving.Set("")
		if err != nil {
			c.status.Set("读取 Runner 失败：" + err.Error())
			return
		}
		c.config.Set(composerConfigOf(s))
		c.runnerTry.Set(nil)
		c.status.Set("已读取服务器保存的 Runner。")
		c.load()
	})
}

func (c *composerContext) chooseModel(model botSettingsModel) {
	if !c.canEditModel() || c.saving.Get() != "" {
		return
	}
	if model.ID != "" {
		catalog := c.catalog.Get()
		if catalog == nil || catalog.modelErr != nil {
			return
		}
		if _, ok := catalog.model(model); !ok {
			return
		}
	}
	own := c.current().Settings
	if own.model() != model {
		own.Provider, own.ModelID = model.Provider, model.ID
		c.save("settings", own, nil)
	}
}

func (c *composerContext) chooseThinking(level string) {
	if !c.canEditModel() || c.saving.Get() != "" {
		return
	}
	own := c.current().Settings
	if own.ThinkingLevel != level {
		own.ThinkingLevel = level
		c.save("settings", own, nil)
	}
}

func (c *composerContext) Controls() core.Node {
	return core.View{Style: core.Style{MinWidth: core.Pt(0)}, Children: []core.Node{
		ui.Button{Text: "会话设置", TestID: "composer-settings", Variant: ui.ButtonGhost,
			Disabled: func() bool { s := c.session(); return s.ID <= 0 || s.Main },
			OnPress:  func() { c.panel.Set("settings"); c.load() }},
		core.Show{When: func() bool { return c.panel.Get() != "" }, Then: func(cx *core.Scope) core.Node {
			close := func() {
				if c.saving.Get() == "" {
					c.panel.Set("")
				}
			}
			return ui.Dialog{TestID: "composer-settings-dialog", Title: "会话设置", Open: func() bool { return true },
				OnOpenChange: func(open bool, _ base.Reason) {
					if !open {
						close()
					}
				},
				Children: []core.Node{core.Scroll{
					Style: core.Style{FlexShrink: 1, MinHeight: core.Pt(0), Gap: 8},
					StyleFunc: func() core.Style {
						env := cx.Env()
						return core.Style{MaxHeight: core.Pt(max(90, min(450, (env.Window().Height-env.KeyboardHeight())*0.60)))}
					},
					Children: []core.Node{
						core.Dynamic(func(pane *core.Scope) core.Node {
							switch c.panel.Get() {
							case "project":
								return c.projectPane(pane)
							default:
								return c.settingsPane(pane)
							}
						}),
						core.Text{Role: core.RoleStatus, TestID: "composer-settings-status", ContentFunc: c.status.Get},
					},
				}}, Footer: []core.Node{ui.Button{Text: "关闭", TestID: "composer-settings-close", Variant: ui.ButtonSecondary,
					Disabled: func() bool { return c.saving.Get() != "" }, OnPress: close}}}
		}},
	}}
}

func (c *composerContext) settingsPane(cx *core.Scope) core.Node {
	width := core.Style{Width: core.Pct(100)}
	loadingOrError := func() string {
		v := c.catalog.Get()
		if v == nil || c.loading.Get() {
			return "正在确认模型与 Runner…"
		}
		if err := errors.Join(v.botErr, v.modelErr, v.runnerErr); err != nil {
			return "读取失败，可刷新重试：" + strings.ReplaceAll(err.Error(), "\n", "；")
		}
		return ""
	}
	modelOptions := func() []ui.Option[botSettingsModel] {
		return composerModelOptions(c.catalog.Get(), c.current().Settings.model())
	}
	return core.View{Style: core.Style{Gap: 8}, Children: []core.Node{
		core.Show{When: func() bool { return loadingOrError() != "" }, Then: func(*core.Scope) core.Node {
			return core.Text{ContentFunc: loadingOrError, Role: core.RoleStatus}
		}},
		ui.Button{Text: "刷新模型与 Runner", TestID: "composer-context-refresh", Variant: ui.ButtonSecondary, Busy: c.loading.Get, OnPress: c.load},
		core.Text{Content: "模型"},
		ui.Select[botSettingsModel]{Label: "模型", TestID: "composer-model", Style: width, Searchable: true,
			SearchPlaceholder: "搜索模型", Empty: "没有匹配的模型", OptionsFunc: modelOptions,
			Value: func() botSettingsModel { return c.current().Settings.model() }, OnChange: c.chooseModel,
			Disabled: func() bool { return !c.canEditModel() || c.saving.Get() != "" }},
		core.Text{Content: "思考级别"},
		ui.Select[string]{Label: "思考级别", TestID: "composer-thinking", Style: width, OptionsFunc: c.thinkingOptions,
			Value: func() string { return c.current().Settings.ThinkingLevel }, OnChange: c.chooseThinking,
			Disabled: func() bool { return !c.canEditModel() || c.saving.Get() != "" || len(c.thinkingOptions()) == 1 }},
		core.Text{Content: "Runner"},
		ui.Select[string]{Label: "Runner", TestID: "composer-runner", Style: width, OptionsFunc: c.runnerOptions,
			Value: func() string { return c.current().RunnerID }, OnChange: c.chooseRunner,
			Disabled: func() bool {
				v := c.catalog.Get()
				return v == nil || v.runnerErr != nil || !c.canEditRunner() || c.saving.Get() != ""
			}},
		core.Show{When: func() bool { return c.runnerTry.Get() != nil && c.saving.Get() == "" }, Then: func(*core.Scope) core.Node {
			return core.View{Style: core.Style{Gap: 6}, Children: []core.Node{
				core.Text{Role: core.RoleStatus, Content: "Runner 尚未确认保存。请重试或读取服务器保存的 Runner。"},
				ui.Button{Text: "重试保存 Runner", TestID: "composer-runner-retry", Variant: ui.ButtonSecondary,
					Disabled: func() bool { return !c.canEditRunner() }, OnPress: func() {
						if id := c.runnerTry.Get(); id != nil {
							c.chooseRunner(*id)
						}
					}},
				ui.Button{Text: "读取已保存的 Runner", TestID: "composer-runner-check", Variant: ui.ButtonSecondary, OnPress: c.readSavedRunner},
			}}
		}},
		ui.Button{TestID: "composer-project", Variant: ui.ButtonSecondary, TextFunc: func() string {
			return "项目目录 · " + cmp.Or(c.current().Cwd, "Runner 工作区")
		}, Disabled: func() bool { return c.saving.Get() != "" || c.runnerTry.Get() != nil },
			OnPress: func() { c.status.Set(""); c.panel.Set("project") }},
		composerActivityMain(cx, chatSessionPath(c.botID, core.Untracked(cx, c.current).ID), func() bool { return c.session().writable() }),
	}}
}

func composerModelOptions(catalog *composerCatalog, selected botSettingsModel) []ui.Option[botSettingsModel] {
	label := "跟随 Bot"
	if catalog != nil && catalog.bot.ModelID != "" {
		label += "（" + botModelLabel(catalog.bot.Provider, catalog.bot.ModelID) + "）"
	}
	items := []ui.Option[botSettingsModel]{{Label: label}}
	found := selected.ID == ""
	if catalog != nil && catalog.modelErr == nil {
		for _, model := range catalog.models {
			key := botSettingsModel{model.Provider, model.ID}
			found = found || key == selected
			label := botModelLabel(model.Provider, model.ID)
			if model.Name != "" && model.Name != model.ID {
				label = model.Name + " · " + label
			}
			items = append(items, ui.Option[botSettingsModel]{Value: key, Label: label})
		}
	}
	if !found {
		label := "尚未确认"
		if catalog != nil && catalog.modelErr == nil {
			label = "不可用"
		}
		items = append(items, ui.Option[botSettingsModel]{Value: selected, Label: botModelLabel(selected.Provider, selected.ID) + " · " + label, Disabled: func() bool { return true }})
	}
	return items
}

func (c *composerContext) thinkingOptions() []ui.Option[string] {
	catalog := c.catalog.Get()
	own := c.current().Settings
	label := "跟随 Bot"
	var levels []string
	if catalog != nil {
		for _, option := range botSettingsThinkingOptions {
			if option.Value == catalog.bot.ThinkingLevel {
				label += "（" + option.Label + "）"
				break
			}
		}
		if m, ok := catalog.model(catalog.primary(own)); ok && m.Reasoning {
			levels = m.ThinkingLevels
		}
	}
	items := []ui.Option[string]{{Label: label}}
	for _, option := range botSettingsThinkingOptions {
		if slices.Contains(levels, option.Value) {
			items = append(items, option)
		}
	}
	if own.ThinkingLevel != "" && !slices.Contains(levels, own.ThinkingLevel) {
		items = append(items, ui.Option[string]{Value: own.ThinkingLevel, Label: own.ThinkingLevel + " · 不在当前模型支持列表", Disabled: func() bool { return true }})
	}
	return items
}

func (c *composerContext) runnerOptions() []ui.Option[string] {
	catalog := c.catalog.Get()
	choice := c.current().RunnerID
	items := []ui.Option[string]{{Label: "跟随 Bot", Disabled: func() bool { return true }}}
	if catalog != nil && catalog.runnerErr == nil {
		if own, ok := catalog.runner(""); ok {
			items[0] = ui.Option[string]{Label: "跟随 Bot（" + composerRunnerLabel(own) + "）", Disabled: func() bool { return own.Status != "available" }}
		}
		seen := make(map[string]bool, len(catalog.runners))
		for _, runner := range catalog.runners {
			if seen[runner.ID] {
				continue
			}
			seen[runner.ID] = true
			option, _ := catalog.runner(runner.ID)
			items = append(items, ui.Option[string]{Value: option.ID, Label: composerRunnerLabel(option), Disabled: func() bool { return option.Status != "available" }})
		}
		if _, found := catalog.runner(choice); found {
			return items
		}
	}
	if choice != "" {
		items = append(items, ui.Option[string]{Value: choice, Label: choice + " · 未确认可用", Disabled: func() bool { return true }})
	}
	return items
}

func composerRunnerLabel(r composerRunner) string {
	if reason := r.unavailable(); reason != "" {
		return r.label() + " · " + reason
	}
	return r.label()
}
