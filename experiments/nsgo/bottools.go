package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

func toolsPage(cx *core.Scope, botID string) core.Node {
	base := botPath(botID)
	return core.Scroll{
		TestID: "tools-page",
		Style:  core.Style{FlexGrow: 1, FlexBasis: core.Pt(0), MinHeight: core.Pt(0), Padding: core.All(12), Gap: 12},
		Children: []core.Node{
			core.Text{Content: "工具与连接", Role: core.RoleHeader, Style: core.Style{FontSize: 22, FontWeight: core.FontWeightSemibold}},
			toolsCatalogSection(cx, base+"/integrations"),
			toolsChannelsSection(cx, base+"/channels"),
			toolsVaultSection(cx, base+"/vault"),
		},
	}
}

type toolsFeed[T any] struct {
	cx      *core.Scope
	value   *core.Signal[*T]
	busy    *core.Signal[bool]
	fresh   *core.Signal[bool]
	message *core.Signal[string]
}

func toolsNewFeed[T any](cx *core.Scope) toolsFeed[T] {
	return toolsFeed[T]{cx, core.NewSignal[*T](cx, nil), core.NewSignal(cx, false), core.NewSignal(cx, false), core.NewSignal(cx, "")}
}

func (f toolsFeed[T]) run(wait, success, failure string, load func(context.Context) (T, error)) {
	if f.busy.Get() {
		return
	}
	f.busy.Set(true)
	f.message.Set(wait)
	work(f.cx, load, func(data T, err error) {
		if err == nil {
			f.value.Set(new(data))
			f.message.Set(success)
		} else {
			f.message.Set(failure)
		}
		f.fresh.Set(err == nil)
		f.busy.Set(false)
	})
}

func (f toolsFeed[T]) status(id string) core.Node {
	return core.Show{When: func() bool { return f.message.Get() != "" }, Then: func(*core.Scope) core.Node {
		return core.Text{TestID: id, Role: core.RoleStatus, ContentFunc: f.message.Get}
	}}
}

func toolsGet[T any](ctx context.Context, path string) (out T, err error) {
	err = requestJSON(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

func toolsSection(cx *core.Scope, title string, children ...core.Node) core.Node {
	return core.View{
		Style:     core.Style{Gap: 10, Padding: core.All(12), BorderWidth: 1, CornerRadius: 10},
		StyleFunc: func() core.Style { return core.Style{BorderColor: ui.ColorsOf(cx).Border()} },
		Children: append([]core.Node{core.Text{Content: title, Role: core.RoleHeader,
			Style: core.Style{FontSize: 17, FontWeight: core.FontWeightSemibold}}}, children...),
	}
}

func toolsCaption(cx *core.Scope, text string) core.Text {
	return core.Text{Content: text, StyleFunc: func() core.Style {
		return core.Style{FontSize: 13, Color: ui.ColorsOf(cx).Muted()}
	}}
}

func toolsLabel(text string) core.Node {
	return core.Text{Content: text, Style: core.Style{FontWeight: core.FontWeightSemibold}}
}

func toolsRow(children ...core.Node) core.Node {
	return core.View{Style: core.Style{Gap: 4, Padding: core.Edges{Top: 4, Bottom: 4}}, Children: children}
}

func toolsButton(id, text string, variant ui.ButtonVariant, disabled func() bool, press func()) ui.Button {
	return ui.Button{TestID: id, Text: text, Variant: variant, Disabled: disabled, OnPress: press,
		Style: core.Style{Height: core.Pt(40), AlignSelf: core.AlignStart}}
}

type toolsCatalog struct {
	Skills        []toolsSkill  `json:"skills"`
	Servers       []toolsServer `json:"servers"`
	Warnings      []string      `json:"warnings"`
	Errors        []string      `json:"errors"`
	ReloadPending bool          `json:"reloadPending"`
}

type toolsSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Scope       string `json:"scope"`
	Overrides   bool   `json:"overrides"`
	Error       string `json:"error"`
}

type toolsServer struct {
	Name      string   `json:"name"`
	Scope     string   `json:"scope"`
	State     string   `json:"state"`
	Tools     []string `json:"tools"`
	OAuth     string   `json:"oauth"`
	Overrides bool     `json:"overrides"`
}

func toolsCatalogSection(cx *core.Scope, path string) core.Node {
	feed := toolsNewFeed[toolsCatalog](cx)
	refresh := func() {
		feed.run("正在读取工具…", "", "读取工具失败。请刷新工具。", func(ctx context.Context) (toolsCatalog, error) {
			return toolsGet[toolsCatalog](ctx, path)
		})
	}
	reload := func() {
		feed.run("正在重载工具…", "工具已重载。", "未能确认工具重载。请刷新状态，等运行中的任务结束后再试。", func(ctx context.Context) (out toolsCatalog, err error) {
			err = requestJSON(ctx, http.MethodPost, path+"/reload", nil, &out)
			return out, err
		})
	}
	refresh()
	return toolsSection(cx, "Skills 与 MCP",
		toolsButton("tools-refresh", "刷新工具", ui.ButtonOutline, feed.busy.Get, refresh),
		toolsButton("tools-reload", "重载工具", ui.ButtonSecondary, feed.busy.Get, reload),
		feed.status("tools-status"),
		core.Dynamic(func(cx *core.Scope) core.Node {
			catalog := feed.value.Get()
			if catalog == nil {
				return core.Group{}
			}
			rows := core.Group{}
			if len(catalog.Errors) > 0 {
				rows = append(rows, toolsCaption(cx, "配置错误："+strings.Join(catalog.Errors, "\n")))
			}
			if len(catalog.Warnings) > 0 {
				rows = append(rows, toolsCaption(cx, "配置提醒："+strings.Join(catalog.Warnings, "\n")))
			}
			if catalog.ReloadPending {
				rows = append(rows, toolsCaption(cx, "有更改等待重载。"))
			}
			rows = append(rows, toolsLabel("Skills"))
			valid := 0
			for _, skill := range catalog.Skills {
				if skill.Error != "" {
					rows = append(rows, toolsCaption(cx, skill.Name+" 未生效："+skill.Error))
					continue
				}
				valid++
				rows = append(rows, toolsRow(toolsLabel(skill.Name),
					toolsCaption(cx, toolsScopeLabel(skill.Scope, skill.Overrides)),
					core.Text{Content: skill.Description}))
			}
			if valid == 0 {
				rows = append(rows, toolsCaption(cx, "暂无有效 Skill。"))
			}
			rows = append(rows, toolsLabel("MCP"))
			if len(catalog.Servers) == 0 {
				rows = append(rows, toolsCaption(cx, "暂无 MCP 服务。"))
			}
			for _, server := range catalog.Servers {
				details := toolsScopeLabel(server.Scope, server.Overrides) + " · " + toolsMCPState(server.State)
				switch server.OAuth {
				case "authorize":
					details += " · 待授权"
				case "reauthorize":
					details += " · 需重新授权"
				case "authorized":
					details += " · 已授权"
				}
				row := []core.Node{toolsLabel(server.Name), toolsCaption(cx, details)}
				if len(server.Tools) != 0 {
					row = append(row, core.Text{Content: fmt.Sprintf("%d 个工具：%s", len(server.Tools), strings.Join(server.Tools, "、"))})
				}
				rows = append(rows, toolsRow(row...))
			}
			return rows
		}),
		toolsCaption(cx, "MCP 授权需在 Dex 网页完成。"),
	)
}

func toolsScopeLabel(scope string, overrides bool) string {
	var label string
	switch scope {
	case "dex":
		label = "Dex 共享"
	case "agent":
		label = "Bot 专属"
	case "project":
		label = "工作区"
	default:
		label = "来源未知"
	}
	if overrides {
		label += " · 覆盖同名项"
	}
	return label
}

func toolsMCPState(state string) string {
	switch state {
	case "idle":
		return "未加载"
	case "connected":
		return "已连接"
	case "failed":
		return "连接失败"
	case "disabled":
		return "已停用"
	default:
		return "状态未知"
	}
}

type toolsChannel struct {
	Enabled bool `json:"enabled"`
	Status  struct {
		State string `json:"state"`
	} `json:"status"`
}

type toolsChannels struct {
	Telegram toolsChannel `json:"telegram"`
	Wechat   toolsChannel `json:"wechat"`
	Feishu   struct {
		Enabled bool   `json:"enabled"`
		Paired  bool   `json:"paired"`
		Status  string `json:"status"`
	} `json:"feishu"`
}

func toolsChannelsSection(cx *core.Scope, path string) core.Node {
	feed := toolsNewFeed[toolsChannels](cx)
	confirm := core.NewSignal(cx, "")
	refresh := func() {
		confirm.Set("")
		feed.run("正在读取渠道…", "", "读取渠道失败。请刷新渠道。", func(ctx context.Context) (toolsChannels, error) {
			return toolsGet[toolsChannels](ctx, path)
		})
	}
	canDisconnect := func() bool { return feed.fresh.Get() && !feed.busy.Get() }
	disconnect := func(key, name string) {
		if !canDisconnect() || confirm.Get() != key {
			return
		}
		confirm.Set("")
		feed.run("正在断开"+name+"…", "已断开"+name+"。", "未能确认断开结果。请刷新渠道后再试。", func(ctx context.Context) (toolsChannels, error) {
			if err := requestJSON(ctx, http.MethodDelete, path+"/"+key, nil, nil); err != nil {
				return toolsChannels{}, err
			}
			return toolsGet[toolsChannels](ctx, path)
		})
	}
	refresh()
	return toolsSection(cx, "渠道连接",
		toolsButton("tools-channels-refresh", "刷新渠道", ui.ButtonOutline, feed.busy.Get, refresh),
		feed.status("tools-channels-status"),
		core.Dynamic(func(cx *core.Scope) core.Node {
			channels := feed.value.Get()
			if channels == nil {
				return core.Group{}
			}
			rows := core.Group{}
			for _, channel := range []struct {
				key, name, state string
				enabled          bool
			}{
				{"telegram", "Telegram", channels.Telegram.Status.State, channels.Telegram.Enabled},
				{"feishu", "飞书", channels.Feishu.Status, channels.Feishu.Enabled},
				{"wechat", "微信", channels.Wechat.Status.State, channels.Wechat.Enabled},
			} {
				status := "未启用"
				if channel.enabled {
					status = "已启用 · " + toolsChannelState(channel.key, channel.state, channels.Feishu.Paired)
				}
				badge := toolsCaption(cx, status)
				badge.TestID = "tools-" + channel.key + "-status"
				row := []core.Node{toolsLabel(channel.name), badge}
				if channel.enabled {
					row = append(row, core.Show{When: func() bool { return confirm.Get() == channel.key },
						Then: func(cx *core.Scope) core.Node {
							return toolsRow(toolsCaption(cx, "断开会清除此渠道的凭据。重新启用时需要连接。"),
								toolsButton("tools-"+channel.key+"-confirm", "确认断开"+channel.name, ui.ButtonDanger,
									func() bool { return !canDisconnect() }, func() { disconnect(channel.key, channel.name) }),
								toolsButton("tools-"+channel.key+"-cancel", "取消", ui.ButtonGhost, nil, func() { confirm.Set("") }))
						}, Else: func(*core.Scope) core.Node {
							return toolsButton("tools-"+channel.key+"-disconnect", "断开"+channel.name, ui.ButtonDanger,
								func() bool { return !canDisconnect() }, func() { confirm.Set(channel.key) })
						}})
				}
				rows = append(rows, toolsRow(row...))
			}
			return rows
		}),
		toolsCaption(cx, "连接或重新配对请在 Dex 网页完成。"),
	)
}

func toolsChannelState(key, state string, feishuPaired bool) string {
	switch state {
	case "running":
		if key == "feishu" && !feishuPaired {
			return "待配对"
		}
		return "运行中"
	case "off":
		return "未运行"
	case "error":
		return "连接异常"
	case "needs_login":
		if key == "feishu" {
			return "待配对"
		}
		return "需重新登录"
	default:
		return "状态未知"
	}
}
