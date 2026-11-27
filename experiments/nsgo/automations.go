package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

type automationsSchedule struct {
	Kind    string `json:"kind"`
	Minutes int    `json:"minutes,omitzero"`
	Time    string `json:"time,omitempty"`
	Days    []int  `json:"days,omitempty"`
	At      int64  `json:"at,omitzero"`
}

// Omit notification: Dex keeps the existing route on update.
type automationsInput struct {
	Name            string              `json:"name"`
	Prompt          string              `json:"prompt"`
	Cwd             string              `json:"cwd"`
	Schedule        automationsSchedule `json:"schedule"`
	TimeZone        string              `json:"timeZone"`
	Session         string              `json:"session"`
	TargetSessionID int64               `json:"targetSessionId,omitzero"`
	Enabled         bool                `json:"enabled"`
	RunnerID        string              `json:"runnerId"`
}

type automationsEntry struct {
	automationsInput
	ID       string  `json:"id"`
	NextRuns []int64 `json:"nextRuns"`
}

type automationsRun struct {
	ID               string `json:"id"`
	Status           string `json:"status"`
	Outcome          string `json:"outcome"`
	Summary          string `json:"summary"`
	Reason           string `json:"reason"`
	NotificationNote string `json:"notificationNote"`
	CreatedAt        int64  `json:"createdAt"`
	SessionID        int    `json:"sessionId"`
}

type automationsRunView struct {
	ID, Name string
	Initial  []automationsRun
}

func automationsNewInput(name, prompt, interval string) (automationsInput, error) {
	name, prompt = strings.TrimSpace(name), strings.TrimSpace(prompt)
	if n := utf8.RuneCountInString(name); n < 1 || n > 100 {
		return automationsInput{}, errors.New("名称必须为 1 至 100 个字符")
	}
	if n := utf8.RuneCountInString(prompt); n < 1 || n > 32000 {
		return automationsInput{}, errors.New("任务指令必须为 1 至 32000 个字符")
	}
	minutes, err := strconv.Atoi(strings.TrimSpace(interval))
	if err != nil || minutes < 5 || minutes > 10080 {
		return automationsInput{}, errors.New("运行间隔必须为 5 至 10080 的整数分钟")
	}
	return automationsInput{Name: name, Prompt: prompt, Schedule: automationsSchedule{Kind: "every", Minutes: minutes},
		TimeZone: "UTC", Session: "new", Enabled: false}, nil
}

func automationsPage(cx *core.Scope, botID string, onChat func(id int, title string)) core.Node {
	endpoint := botPath(botID) + "/automations"
	entries := core.NewSignalFunc(cx, []automationsEntry(nil), nil)
	loading := core.NewSignal(cx, false)
	writing := core.NewSignal(cx, false)
	creating := core.NewSignal(cx, false)
	selected := core.NewSignal(cx, (*automationsRunView)(nil))
	listStatus := core.NewSignal(cx, "正在加载自动化…")
	writeTarget := core.NewSignal(cx, "")
	writeStatus := core.NewSignal(cx, "")
	busy := func() bool { return loading.Get() || writing.Get() }
	load := func() {
		if busy() {
			return
		}
		loading.Set(true)
		listStatus.Set("正在加载自动化…")
		work(cx, func(ctx context.Context) ([]automationsEntry, error) {
			var result struct {
				Automations []automationsEntry `json:"automations"`
			}
			err := requestJSON(ctx, http.MethodGet, endpoint, nil, &result)
			return result.Automations, err
		}, func(result []automationsEntry, err error) {
			loading.Set(false)
			if err != nil {
				listStatus.Set("加载自动化失败，可刷新重试：" + err.Error())
				return
			}
			entries.Set(result)
			if len(result) == 0 {
				listStatus.Set("暂无自动化。可新建一个定时任务。")
			} else {
				listStatus.Set("")
			}
		})
	}
	cx.Post(load)

	toggle := func(item automationsEntry) {
		if busy() {
			return
		}
		writing.Set(true)
		writeTarget.Set(item.ID)
		writeStatus.Set("正在更新计划…")
		work(cx, func(ctx context.Context) (automationsEntry, error) {
			path := endpoint + "/" + url.PathEscape(item.ID)
			var current automationsEntry
			// PUT replaces the settings. Read the current task before changing one field.
			if err := requestJSON(ctx, http.MethodGet, path, nil, &current); err != nil {
				return automationsEntry{}, err
			}
			want := !item.Enabled
			if current.Enabled == want {
				return current, nil
			}
			current.Enabled = want
			var saved automationsEntry
			err := requestJSON(ctx, http.MethodPut, path, current.automationsInput, &saved)
			return saved, err
		}, func(saved automationsEntry, err error) {
			writing.Set(false)
			if err != nil {
				writeStatus.Set("无法确认更新结果。请先刷新自动化：" + err.Error())
				return
			}
			rows := slices.Clone(entries.Get())
			if i := slices.IndexFunc(rows, func(row automationsEntry) bool { return row.ID == item.ID }); i >= 0 {
				rows[i] = saved
				entries.Set(rows)
			}
			if saved.Enabled {
				writeStatus.Set("计划已启用。")
			} else {
				writeStatus.Set("计划已暂停。")
			}
		})
	}
	run := func(item automationsEntry) {
		if busy() {
			return
		}
		writing.Set(true)
		writeTarget.Set(item.ID)
		writeStatus.Set("正在请求运行…")
		work(cx, func(ctx context.Context) (automationsRun, error) {
			var result automationsRun
			err := requestJSON(ctx, http.MethodPost, endpoint+"/"+url.PathEscape(item.ID)+"/run", nil, &result)
			return result, err
		}, func(result automationsRun, err error) {
			writing.Set(false)
			if err != nil {
				writeStatus.Set("无法确认运行结果。请先查看运行记录：" + err.Error())
				return
			}
			writeStatus.Set("")
			selected.Set(&automationsRunView{item.ID, item.Name, []automationsRun{result}})
		})
	}

	colors := ui.ColorsOf(cx)
	return core.View{Style: ui.FillRest, Children: []core.Node{
		core.Show{When: func() bool { return !creating.Get() && selected.Get() == nil }, Then: func(*core.Scope) core.Node {
			return core.Scroll{TestID: "automations-list", Style: automationsScrollStyle(), Children: []core.Node{
				automationsHeading("自动化"),
				ui.Button{Text: "新建自动化", TestID: "automations-new", Disabled: busy, OnPress: func() { creating.Set(true) }},
				ui.Button{Text: "刷新自动化", TestID: "automations-refresh", Variant: ui.ButtonSecondary,
					Busy: loading.Get, Disabled: writing.Get, OnPress: func() { writeStatus.Set(""); load() }},
				automationsStatus("automations-status", listStatus.Get),
				automationsStatus("automations-write-status", func() string {
					if writeTarget.Get() == "" {
						return writeStatus.Get()
					}
					return ""
				}),
				core.Each(entries.Get, func(item automationsEntry) string { return item.ID }, func(_ *core.Scope, item func() automationsEntry, _ func() int) core.Node {
					id := item().ID
					return core.View{Style: core.Style{Gap: 6, Padding: core.All(10), BorderWidth: 1, CornerRadius: 8},
						StyleFunc: func() core.Style { return core.Style{BorderColor: colors.Border()} }, Children: []core.Node{
							core.Text{ContentFunc: func() string { return item().Name }, Role: core.RoleHeader, Style: core.Style{FontWeight: core.FontWeightSemibold}},
							core.Text{TestID: "automations-state-" + id, ContentFunc: func() string {
								if item().Enabled {
									return "已启用"
								}
								return "已暂停"
							}},
							core.Text{ContentFunc: func() string { return automationsScheduleText(item()) }, StyleFunc: func() core.Style { return core.Style{Color: colors.Muted()} }},
							ui.Button{TestID: "automations-toggle-" + id, TextFunc: func() string {
								if item().Enabled {
									return "暂停计划"
								}
								return "启用计划"
							}, Variant: ui.ButtonSecondary, Disabled: busy, OnPress: func() { toggle(item()) }},
							ui.Button{Text: "立即运行", TestID: "automations-run-" + id, Variant: ui.ButtonSecondary, Disabled: busy, OnPress: func() { run(item()) }},
							ui.Button{Text: "运行记录", TestID: "automations-history-" + id, Variant: ui.ButtonSecondary, Disabled: busy,
								OnPress: func() { selected.Set(&automationsRunView{ID: id, Name: item().Name}) }},
							automationsStatus("automations-write-status-"+id, func() string {
								if writeTarget.Get() == id {
									return writeStatus.Get()
								}
								return ""
							}),
						}}
				}),
			}}
		}},
		core.Show{When: creating.Get, Then: func(form *core.Scope) core.Node {
			return automationsCreate(form, endpoint, func() { creating.Set(false) }, func(saved automationsEntry) {
				entries.Set(append(slices.Clone(entries.Get()), saved))
				listStatus.Set("")
				writeTarget.Set("")
				writeStatus.Set("已创建。计划已暂停，可先手动运行。")
				creating.Set(false)
			})
		}},
		core.Show{When: func() bool { return selected.Get() != nil }, Then: func(detail *core.Scope) core.Node {
			return automationsHistory(detail, endpoint, *selected.Get(), func() { selected.Set(nil) }, onChat)
		}},
	}}
}

func automationsCreate(cx *core.Scope, endpoint string, back func(), created func(automationsEntry)) core.Node {
	name := core.NewSignal(cx, "")
	prompt := core.NewSignal(cx, "")
	interval := core.NewSignal(cx, "60")
	composing := core.NewSignal(cx, false)
	saving := core.NewSignal(cx, false)
	status := core.NewSignal(cx, "")
	composition := func(value core.Composition) { composing.Set(value.Composing) }
	submit := func() {
		if saving.Get() || composing.Get() {
			return
		}
		input, err := automationsNewInput(name.Get(), prompt.Get(), interval.Get())
		if err != nil {
			status.Set(err.Error())
			return
		}
		saving.Set(true)
		status.Set("正在创建…")
		work(cx, func(ctx context.Context) (automationsEntry, error) {
			var result automationsEntry
			err := requestJSON(ctx, http.MethodPost, endpoint, input, &result)
			return result, err
		}, func(result automationsEntry, err error) {
			saving.Set(false)
			if err != nil {
				status.Set("无法确认创建结果。可返回并刷新列表确认：" + err.Error())
				return
			}
			created(result)
		})
	}
	return core.Scroll{TestID: "automations-form", Style: automationsScrollStyle(), Children: []core.Node{
		ui.Button{Text: "返回自动化列表", TestID: "automations-create-back", Variant: ui.ButtonSecondary, Disabled: saving.Get, OnPress: back},
		automationsHeading("新建自动化"),
		core.Text{Content: "每次运行会打开新会话。创建后计划保持暂停，直到手动启用。"},
		core.Text{Content: "名称"},
		ui.Input{Label: "名称", TestID: "automations-name", Value: name.Get, OnChange: name.Set, OnCompositionChange: composition, ReadonlyFunc: saving.Get},
		core.Text{Content: "任务指令"},
		ui.Input{Label: "任务指令", TestID: "automations-prompt", Value: prompt.Get, OnChange: prompt.Set, OnCompositionChange: composition,
			ReadonlyFunc: saving.Get, Multiline: true, SubmitBehavior: core.SubmitBehaviorNewline, Style: core.Style{Height: core.Pt(96)}},
		core.Text{Content: "运行间隔（分钟，5 至 10080）"},
		ui.Input{Label: "运行间隔（分钟）", TestID: "automations-interval", Value: interval.Get, OnChange: interval.Set,
			KeyboardType: core.KeyboardNumber, OnCompositionChange: composition, ReadonlyFunc: saving.Get},
		ui.Button{Text: "创建自动化", TestID: "automations-submit", Busy: saving.Get, Disabled: composing.Get, OnPress: submit},
		automationsStatus("automations-form-status", status.Get),
	}}
}

func automationsHistory(cx *core.Scope, endpoint string, view automationsRunView, back func(), onChat func(int, string)) core.Node {
	runs := core.NewSignalFunc(cx, view.Initial, slices.Equal)
	loading := core.NewSignal(cx, false)
	status := core.NewSignal(cx, "正在加载运行记录…")
	load := func() {
		if loading.Get() {
			return
		}
		loading.Set(true)
		status.Set("正在加载运行记录…")
		work(cx, func(ctx context.Context) ([]automationsRun, error) {
			var result struct {
				Runs []automationsRun `json:"runs"`
			}
			err := requestJSON(ctx, http.MethodGet, endpoint+"/"+url.PathEscape(view.ID)+"/runs", nil, &result)
			return result.Runs, err
		}, func(result []automationsRun, err error) {
			loading.Set(false)
			if err != nil {
				status.Set("加载运行记录失败，可刷新重试：" + err.Error())
				return
			}
			runs.Set(result)
			if len(result) == 0 {
				status.Set("暂无运行记录。可返回列表手动运行。")
			} else {
				status.Set("")
			}
		})
	}
	cx.Post(load)
	colors := ui.ColorsOf(cx)
	return core.Scroll{TestID: "automations-runs", Style: automationsScrollStyle(), Children: []core.Node{
		ui.Button{Text: "返回自动化列表", TestID: "automations-runs-back", Variant: ui.ButtonSecondary, OnPress: back},
		automationsHeading("运行记录"),
		core.Text{Content: view.Name},
		ui.Button{Text: "刷新记录", TestID: "automations-runs-refresh", Variant: ui.ButtonSecondary, Busy: loading.Get, OnPress: load},
		automationsStatus("automations-runs-status", status.Get),
		core.Each(runs.Get, func(item automationsRun) string { return item.ID }, func(_ *core.Scope, item func() automationsRun, _ func() int) core.Node {
			return core.View{TestID: "automations-record-" + item().ID, Style: core.Style{Gap: 6, Padding: core.All(10), BorderWidth: 1, CornerRadius: 8},
				StyleFunc: func() core.Style { return core.Style{BorderColor: colors.Border()} }, Children: []core.Node{
					core.Text{ContentFunc: func() string { return automationsRunLabel(item()) }, Role: core.RoleHeader, Style: core.Style{FontWeight: core.FontWeightSemibold}},
					core.Text{ContentFunc: func() string { return automationsTime(item().CreatedAt) }},
					automationsStatus("automations-summary-"+item().ID, func() string { return item().Summary }),
					automationsStatus("automations-reason-"+item().ID, func() string { return item().Reason }),
					automationsStatus("automations-notification-"+item().ID, func() string { return item().NotificationNote }),
					core.Show{When: func() bool { return onChat != nil && item().SessionID > 0 }, Then: func(*core.Scope) core.Node {
						return ui.Button{Text: "查看会话", TestID: "automations-chat-" + item().ID, Variant: ui.ButtonSecondary,
							OnPress: func() { onChat(item().SessionID, view.Name) }}
					}},
				}}
		}),
	}}
}

func automationsScrollStyle() core.Style {
	return ui.FillRest.Merge(core.Style{Padding: core.All(12), Gap: 10})
}

func automationsHeading(text string) core.Text {
	return core.Text{Content: text, Role: core.RoleHeader, Style: core.Style{FontSize: 22, FontWeight: core.FontWeightSemibold}}
}

func automationsStatus(id string, text func() string) core.Node {
	return core.Show{When: func() bool { return text() != "" }, Then: func(*core.Scope) core.Node {
		return core.Text{TestID: id, ContentFunc: text, Role: core.RoleStatus}
	}}
}

func automationsTime(millis int64) string {
	return time.UnixMilli(millis).Local().Format("2006-01-02 15:04 MST")
}

func automationsScheduleText(item automationsEntry) string {
	schedule := item.Schedule
	var label string
	switch schedule.Kind {
	case "every":
		label = fmt.Sprintf("每 %d 分钟", schedule.Minutes)
	case "daily":
		label = "每天 " + schedule.Time + "（" + item.TimeZone + "）"
	case "weekly":
		label = fmt.Sprintf("每周 %v %s（%s）", schedule.Days, schedule.Time, item.TimeZone)
	case "once":
		label = "仅一次：" + automationsTime(schedule.At)
	}
	if len(item.NextRuns) > 0 {
		label += "\n下次运行：" + automationsTime(item.NextRuns[0])
	}
	return label
}

func automationsRunLabel(run automationsRun) string {
	switch run.Status {
	case "running":
		return "处理中"
	case "failed":
		return "运行失败"
	case "skipped":
		return "已跳过"
	case "done":
		switch run.Outcome {
		case "changed":
			return "已完成 · 有变更"
		case "unchanged":
			return "已完成 · 无变更"
		default:
			return "已完成"
		}
	default:
		return "未知运行状态"
	}
}
