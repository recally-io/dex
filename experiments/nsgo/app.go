package main

import (
	"context"
	"slices"
	"strings"
	"time"
	"uuid"

	"github.com/nsgo-dev/nsgo"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

func main() { nsgo.Run(Root) }

type chatPhase uint8

const (
	chatIdle chatPhase = iota
	chatOpening
	chatSyncing
	chatOnline
	chatOffline
)

func Root(cx *core.Scope) core.Node {
	lifetime := cx.Context()
	phase := core.NewSignal(cx, chatIdle)
	connection := core.NewSignal(cx, "未连接")
	lines := core.NewSignalFunc(cx, []chatLine(nil), slices.Equal)
	draft := core.NewSignal(cx, "")
	composing := core.NewSignal(cx, false)
	sending := core.NewSignal(cx, false)
	sendStatus := core.NewSignal(cx, "")
	colors := ui.ColorsOf(cx)
	var history core.Ref

	// Only the UI goroutine reads or changes the active connection and draft.
	var active context.Context
	var stop context.CancelFunc
	var session int
	type submission struct {
		session   int
		key, text string
	}
	var pending submission

	connect := func() {
		if phase.Get() == chatOpening {
			return
		}
		if stop != nil {
			stop()
		}
		if sending.Get() {
			sending.Set(false)
			sendStatus.Set("重连中断了发送。草稿已保留，可重试。")
		}
		attempt, cancel := context.WithCancel(lifetime)
		active, stop = attempt, cancel
		phase.Set(chatOpening)
		connection.Set("正在连接…")
		go func() {
			openCtx, done := context.WithTimeout(attempt, 15*time.Second)
			id, err := openMain(openCtx)
			done()
			if err != nil {
				cx.Post(func() {
					if active == attempt {
						phase.Set(chatOffline)
						connection.Set("连接失败：" + err.Error())
					}
				})
				return
			}
			cx.Post(func() {
				if active == attempt {
					session = id
					phase.Set(chatSyncing)
					connection.Set("正在读取历史…")
				}
			})
			err = watchChat(attempt, id, func(snapshot []chatLine) {
				copyOfSnapshot := slices.Clone(snapshot)
				cx.Post(func() {
					if active != attempt || phase.Get() == chatOffline {
						return
					}
					lines.Set(copyOfSnapshot)
					if phase.Get() == chatSyncing {
						phase.Set(chatOnline)
						connection.Set("已连接")
						cx.Post(func() {
							if active == attempt {
								history.ScrollToEnd(false)
							}
						})
					}
				})
			})
			cx.Post(func() {
				if active != attempt {
					return
				}
				phase.Set(chatOffline)
				if err == nil {
					connection.Set("连接已断开，请重连。")
				} else {
					connection.Set("连接已断开：" + err.Error())
				}
			})
		}()
	}

	canSend := func() bool {
		return phase.Get() == chatOnline && !sending.Get() && !composing.Get() && strings.TrimSpace(draft.Get()) != ""
	}
	submit := func() {
		if !canSend() {
			return
		}
		text := draft.Get()
		// A lost response does not mean the server rejected the request.
		if pending.key == "" || pending.session != session || pending.text != text {
			pending = submission{session, uuid.New().String(), text}
		}
		attempt, request := active, pending
		sending.Set(true)
		sendStatus.Set("正在发送…")
		go func() {
			ctx, cancel := context.WithTimeout(attempt, 20*time.Second)
			defer cancel()
			err := sendChat(ctx, request.session, request.key, request.text)
			cx.Post(func() {
				if active != attempt {
					return
				}
				sending.Set(false)
				if err != nil {
					sendStatus.Set("无法确认发送结果：" + err.Error())
					return
				}
				pending = submission{}
				draft.Set("")
				sendStatus.Set("已发送")
			})
		}()
	}

	return core.View{
		Style: core.Style{FlexGrow: 1, MinHeight: core.Pt(0), Gap: 10},
		StyleFunc: func() core.Style {
			padding := core.Insets(cx)
			return core.Style{
				Background: colors.Background(),
				Padding: core.Edges{Top: padding.Top + 16, Right: padding.Right + 16,
					Bottom: max(padding.Bottom, cx.Env().KeyboardHeight()) + 16, Left: padding.Left + 16},
			}
		},
		Children: []core.Node{
			core.Text{Content: "Default Bot · 主聊天", Role: core.RoleHeader,
				Style: core.Style{FontSize: 22, FontWeight: core.FontWeightSemibold}},
			core.View{Class: "flex-row items-center gap-2", Children: []core.Node{
				ui.Button{TestID: "connect", TextFunc: func() string {
					if phase.Get() == chatIdle {
						return "Connect"
					}
					return "Reconnect"
				}, Disabled: func() bool { return phase.Get() == chatOpening }, OnPress: connect},
				ui.Button{Text: "最新消息", TestID: "latest", Disabled: func() bool { return len(lines.Get()) == 0 },
					OnPress: func() { history.ScrollToEnd(false) }},
			}},
			core.Text{TestID: "connection-status", Role: core.RoleStatus, ContentFunc: connection.Get, MaxLines: 2},
			core.Scroll{TestID: "history", Ref: &history,
				Style: core.Style{FlexGrow: 1, FlexBasis: core.Pt(0), MinHeight: core.Pt(0), Padding: core.All(12), Gap: 12,
					BorderWidth: 1, CornerRadius: 10},
				StyleFunc: func() core.Style { return core.Style{BorderColor: colors.Border()} },
				Children: []core.Node{
					core.Show{When: func() bool { return len(lines.Get()) == 0 }, Then: func(*core.Scope) core.Node {
						return core.Text{Content: "暂无消息", StyleFunc: func() core.Style { return core.Style{Color: colors.Muted()} }}
					}},
					core.Each(lines.Get, func(line chatLine) string { return line.ID }, func(_ *core.Scope, line func() chatLine, _ func() int) core.Node {
						return core.View{Style: core.Style{Gap: 3}, Children: []core.Node{
							core.Text{ContentFunc: func() string {
								if line().Role == "user" {
									return "你"
								}
								return "Default Bot"
							}, StyleFunc: func() core.Style {
								return core.Style{Color: colors.Muted(), FontSize: 12, FontWeight: core.FontWeightSemibold}
							}},
							core.Text{ContentFunc: func() string { return line().Text }, Style: core.Style{FontSize: 15}},
						}}
					}),
				}},
			ui.Input{TestID: "draft", Label: "消息", Placeholder: "输入消息，点击发送", Multiline: true,
				SubmitBehavior: core.SubmitBehaviorNewline, Value: draft.Get, ReadonlyFunc: sending.Get,
				OnChange:            func(text string) { draft.Set(text) },
				OnCompositionChange: func(value core.Composition) { composing.Set(value.Composing) },
				Style:               core.Style{Height: core.Pt(64)}},
			core.View{Class: "flex-row items-center gap-2", Children: []core.Node{
				ui.Button{Text: "发送", TestID: "send", OnPress: submit, Disabled: func() bool { return !canSend() },
					Style: core.Style{Height: core.Pt(40), MinWidth: core.Pt(64)}},
				core.Text{ContentFunc: sendStatus.Get, Role: core.RoleStatus, MaxLines: 2,
					Style: core.Style{FlexShrink: 1, MinWidth: core.Pt(0)}},
			}},
		},
	}
}
