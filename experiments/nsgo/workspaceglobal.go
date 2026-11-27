package main

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/nsgo-dev/nsgo/core"
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
}

func globalIntegrationsPage(cx *core.Scope) core.Node {
	return globalOverview(cx, "global-integrations", "集成", "查看整个 dex 实例共享的 Skills 和 MCP 服务器。",
		func(ctx context.Context) (out globalIntegrations, err error) {
			err = requestJSON(ctx, http.MethodGet, "/api/integrations", nil, &out)
			return out, err
		}, func(cx *core.Scope, data globalIntegrations) []core.Node {
			var sections []core.Node
			if n := len(data.Errors); n > 0 {
				sections = append(sections, globalSection(cx, "global-integration-warnings", "配置提醒", "",
					[]core.Node{core.Text{Content: fmt.Sprintf("扫描共享 Skills 时发现 %d 条提醒。请在 dex 主机上检查配置。", n), Role: core.RoleStatus}}))
			}
			skills := make([]core.Node, 0, max(1, len(data.Skills)))
			for _, skill := range data.Skills {
				desc := skill.Description
				if skill.Error != "" {
					desc = "此 Skill 的配置无效。"
				}
				skills = append(skills, globalRow(cx, "global-skill-"+skill.Name, skill.Name, desc))
			}
			if len(skills) == 0 {
				skills = append(skills, globalMuted(cx, "暂无共享 Skills。"))
			}
			servers := make([]core.Node, 0, max(1, len(data.Servers)))
			for _, server := range data.Servers {
				servers = append(servers, globalRow(cx, "global-mcp-"+server.Name, server.Name, globalMCPLabel(server)))
			}
			if len(servers) == 0 {
				servers = append(servers, globalMuted(cx, "暂无共享 MCP 服务器。"))
			}
			return append(sections,
				globalSection(cx, "global-skills", "共享 Skills", "", skills),
				globalSection(cx, "global-mcp", "共享 MCP 服务器", "显示保存的声明和授权状态。此页不检查实时连接。", servers))
		}, nil)
}

func globalMCPLabel(server globalMCP) string {
	var labels []string
	if server.Transport != "" {
		labels = append(labels, server.Transport)
	}
	if server.State == "disabled" {
		return strings.Join(append(labels, "已停用"), " · ")
	}
	switch server.OAuth {
	case "authorize":
		labels = append(labels, "待授权")
	case "reauthorize":
		labels = append(labels, "需要重新授权")
	case "authorized":
		labels = append(labels, "已授权")
	}
	return strings.Join(labels, " · ")
}

type globalDefaults struct {
	Provider         string `json:"provider"`
	Model            string `json:"modelId"`
	Thinking         string `json:"thinkingLevel"`
	FallbackProvider string `json:"fallbackProvider"`
	FallbackModel    string `json:"fallbackModelId"`
	RunnerID         string `json:"runnerId"`
}

type globalGeneral struct {
	defaults  globalDefaults
	providers int
	runner    string
}

func globalSettingsPage(cx *core.Scope, openArchived func()) core.Node {
	archived := globalSection(cx, "global-settings-archive-section", "已归档 Bot", "查看已归档的 Bot，选择要恢复的 Bot。",
		[]core.Node{ui.Button{Text: "查看已归档 Bot", TestID: "global-settings-archived", Variant: ui.ButtonOutline,
			Style: core.Style{AlignSelf: core.AlignStart}, OnPress: openArchived}})
	return globalOverview(cx, "global-settings", "通用设置", "新 Bot 会从默认 Bot 复制这些设置。现有 Bot 保留各自的设置。",
		globalReadGeneral, func(cx *core.Scope, data globalGeneral) []core.Node {
			d := data.defaults
			providers := "未配置自定义模型供应商。"
			if data.providers > 0 {
				providers = "已添加 " + strconv.Itoa(data.providers) + " 个模型供应商。"
			}
			return []core.Node{globalSection(cx, "global-settings-defaults", "Bot 默认设置", "",
				[]core.Node{
					globalRow(cx, "global-setting-primary-model", "默认模型", globalModelLabel(d.Provider, d.Model)),
					globalRow(cx, "global-setting-thinking", "推理级别", globalThinkingLabel(d.Thinking)),
					globalRow(cx, "global-setting-fallback-model", "备用模型", globalModelLabel(d.FallbackProvider, d.FallbackModel)),
					globalRow(cx, "global-setting-runner", "默认 Runner", data.runner),
					globalRow(cx, "global-setting-providers", "模型供应商", providers),
				})}
		}, archived)
}

func globalReadGeneral(ctx context.Context) (globalGeneral, error) {
	var result globalGeneral
	if err := requestJSON(ctx, http.MethodGet, botPath("default"), nil, &result.defaults); err != nil {
		return result, err
	}
	var settings struct {
		Providers []struct{} `json:"providers"`
	}
	if err := requestJSON(ctx, http.MethodGet, "/api/settings", nil, &settings); err != nil {
		return result, err
	}
	result.providers = len(settings.Providers)
	if result.defaults.RunnerID == "" || result.defaults.RunnerID == "local" {
		result.runner = "本机（Local）"
		return result, nil
	}
	var runners struct {
		Runners []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"runners"`
	}
	if err := requestJSON(ctx, http.MethodGet, "/api/settings/runners", nil, &runners); err != nil {
		result.runner = "暂时无法确认已选 Runner。请刷新。"
		return result, nil
	}
	result.runner = "找不到已选 Runner：" + result.defaults.RunnerID
	for _, runner := range runners.Runners {
		if runner.ID == result.defaults.RunnerID {
			result.runner = runner.Name
			break
		}
	}
	return result, nil
}

func globalModelLabel(provider, model string) string {
	if model == "" {
		return "未选择"
	}
	if provider == "" {
		return model
	}
	return provider + "/" + model
}

func globalThinkingLabel(level string) string {
	switch level {
	case "off":
		return "关闭"
	case "minimal":
		return "最低"
	case "low":
		return "低"
	case "medium":
		return "中"
	case "high":
		return "高"
	case "xhigh":
		return "很高"
	case "max":
		return "最高"
	case "":
		return "未指定"
	default:
		return level
	}
}

func globalOverview[T any](cx *core.Scope, id, title, caption string, get func(context.Context) (T, error), body func(*core.Scope, T) []core.Node, footer core.Node) core.Node {
	data := core.NewSignal[*T](cx, nil)
	busy := core.NewSignal(cx, false)
	failed := core.NewSignal(cx, false)
	load := func() {
		if busy.Get() {
			return
		}
		busy.Set(true)
		failed.Set(false)
		work(cx, get, func(value T, err error) {
			if err == nil {
				data.Set(new(value))
			}
			failed.Set(err != nil)
			busy.Set(false)
		})
	}
	cx.Untrack(load)
	children := []core.Node{
		core.View{Style: core.Style{Gap: 7}, Children: []core.Node{
			core.Text{Content: title, Role: core.RoleHeader, Style: core.Style{FontSize: 26, FontWeight: core.FontWeightSemibold}},
			globalMuted(cx, caption),
			ui.Button{TestID: id + "-refresh", TextFunc: func() string {
				if failed.Get() {
					return "重试加载"
				}
				return "刷新"
			}, Style: core.Style{AlignSelf: core.AlignStart}, Variant: ui.ButtonOutline, Busy: busy.Get, OnPress: load},
		}},
		core.Show{When: busy.Get, Then: func(*core.Scope) core.Node {
			return core.Text{TestID: id + "-loading", Content: "正在读取…", Role: core.RoleStatus}
		}},
		core.Show{When: failed.Get, Then: func(*core.Scope) core.Node {
			return core.Text{TestID: id + "-error", Content: "读取失败。请重试加载。", Role: core.RoleStatus}
		}},
		core.Dynamic(func(cx *core.Scope) core.Node {
			if value := data.Get(); value != nil {
				return core.Group(body(cx, *value))
			}
			return core.Group{}
		}),
	}
	if footer != nil {
		children = append(children, footer)
	}
	return core.Scroll{TestID: id + "-page", Style: core.Style{FlexGrow: 1, FlexBasis: core.Pt(0), MinHeight: core.Pt(0), Padding: core.All(16)},
		Children: []core.Node{core.View{Style: core.Style{Width: core.Pct(100), MaxWidth: core.Pt(720), AlignSelf: core.AlignCenter, Gap: 18}, Children: children}}}
}

func globalSection(cx *core.Scope, id, title, caption string, rows []core.Node) core.Node {
	colors := ui.ColorsOf(cx)
	children := []core.Node{core.Text{Content: title, Role: core.RoleHeader, Style: core.Style{FontSize: 17, FontWeight: core.FontWeightSemibold}}}
	if caption != "" {
		children = append(children, globalMuted(cx, caption))
	}
	children = append(children, core.View{Style: core.Style{Gap: 10, Padding: core.All(12), BorderWidth: 1, CornerRadius: 9},
		StyleFunc: func() core.Style { return core.Style{BorderColor: colors.Border()} }, Children: rows})
	return core.View{TestID: id, Style: core.Style{Gap: 7}, Children: children}
}

func globalRow(cx *core.Scope, id, label, value string) core.Node {
	children := []core.Node{core.Text{Content: label, Style: core.Style{FontWeight: core.FontWeightSemibold}}}
	if value != "" {
		children = append(children, globalMuted(cx, value))
	}
	return core.View{TestID: id, Style: core.Style{Gap: 4}, Children: children}
}

func globalMuted(cx *core.Scope, value string) core.Text {
	colors := ui.ColorsOf(cx)
	return core.Text{Content: value, StyleFunc: func() core.Style { return core.Style{Color: colors.Muted(), FontSize: 13} }}
}
