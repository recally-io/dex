package main

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"uuid"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

type chatPhase uint8

const (
	chatOpening chatPhase = iota
	chatSyncing
	chatOnline
	chatOffline
)

type chatSubmission struct {
	session   int
	key, text string
}

func chatPage(cx *core.Scope, botID string, sessionID int, _ string, drafts map[string]string) core.Node {
	lifetime := cx.Context()
	phase := core.NewSignal(cx, chatOffline)
	connection := core.NewSignal(cx, "正在连接…")
	lines := core.NewSignalFunc(cx, []chatLine(nil), slices.Equal)
	info := core.NewSignal(cx, chatSession{})
	activity := core.NewSignal(cx, chatRun{})
	draftKey := botID + ":" + strconv.Itoa(sessionID)
	draft := core.NewSignal(cx, drafts[draftKey])
	composing := core.NewSignal(cx, false)
	sending := core.NewSignal(cx, false)
	stopping := core.NewSignal(cx, false)
	stoppedTarget := core.NewSignal(cx, "")
	actionStatus := core.NewSignal(cx, "")
	colors := ui.ColorsOf(cx)
	var history core.Ref
	var stopStream context.CancelFunc
	var pending chatSubmission

	setDraft := func(text string) {
		draft.Set(text)
		if text == "" {
			delete(drafts, draftKey)
		} else if drafts != nil {
			drafts[draftKey] = text
		}
	}

	canConnect := func() bool { return phase.Get() != chatOpening && !sending.Get() && !stopping.Get() }
	connect := func() {
		if !canConnect() {
			return
		}
		if stopStream != nil {
			stopStream()
		}
		phase.Set(chatOpening)
		connection.Set("正在连接…")
		activity.Set(chatRun{})
		stoppedTarget.Set("")
		work(cx, func(ctx context.Context) (chatSession, error) {
			return chatGetSession(ctx, botID, sessionID)
		}, func(session chatSession, err error) {
			if err != nil {
				phase.Set(chatOffline)
				connection.Set("连接失败：" + err.Error())
				return
			}
			info.Set(session)
			phase.Set(chatSyncing)
			connection.Set("正在读取历史…")
			attempt, cancel := context.WithCancel(lifetime)
			stopStream = cancel
			go func() {
				err := watchChat(attempt, botID, session.ID, func(update chatUpdate) {
					cx.Post(func() {
						if attempt.Err() != nil || phase.Get() == chatOffline {
							return
						}
						lines.Set(update.Lines)
						activity.Set(chatRun{update.Running, update.RunTarget})
						if phase.Get() == chatSyncing {
							phase.Set(chatOnline)
							connection.Set("已连接")
							cx.Post(func() {
								if attempt.Err() == nil {
									history.ScrollToEnd(false)
								}
							})
						}
					})
				})
				cx.Post(func() {
					if attempt.Err() != nil {
						return
					}
					cancel()
					phase.Set(chatOffline)
					activity.Set(chatRun{})
					connection.Set("连接已断开，请重连。")
					if err != nil {
						connection.Set("连接已断开：" + err.Error())
					}
				})
			}()
		})
	}

	canSend := func() bool {
		return phase.Get() == chatOnline && info.Get().writable() && !sending.Get() && !stopping.Get() && !composing.Get() && strings.TrimSpace(draft.Get()) != ""
	}
	submit := func() {
		if !canSend() {
			return
		}
		session, text := info.Get().ID, draft.Get()
		// A lost response does not mean the server rejected the request.
		if pending.key == "" || pending.session != session || pending.text != text {
			pending = chatSubmission{session, uuid.New().String(), text}
		}
		request := pending
		sending.Set(true)
		actionStatus.Set("正在发送…")
		work(cx, func(ctx context.Context) (struct{}, error) {
			return struct{}{}, sendChat(ctx, botID, request.session, request.key, request.text)
		}, func(_ struct{}, err error) {
			sending.Set(false)
			if err != nil {
				actionStatus.Set("无法确认发送结果，可用原草稿重试：" + err.Error())
				return
			}
			pending = chatSubmission{}
			setDraft("")
			actionStatus.Set("已发送")
		})
	}

	canStop := func() bool {
		session, run := info.Get(), activity.Get()
		return phase.Get() == chatOnline && session.writable() && run.running && !sending.Get() && !stopping.Get() &&
			(!session.Main || (run.target != "" && run.target != stoppedTarget.Get()))
	}
	stop := func() {
		if !canStop() {
			return
		}
		session := info.Get()
		target := ""
		if session.Main {
			target = activity.Get().target
		}
		stopping.Set(true)
		actionStatus.Set("正在请求停止…")
		work(cx, func(ctx context.Context) (struct{}, error) {
			return struct{}{}, chatStopRun(ctx, botID, session.ID, target)
		}, func(_ struct{}, err error) {
			stopping.Set(false)
			if err != nil {
				actionStatus.Set("停止失败：" + err.Error())
				return
			}
			stoppedTarget.Set(target)
			actionStatus.Set("已请求停止")
		})
	}
	connect()

	return core.View{
		Style: core.Style{FlexGrow: 1, MinHeight: core.Pt(0), AlignSelf: core.AlignCenter,
			Width: core.Pct(100), MaxWidth: core.Pt(760), Padding: core.Symmetric(8, 12), Gap: 5},
		Children: []core.Node{
			core.Scroll{TestID: "history", Ref: &history,
				Style: core.Style{FlexGrow: 1, FlexBasis: core.Pt(0), MinHeight: core.Pt(0), Padding: core.Symmetric(12, 4), Gap: 18},
				Children: []core.Node{
					core.Show{When: func() bool { return phase.Get() == chatOnline && len(lines.Get()) == 0 }, Then: func(*core.Scope) core.Node {
						return core.Text{Content: "暂无消息", StyleFunc: func() core.Style { return core.Style{Color: colors.Muted()} }}
					}},
					core.Each(lines.Get, func(line chatLine) string { return line.ID }, func(_ *core.Scope, line func() chatLine, _ func() int) core.Node {
						return chatMessage(line, colors)
					}),
				}},
			core.View{Class: "flex-row items-center gap-1", Children: []core.Node{
				core.Text{TestID: "connection-status", Role: core.RoleStatus, ContentFunc: connection.Get, MaxLines: 3,
					StyleFunc: func() core.Style {
						return core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0), Color: colors.Muted(), FontSize: 12}
					}},
				ui.Button{TestID: "connect", Text: "重连", Variant: ui.ButtonGhost,
					Disabled: func() bool { return !canConnect() }, OnPress: connect},
				core.Show{When: func() bool { return len(lines.Get()) > 0 }, Then: func(*core.Scope) core.Node {
					return ui.Button{TestID: "latest", Text: "最新消息", Variant: ui.ButtonGhost, OnPress: func() { history.ScrollToEnd(false) }}
				}},
			}},
			core.Show{When: func() bool { return info.Get().ID != 0 && !info.Get().writable() }, Then: func(*core.Scope) core.Node {
				return core.Text{Content: "此会话仅供查看", TestID: "chat-read-only", Role: core.RoleStatus}
			}},
			core.Show{When: func() bool { return info.Get().writable() }, Then: func(*core.Scope) core.Node {
				return core.View{
					Style: core.Style{Padding: core.All(6), Gap: 3, BorderWidth: 1, CornerRadius: 14},
					StyleFunc: func() core.Style {
						return core.Style{BorderColor: colors.Border(), Background: colors.Surface()}
					},
					Children: []core.Node{
						ui.Input{TestID: "draft", Label: "消息", Multiline: true,
							SubmitBehavior: core.SubmitBehaviorNewline, Value: draft.Get, ReadonlyFunc: sending.Get,
							OnChange:            setDraft,
							OnCompositionChange: func(value core.Composition) { composing.Set(value.Composing) },
							Style:               core.Style{Height: core.Pt(64), BorderWidth: core.Zero, FontSize: 15},
							StyleFunc:           func() core.Style { return core.Style{Background: colors.Surface()} }},
						core.View{Class: "flex-row items-center gap-1", Children: []core.Node{
							core.Text{ContentFunc: actionStatus.Get, Role: core.RoleStatus, TestID: "chat-action-status", MaxLines: 3,
								StyleFunc: func() core.Style {
									return core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0), Color: colors.Muted(), FontSize: 12}
								}},
							core.Show{When: func() bool { return activity.Get().running }, Then: func(*core.Scope) core.Node {
								return ui.Button{TestID: "chat-stop", Text: "停止", Disabled: func() bool { return !canStop() }, OnPress: stop}
							}},
							ui.Button{Text: "发送", TestID: "send", OnPress: submit, Disabled: func() bool { return !canSend() },
								Variant: ui.ButtonPrimary, Style: core.Style{Height: core.Pt(40), MinWidth: core.Pt(64)}},
						}},
					},
				}
			}},
		},
	}
}

type chatRun struct {
	running bool
	target  string
}

func chatMessage(line func() chatLine, colors ui.Colors) core.Node {
	body := func() core.Node {
		return core.Text{ContentFunc: func() string { return line().Text }, Style: core.Style{FontSize: 15}}
	}
	label := func(text func() string) core.Node {
		return core.Text{ContentFunc: text, StyleFunc: func() core.Style {
			return core.Style{Color: colors.Muted(), FontSize: 12}
		}}
	}
	return core.Show{
		When: func() bool { return line().Role == "user" || line().Role == "queued" },
		Then: func(*core.Scope) core.Node {
			return core.View{Style: core.Style{AlignItems: core.AlignEnd, Padding: core.Edges{Left: 32}, Gap: 4}, Children: []core.Node{
				core.View{Style: core.Style{Padding: core.Symmetric(8, 12), CornerRadius: 14, MaxWidth: core.Pct(100)},
					StyleFunc: func() core.Style { return core.Style{Background: colors.Inset()} }, Children: []core.Node{body()}},
				core.Show{When: func() bool { return line().Role == "queued" }, Then: func(*core.Scope) core.Node {
					return label(func() string { return "已排队" })
				}},
			}}
		},
		Else: func(*core.Scope) core.Node {
			return core.View{Style: core.Style{Gap: 4}, Children: []core.Node{
				core.Show{When: func() bool { return line().Role != "assistant" }, Then: func(*core.Scope) core.Node {
					return label(func() string { return chatRoleLabel(line().Role) })
				}},
				body(),
			}}
		},
	}
}

func chatRoleLabel(role string) string {
	switch role {
	case "tool":
		return "工具"
	case "error":
		return "运行错误"
	case "session_result":
		return "子会话结果"
	default:
		return "系统"
	}
}
