package main

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

type composerActivityMonitor struct {
	ID        string `json:"id"`
	Prompt    string `json:"prompt"`
	State     string `json:"state"`
	Mode      string `json:"mode"`
	Outcome   string `json:"outcome"`
	Runs      int    `json:"runs"`
	MaxRuns   int    `json:"maxRuns"`
	DueAt     int64  `json:"dueAt"`
	ExpiresAt int64  `json:"expiresAt"`
}

type composerActivityDelivery struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Peer        string `json:"peer"`
	Topic       string `json:"topicKey"`
	Body        string `json:"body"`
	Reason      string `json:"reason"`
	Status      string `json:"status"`
	Error       string `json:"error"`
	CreatedAt   int64  `json:"createdAt"`
	DeliveredAt int64  `json:"deliveredAt"`
}

type composerActivityState struct {
	monitors   []composerActivityMonitor
	messages   []composerActivityDelivery
	monitorErr error
	outboxErr  error
	monitorsOK bool
	outboxOK   bool
}

func composerActivity(cx *core.Scope, botID string, session func() chatSession, actions ...func(closePanel func()) core.Node) core.Node {
	mainID := core.NewMemo(cx, func() int {
		if s := session(); s.Main && s.ID > 0 {
			return s.ID
		}
		return 0
	})
	return core.Dynamic(func(cx *core.Scope) core.Node {
		id := mainID.Get()
		if id == 0 {
			return core.Group{}
		}
		var node core.Node
		cx.Untrack(func() {
			node = composerActivityMain(cx, chatSessionPath(botID, id), func() bool {
				s := session()
				return s.ID == id && s.Main && s.writable()
			}, actions...)
		})
		return node
	})
}

func composerActivityMain(cx *core.Scope, path string, writable func() bool, actions ...func(closePanel func()) core.Node) core.Node {
	open := core.NewSignal(cx, false)
	loading := core.NewSignal(cx, false)
	cancelling := core.NewSignal(cx, "")
	cancelNotice := core.NewSignal(cx, "")
	data := core.NewSignalFunc(cx, composerActivityState{}, nil)
	busy := func() bool { return loading.Get() || cancelling.Get() != "" }
	refresh := func() {
		if busy() {
			return
		}
		loading.Set(true)
		work(cx, func(ctx context.Context) (next composerActivityState, _ error) {
			var group sync.WaitGroup
			group.Go(func() {
				var result struct {
					Monitors []composerActivityMonitor `json:"monitors"`
				}
				next.monitorErr = requestJSON(ctx, http.MethodGet, path+"/monitors", nil, &result)
				next.monitors, next.monitorsOK = result.Monitors, next.monitorErr == nil
			})
			group.Go(func() {
				var result struct {
					Messages []composerActivityDelivery `json:"messages"`
				}
				next.outboxErr = requestJSON(ctx, http.MethodGet, path+"/outbox", nil, &result)
				next.messages, next.outboxOK = result.Messages, next.outboxErr == nil
			})
			group.Wait()
			return next, nil
		}, func(next composerActivityState, _ error) {
			prior := data.Get()
			if !next.monitorsOK {
				next.monitors, next.monitorsOK = prior.monitors, prior.monitorsOK
			}
			if !next.outboxOK {
				next.messages, next.outboxOK = prior.messages, prior.outboxOK
			}
			data.Set(next)
			loading.Set(false)
		})
	}
	cancel := func(m composerActivityMonitor) {
		if busy() || !writable() || m.State != "scheduled" || data.Get().monitorErr != nil {
			return
		}
		cancelling.Set(m.ID)
		cancelNotice.Set("")
		work(cx, func(ctx context.Context) (updated composerActivityMonitor, err error) {
			err = requestJSON(ctx, http.MethodDelete, path+"/monitors/"+url.PathEscape(m.ID), nil, &updated)
			return updated, err
		}, func(updated composerActivityMonitor, err error) {
			cancelling.Set("")
			if err != nil {
				cancelNotice.Set("未能确认取消结果，正在刷新监控：" + err.Error())
			} else {
				latest := data.Get()
				if idx := slices.IndexFunc(latest.monitors, func(current composerActivityMonitor) bool { return current.ID == updated.ID }); idx >= 0 {
					latest.monitors = slices.Clone(latest.monitors)
					latest.monitors[idx] = updated
					data.Set(latest)
				}
				if updated.State == "cancelled" {
					cancelNotice.Set("监控已取消。")
				} else {
					cancelNotice.Set("监控状态已变化，正在刷新。")
				}
			}
			refresh()
		})
	}
	setOpen := func(next bool) {
		open.Set(next)
		if next {
			refresh()
		}
	}
	cx.Untrack(refresh)
	cx.Effect(func() {
		interval := 30 * time.Second
		if open.Get() {
			interval = 5 * time.Second
		}
		var stop func() bool
		var schedule func()
		schedule = func() {
			stop = cx.AfterFunc(interval, func() {
				refresh()
				schedule()
			})
		}
		schedule()
		cx.Cleanup(func() { stop() })
	})

	counts := core.NewMemo(cx, func() (counts [3]int) {
		value := data.Get()
		for _, monitor := range value.monitors {
			if monitor.State == "scheduled" {
				counts[0]++
			}
		}
		for _, message := range value.messages {
			if message.Status == "failed" || message.Error != "" {
				counts[1]++
			} else if message.Status == "pending" {
				counts[2]++
			}
		}
		return counts
	})
	chrome := ui.ColorsOf(cx)
	label := func() string {
		c := counts.Get()
		return fmt.Sprintf("Session activity: %d scheduled monitors, %d delivery issues, %d pending deliveries", c[0], c[1], c[2])
	}
	triggerStyle := core.Style{Height: core.Pt(28), Padding: core.Symmetric(0, 7), CornerRadius: 6,
		Direction: core.Row, AlignItems: core.AlignCenter}
	triggerLook := func() core.Style {
		return core.Style{Hover: &core.Style{Background: chrome.Hover()}, FocusVisible: ui.FocusRing(cx, 2)}
	}
	face := func() core.Node {
		return core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 6}, Children: []core.Node{
			ui.Icon(lucide.Clock, 14, chrome.Muted),
			core.Text{ContentFunc: func() string {
				label, c := "Activity", counts.Get()
				if c[0] > 0 {
					label += fmt.Sprintf(" %d", c[0])
				}
				if c[1] > 0 {
					label += " !"
				} else if c[2] > 0 {
					label += " •"
				}
				return label
			}, Style: core.Style{FontSize: 13, FontWeight: core.FontWeightMedium, LineHeight: 19.5}, StyleFunc: ui.Colored(chrome.Muted)},
		}}
	}

	panel := func(cx *core.Scope) []core.Node {
		colors := ui.ColorsOf(cx)
		nodes := []core.Node{
			core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, Gap: 8}, Children: []core.Node{
				ui.Button{TestID: "composer-activity-refresh", Text: "刷新活动", Variant: ui.ButtonOutline,
					Disabled: busy, OnPress: refresh},
				ui.Button{TestID: "composer-activity-close", Text: "关闭", Variant: ui.ButtonGhost, OnPress: func() { setOpen(false) }},
			}},
		}
		for _, action := range actions {
			nodes = append(nodes, action(func() { setOpen(false) }))
		}
		return append(nodes, core.Scroll{TestID: "composer-activity-list", Style: core.Style{Gap: 10, MinHeight: core.Pt(0)},
			StyleFunc: func() core.Style { return core.Style{Height: core.Pt(min(360, max(96, cx.Env().Window().Height/2)))} },
			Children: []core.Node{
				core.Show{When: loading.Get, Then: func(*core.Scope) core.Node {
					return core.Text{Content: "正在读取活动…", Role: core.RoleStatus}
				}},
				core.Text{ContentFunc: func() string { return fmt.Sprintf("监控 · %d 项待执行", counts.Get()[0]) }, Role: core.RoleHeader,
					Style: core.Style{FontWeight: core.FontWeightSemibold}},
				core.Show{When: func() bool { return cancelNotice.Get() != "" }, Then: func(*core.Scope) core.Node {
					return core.Text{TestID: "composer-activity-cancel-status", Role: core.RoleStatus, ContentFunc: cancelNotice.Get}
				}},
				composerActivityReadState("monitor", "监控", "暂无监控。", func() (error, bool) {
					v := data.Get()
					return v.monitorErr, v.monitorsOK && len(v.monitors) == 0
				}),
				core.Each(func() []composerActivityMonitor { return data.Get().monitors }, func(m composerActivityMonitor) string { return m.ID },
					func(_ *core.Scope, monitor func() composerActivityMonitor, _ func() int) core.Node {
						id := monitor().ID
						return composerActivityRow(colors, "composer-activity-monitor-"+id,
							core.Text{TestID: "composer-activity-monitor-state-" + id,
								ContentFunc: func() string { return composerActivityMonitorLabel(monitor().State) }, Style: core.Style{FontWeight: core.FontWeightSemibold}},
							core.Text{ContentFunc: func() string { return monitor().Prompt }},
							composerActivityMetadata(colors, func() string { return composerActivityMonitorMeta(monitor()) }),
							core.Show{When: func() bool { m := monitor(); return m.Outcome != "" && m.Outcome != m.State }, Then: func(*core.Scope) core.Node {
								return composerActivityMetadata(colors, func() string { return monitor().Outcome })
							}},
							core.Show{When: func() bool { return writable() && monitor().State == "scheduled" }, Then: func(*core.Scope) core.Node {
								return ui.Button{Text: "取消监控", TestID: "composer-activity-cancel-" + id, Variant: ui.ButtonOutline,
									Style: core.Style{AlignSelf: core.AlignStart}, Busy: func() bool { return cancelling.Get() == id },
									Disabled: func() bool { return busy() || data.Get().monitorErr != nil }, OnPress: func() { cancel(monitor()) }}
							}})
					}),
				core.View{Style: core.Style{Height: core.Pt(1)}, StyleFunc: ui.Filled(colors.Border)},
				core.Text{ContentFunc: func() string { return fmt.Sprintf("发件箱 · 最近 %d 条", len(data.Get().messages)) }, Role: core.RoleHeader,
					Style: core.Style{FontWeight: core.FontWeightSemibold}},
				composerActivityReadState("outbox", "发件箱", "暂无外发记录。", func() (error, bool) {
					v := data.Get()
					return v.outboxErr, v.outboxOK && len(v.messages) == 0
				}),
				core.Each(func() []composerActivityDelivery { return data.Get().messages }, func(m composerActivityDelivery) string { return m.ID },
					func(_ *core.Scope, message func() composerActivityDelivery, _ func() int) core.Node {
						return composerActivityRow(colors, "composer-activity-delivery-"+message().ID,
							core.Text{ContentFunc: func() string { return composerActivityDeliveryLabel(message()) }, Style: core.Style{FontWeight: core.FontWeightSemibold}},
							composerActivityMetadata(colors, func() string { return composerActivityDeliveryMeta(message()) }),
							core.Show{When: func() bool { return cmp.Or(message().Body, message().Reason) != "" }, Then: func(*core.Scope) core.Node {
								return core.Text{ContentFunc: func() string { return cmp.Or(message().Body, message().Reason) }}
							}},
							core.Show{When: func() bool { return message().Error != "" }, Then: func(*core.Scope) core.Node {
								return core.Text{ContentFunc: func() string { return "外发错误：" + message().Error }, StyleFunc: ui.Colored(colors.Danger)}
							}})
					}),
			}})
	}
	return core.Dynamic(func(cx *core.Scope) core.Node {
		if cx.Env().Window().Width < 600 || ui.Touch(cx) {
			trigger := base.Button{TestID: "composer-activity", LabelFunc: label, Style: triggerStyle, StyleFunc: triggerLook,
				HitSlop: core.Edges{Top: 8, Bottom: 8}, OnPress: func(core.PressEvent) { setOpen(true) }, Children: []core.Node{face()}}
			return core.Group{trigger, ui.Dialog{Title: "会话活动", TestID: "composer-activity-panel", Open: open.Get,
				OnOpenChange: func(next bool, _ base.Reason) { setOpen(next) }, Children: panel(cx)}}
		}
		return base.Popover{TestID: "composer-activity", LabelFunc: label, Trigger: face(), Placement: core.PlaceTop, Align: core.AlignEnd,
			Style: triggerStyle, StyleFunc: triggerLook, Offset: 6, Margin: 8,
			PopupStyleFunc: func() core.Style {
				return ui.FinOf(cx).Menu().Over(core.Style{Width: core.Pt(360), Padding: core.All(12), Gap: 8,
					CornerRadius: ui.RadiusCard, BorderWidth: 1, Color: chrome.Text()})
			}, Open: open.Get, OnOpenChange: func(next bool, _ base.Reason) { setOpen(next) }, Children: panel(cx)}
	})
}

func composerActivityReadState(id, label, empty string, status func() (err error, empty bool)) core.Node {
	return core.Show{When: func() bool { err, none := status(); return err != nil || none }, Then: func(*core.Scope) core.Node {
		return core.Text{TestID: "composer-activity-" + id + "-status", Role: core.RoleStatus, ContentFunc: func() string {
			if err, _ := status(); err != nil {
				return "无法读取" + label + "。请刷新活动。"
			}
			return empty
		}}
	}}
}

func composerActivityRow(colors ui.Colors, id string, children ...core.Node) core.Node {
	return core.View{TestID: id, Style: core.Style{Padding: core.All(9), Gap: 5, BorderWidth: 1, CornerRadius: 8, MinWidth: core.Pt(0)},
		StyleFunc: func() core.Style { return core.Style{BorderColor: colors.Border()} }, Children: children}
}

func composerActivityMetadata(colors ui.Colors, text func() string) core.Node {
	return core.Text{ContentFunc: text, Style: core.Style{FontSize: 12}, StyleFunc: ui.Colored(colors.Muted)}
}

func composerActivityMonitorLabel(state string) string {
	switch state {
	case "scheduled":
		return "待执行"
	case "fired":
		return "已触发"
	case "cancelled":
		return "已取消"
	case "expired":
		return "已过期"
	default:
		return state
	}
}

func composerActivityMonitorMeta(m composerActivityMonitor) string {
	mode := "提示"
	if m.Mode == "script" {
		mode = "脚本"
	}
	parts := []string{fmt.Sprintf("%s · %d/%d 次检查", mode, m.Runs, m.MaxRuns)}
	if m.DueAt > 0 {
		parts = append(parts, "计划时间 "+composerActivityTime(m.DueAt))
	}
	if m.State == "scheduled" && m.ExpiresAt > 0 {
		parts = append(parts, "截止 "+composerActivityTime(m.ExpiresAt))
	}
	return strings.Join(parts, " · ")
}

func composerActivityDeliveryLabel(m composerActivityDelivery) string {
	if m.Peer == "" {
		return "站内记录"
	}
	switch m.Status {
	case "pending":
		return "待外发"
	case "sent":
		return "已发送"
	case "failed":
		return "外发失败"
	case "superseded":
		return "已被替代"
	default:
		return m.Status
	}
}

func composerActivityDeliveryMeta(m composerActivityDelivery) string {
	parts := []string{m.Kind}
	if m.Peer != "" {
		parts = append(parts, m.Peer)
	}
	if m.Topic != "" {
		parts = append(parts, "#"+m.Topic)
	}
	if ms := cmp.Or(m.DeliveredAt, m.CreatedAt); ms > 0 {
		parts = append(parts, composerActivityTime(ms))
	}
	return strings.Join(parts, " · ")
}

func composerActivityTime(ms int64) string {
	return time.UnixMilli(ms).Local().Format("01-02 15:04")
}
