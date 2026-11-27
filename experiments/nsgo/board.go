package main

import (
	"context"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

const boardTasksPath = "/api/tasks"

type boardTask struct {
	ID       string `json:"id"`
	Key      string `json:"key"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Assignee string `json:"assigneeBotId"`
	Reviewer string `json:"reviewerKind"`
	Revision int64  `json:"revision"`
	Blocked  bool   `json:"blocked"`
}

func boardStatus(status string) string {
	switch status {
	case "backlog":
		return "待排期"
	case "ready":
		return "待开始"
	case "in_progress":
		return "进行中"
	case "in_review":
		return "待评审"
	case "done":
		return "已完成"
	case "cancelled":
		return "已取消"
	default:
		return status
	}
}

func boardPage(cx *core.Scope, botID string, onTask func(key string)) core.Node {
	colors := ui.ColorsOf(cx)
	tasks := core.NewSignalFunc(cx, []boardTask(nil), nil)
	loaded := core.NewSignal(cx, false)
	loading := core.NewSignal(cx, false)
	loadError := core.NewSignal(cx, "")
	creating := core.NewSignal(cx, false)
	saving := core.NewSignal(cx, false)
	createError := core.NewSignal(cx, "")
	title := core.NewSignal(cx, "")
	composing := core.NewSignal(cx, false)

	load := func() {
		if loading.Get() || saving.Get() {
			return
		}
		loading.Set(true)
		loadError.Set("")
		work(cx, func(ctx context.Context) ([]boardTask, error) {
			var result struct {
				Tasks []boardTask `json:"tasks"`
			}
			err := requestJSON(ctx, http.MethodGet, boardTasksPath+"?includeClosed=true", nil, &result)
			return result.Tasks, err
		}, func(result []boardTask, err error) {
			loading.Set(false)
			if err != nil {
				loadError.Set("无法读取任务：" + err.Error())
				return
			}
			tasks.Set(result)
			loaded.Set(true)
		})
	}
	canCreate := func() bool {
		length := utf8.RuneCountInString(strings.TrimSpace(title.Get()))
		return botID != "" && !loading.Get() && !saving.Get() && !composing.Get() && length > 0 && length <= 160
	}
	create := func() {
		if !canCreate() {
			return
		}
		body := struct {
			Title    string `json:"title"`
			Assignee string `json:"assigneeBotId"`
			Status   string `json:"status"`
		}{Title: strings.TrimSpace(title.Get()), Assignee: botID, Status: "ready"}
		saving.Set(true)
		createError.Set("")
		work(cx, func(ctx context.Context) (boardTask, error) {
			var task boardTask
			err := requestJSON(ctx, http.MethodPost, boardTasksPath, body, &task)
			return task, err
		}, func(task boardTask, err error) {
			saving.Set(false)
			if err != nil {
				createError.Set("未能确认创建结果。重试前请刷新列表：" + err.Error())
				return
			}
			title.Set("")
			creating.Set(false)
			onTask(task.Key)
		})
	}

	cx.Untrack(load)
	return core.Scroll{TestID: "board-page", Style: core.Style{FlexGrow: 1, FlexBasis: core.Pt(0), MinHeight: core.Pt(0), Padding: core.All(14), Gap: 12}, Children: []core.Node{
		core.Text{Content: "任务看板", Role: core.RoleHeader, Style: core.Style{FontSize: 22, FontWeight: core.FontWeightSemibold}},
		core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, Gap: 8}, Children: []core.Node{
			ui.Button{TestID: "board-new", Text: "新建任务", Variant: ui.ButtonPrimary,
				Disabled: func() bool { return creating.Get() || botID == "" }, OnPress: func() { creating.Set(true) }},
			ui.Button{TestID: "board-refresh", TextFunc: func() string {
				if loadError.Get() != "" {
					return "重试加载"
				}
				return "刷新任务"
			}, Disabled: func() bool { return loading.Get() || saving.Get() }, OnPress: load},
		}},
		core.Show{When: creating.Get, Then: func(*core.Scope) core.Node {
			return core.View{Style: core.Style{Gap: 8}, Children: []core.Node{
				core.Text{Content: "任务标题", Style: core.Style{FontWeight: core.FontWeightSemibold}},
				core.Text{Content: "负责 Bot：" + botID, StyleFunc: func() core.Style { return core.Style{Color: colors.Muted(), FontSize: 12} }},
				ui.Input{TestID: "board-task-title", Label: "任务标题", Value: title.Get, ReadonlyFunc: saving.Get,
					OnChange:            func(value string) { title.Set(value) },
					OnCompositionChange: func(value core.Composition) { composing.Set(value.Composing) }},
				core.Show{When: func() bool { return utf8.RuneCountInString(strings.TrimSpace(title.Get())) > 160 }, Then: func(*core.Scope) core.Node {
					return core.Text{Content: "任务标题最多 160 个字符。", Role: core.RoleStatus}
				}},
				core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, Gap: 8}, Children: []core.Node{
					ui.Button{TestID: "board-create", Text: "创建并打开", Busy: saving.Get,
						Disabled: func() bool { return !canCreate() }, OnPress: create},
					ui.Button{TestID: "board-close-create", Text: "收起新建", Disabled: saving.Get, OnPress: func() {
						composing.Set(false)
						creating.Set(false)
					}},
				}},
				core.Show{When: func() bool { return createError.Get() != "" }, Then: func(*core.Scope) core.Node {
					return core.Text{TestID: "board-create-error", ContentFunc: createError.Get, Role: core.RoleStatus}
				}},
			}}
		}},
		core.Show{When: loading.Get, Then: func(*core.Scope) core.Node {
			return core.Text{TestID: "board-loading", Content: "正在读取任务…", Role: core.RoleStatus}
		}},
		core.Show{When: func() bool { return loadError.Get() != "" }, Then: func(*core.Scope) core.Node {
			return core.Text{TestID: "board-load-error", ContentFunc: loadError.Get, Role: core.RoleStatus}
		}},
		core.Show{When: loaded.Get, Then: func(*core.Scope) core.Node {
			return core.Text{TestID: "board-ready", Content: "全部 Bot 的任务", Role: core.RoleHeader,
				Style: core.Style{FontSize: 16, FontWeight: core.FontWeightSemibold}}
		}},
		core.Show{When: func() bool { return loaded.Get() && !loading.Get() && loadError.Get() == "" && len(tasks.Get()) == 0 }, Then: func(*core.Scope) core.Node {
			return core.Text{TestID: "board-empty", Content: "暂无任务。可新建任务。"}
		}},
		core.Each(tasks.Get, func(task boardTask) string { return task.Key }, func(_ *core.Scope, task func() boardTask, _ func() int) core.Node {
			return core.View{Style: core.Style{Gap: 6, Padding: core.All(10), BorderWidth: 1, CornerRadius: 8},
				StyleFunc: func() core.Style { return core.Style{BorderColor: colors.Border()} }, Children: []core.Node{
					core.Text{ContentFunc: func() string {
						meta := task().Key + " · " + boardStatus(task().Status)
						if task().Blocked {
							meta += " · 有阻塞"
						}
						return meta + " · " + task().Assignee
					}, StyleFunc: func() core.Style { return core.Style{Color: colors.Muted(), FontSize: 12} }},
					core.Text{ContentFunc: func() string { return task().Title }, Style: core.Style{FontSize: 16, FontWeight: core.FontWeightSemibold}},
					ui.Button{TestID: "board-open-" + task().Key, Text: "打开任务", Style: core.Style{AlignSelf: core.AlignStart},
						LabelFunc: func() string { return "打开任务 " + task().Key + " " + task().Title },
						Disabled:  saving.Get, OnPress: func() { onTask(task().Key) }},
				}}
		}),
	}}
}
