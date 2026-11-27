package main

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

type botListItem struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	ModelID  string `json:"modelId"`
}

func botsPage(cx *core.Scope, current string, selectBot func(id, name string), archivedInitially ...bool) core.Node {
	items := core.NewSignalFunc(cx, []botListItem(nil), slices.Equal)
	archived := core.NewSignal(cx, len(archivedInitially) > 0 && archivedInitially[0])
	loading := core.NewSignal(cx, false)
	loadError := core.NewSignal(cx, "")
	restoring := core.NewSignal(cx, "")
	status := core.NewSignal(cx, "")
	busy := func() bool { return loading.Get() || restoring.Get() != "" }
	load := func(archive bool) {
		if busy() {
			return
		}
		if archived.Get() != archive {
			items.Set(nil)
			status.Set("")
		}
		archived.Set(archive)
		loading.Set(true)
		loadError.Set("")
		path := "/api/bots"
		if archive {
			path = "/api/archived-bots"
		}
		work(cx, func(ctx context.Context) ([]botListItem, error) {
			var result struct {
				Bots []botListItem `json:"bots"`
			}
			err := requestJSON(ctx, http.MethodGet, path, nil, &result)
			return result.Bots, err
		}, func(result []botListItem, err error) {
			loading.Set(false)
			if err != nil {
				loadError.Set("读取 Bot 失败：" + err.Error() + "。请刷新重试。")
				return
			}
			items.Set(result)
		})
	}
	load(archived.Get())

	restore := func(id string) {
		if busy() || !archived.Get() || loadError.Get() != "" {
			return
		}
		restoring.Set(id)
		status.Set("")
		work(cx, func(ctx context.Context) (botListItem, error) {
			var bot botListItem
			err := requestJSON(ctx, http.MethodDelete, botPath(id)+"/archive", nil, &bot)
			if err == nil && bot.ID != id {
				err = errors.New("恢复结果与所选 Bot 不符")
			}
			return bot, err
		}, func(bot botListItem, err error) {
			restoring.Set("")
			if err != nil {
				status.Set("恢复失败：" + err.Error() + "。若连接中断，请先刷新确认结果。")
				return
			}
			load(true)
			selectBot(bot.ID, bot.Name)
		})
	}

	colors := ui.ColorsOf(cx)
	return core.Scroll{TestID: "bots-page", Style: core.Style{FlexGrow: 1, MinHeight: core.Pt(0), Gap: 10, Padding: core.All(16)}, Children: []core.Node{
		core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, Gap: 8, AlignItems: core.AlignCenter}, Children: []core.Node{
			core.Text{ContentFunc: func() string {
				if archived.Get() {
					return "已归档的 Bot"
				}
				return "Bot 名录"
			}, Role: core.RoleHeader, Style: core.Style{FontSize: 22, FontWeight: core.FontWeightSemibold, FlexGrow: 1}},
			ui.Button{TestID: "bots-archive-toggle", TextFunc: func() string {
				if archived.Get() {
					return "返回名录"
				}
				return "已归档"
			}, Variant: ui.ButtonSecondary, Disabled: busy, OnPress: func() { load(!archived.Get()) }},
			ui.Button{Text: "刷新", TestID: "bots-refresh", Variant: ui.ButtonGhost, Busy: loading.Get, Disabled: busy, OnPress: func() { load(archived.Get()) }},
		}},
		core.Show{When: func() bool { return loading.Get() || loadError.Get() != "" || len(items.Get()) == 0 || archived.Get() }, Then: func(*core.Scope) core.Node {
			return core.Text{Role: core.RoleStatus, ContentFunc: func() string {
				switch {
				case loading.Get():
					return "正在读取 Bot…"
				case loadError.Get() != "":
					return loadError.Get()
				case len(items.Get()) == 0 && archived.Get():
					return "没有已归档的 Bot。"
				case len(items.Get()) == 0:
					return "还没有 Bot。请从导航中新建。"
				default:
					return "恢复 Bot 会重新启用它保存的渠道。"
				}
			}}
		}},
		core.Show{When: func() bool { return status.Get() != "" }, Then: func(*core.Scope) core.Node {
			return core.Text{TestID: "bots-write-status", Role: core.RoleStatus, ContentFunc: status.Get}
		}},
		core.Each(items.Get, func(bot botListItem) string { return bot.ID }, func(_ *core.Scope, bot func() botListItem, _ func() int) core.Node {
			id := bot().ID
			buttonStyle := core.Style{MinHeight: core.Pt(44), MinWidth: core.Pt(52)}
			return core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Padding: core.All(8), Gap: 10, BorderWidth: 1, CornerRadius: 10},
				StyleFunc: func() core.Style { return core.Style{BorderColor: colors.Border()} }, Children: []core.Node{
					core.View{Style: core.Style{Width: core.Pt(36), Height: core.Pt(36), CornerRadius: 18, AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter},
						StyleFunc: func() core.Style {
							return core.Style{Background: colors.Surface(), BorderWidth: 1, BorderColor: colors.Border()}
						}, Children: []core.Node{
							core.Text{ContentFunc: func() string {
								for _, r := range strings.TrimSpace(bot().Name) {
									return strings.ToUpper(string(r))
								}
								return "?"
							}, Style: core.Style{FontWeight: core.FontWeightSemibold}},
						}},
					core.View{Style: core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0), Gap: 2}, Children: []core.Node{
						core.Text{ContentFunc: func() string { return bot().Name }, MaxLines: 1, Role: core.RoleHeader,
							Style: core.Style{FontSize: 15, FontWeight: core.FontWeightSemibold}},
						core.Text{ContentFunc: func() string {
							model := botModelLabel(bot().Provider, bot().ModelID)
							if id == current && !archived.Get() {
								return "当前 · " + model
							}
							return model
						}, MaxLines: 1, StyleFunc: func() core.Style { return core.Style{Color: colors.Muted(), FontSize: 12} }},
					}},
					core.Show{When: archived.Get, Then: func(*core.Scope) core.Node {
						return ui.Button{Text: "恢复", TestID: "bots-restore-" + id, Style: buttonStyle, Variant: ui.ButtonSecondary,
							LabelFunc: func() string { return "恢复 " + bot().Name }, Busy: func() bool { return restoring.Get() == id },
							Disabled: func() bool { return busy() || loadError.Get() != "" }, OnPress: func() { restore(id) }}
					}, Else: func(*core.Scope) core.Node {
						return ui.Button{Text: "打开", TestID: "bots-open-" + id, Style: buttonStyle, Variant: ui.ButtonSecondary,
							LabelFunc: func() string { return "打开 " + bot().Name }, Disabled: busy, OnPress: func() { selectBot(id, bot().Name) }}
					}},
				}}
		}),
	}}
}

func botCreateDialog(cx *core.Scope, close func(), onCreated func(id, name string)) core.Node {
	name := core.NewSignal(cx, "")
	composing := core.NewSignal(cx, false)
	busy := core.NewSignal(cx, false)
	status := core.NewSignal(cx, "")
	canCreate := func() bool { return !busy.Get() && !composing.Get() && botsValidName(strings.TrimSpace(name.Get())) }
	cancel := func() {
		if !busy.Get() {
			close()
		}
	}
	create := func() {
		if !canCreate() {
			return
		}
		body := struct {
			Name string `json:"name"`
		}{strings.TrimSpace(name.Get())}
		busy.Set(true)
		status.Set("")
		work(cx, func(ctx context.Context) (botListItem, error) {
			var bot botListItem
			err := requestJSON(ctx, http.MethodPost, "/api/bots", body, &bot)
			if err == nil && bot.ID == "" {
				err = errors.New("创建结果没有 Bot ID")
			}
			return bot, err
		}, func(bot botListItem, err error) {
			if err != nil {
				busy.Set(false)
				status.Set("创建失败：" + err.Error() + "。若连接中断，请先确认是否已经创建。")
				return
			}
			onCreated(bot.ID, bot.Name)
		})
	}
	return ui.Dialog{Title: "新建 Bot", TestID: "bots-create-dialog", Open: func() bool { return true },
		Description: "新 Bot 有独立会话，默认沿用默认 Bot 的可用模型和运行环境。创建后可在 Bot 设置中修改。",
		OnOpenChange: func(open bool, _ base.Reason) {
			if !open {
				cancel()
			}
		},
		NoCloseOnBackdropPress: true,
		Children: []core.Node{
			ui.Input{TestID: "bots-create-name", Label: "Bot 名称", Value: name.Get, ReadonlyFunc: busy.Get,
				OnChange: name.Set, OnCompositionChange: func(c core.Composition) { composing.Set(c.Composing) }},
			core.Text{Content: "名称须为 1 至 40 个字符。"},
			core.Show{When: func() bool { return status.Get() != "" }, Then: func(*core.Scope) core.Node {
				return core.Text{TestID: "bots-create-status", Role: core.RoleStatus, ContentFunc: status.Get}
			}},
		},
		Footer: []core.Node{
			ui.Button{Text: "取消", TestID: "bots-create-cancel", Variant: ui.ButtonSecondary, Disabled: busy.Get, OnPress: cancel},
			ui.Button{Text: "创建 Bot", TestID: "bots-create", Busy: busy.Get, Disabled: func() bool { return !canCreate() }, OnPress: create},
		}}
}

func botsValidName(name string) bool {
	n := utf8.RuneCountInString(name)
	return n >= 1 && n <= 40
}

func botModelLabel(provider, id string) string {
	if id == "" {
		return "未选择模型"
	}
	return provider + " / " + id
}
