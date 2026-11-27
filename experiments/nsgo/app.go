package main

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"uuid"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
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
	options   chatSendOptions
}

func chatPage(cx *core.Scope, botID string, sessionID int, botName string, drafts map[string]string) core.Node {
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
	hasContext := core.NewSignal(cx, false)
	started := core.NewSignal(cx, false)
	mode := core.NewSignal(cx, "steer")
	actionStatus := core.NewSignal(cx, "")
	colors := ui.ColorsOf(cx)
	var history core.Ref
	historyHeight := core.NewSignal(cx, core.Points(0))
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

	controls := newComposerContext(cx, botID, info.Get, started.Get)
	running := func() bool { return activity.Get().running }
	working := func() bool { return sending.Get() || stopping.Get() }
	editable := func() bool { return phase.Get() == chatOnline && info.Get().writable() }
	commands := newComposerCommands(cx, botID, info.Get, draft.Get, setDraft, running, func() bool { return !hasContext.Get() },
		func() bool { return !editable() || working() || composing.Get() || controls.Reason() != "" })
	files := newComposerFiles(cx, botID, func() bool { return !editable() || working() || commands.Pending() })
	canConnect := func() bool { return phase.Get() != chatOpening && !working() && !commands.Pending() }
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
						hasContext.Set(update.HasContext)
						started.Set(started.Get() || update.Started)
						activity.Set(chatRun{update.Running, update.RunTarget})
						if !update.Running {
							mode.Set("steer")
						}
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

	hasDraft := func() bool { return strings.TrimSpace(draft.Get()) != "" || files.Any() }
	canSend := func() bool {
		if !editable() || working() || composing.Get() || commands.Pending() || !hasDraft() {
			return false
		}
		return commands.Active() || !files.Blocked() && controls.Reason() == ""
	}
	submit := func() {
		if !canSend() {
			return
		}
		if commands.Active() {
			actionStatus.Set("")
			commands.Submit()
			return
		}
		session, text := info.Get().ID, strings.TrimSpace(draft.Get())
		ids := files.IDs()
		// A lost response does not mean the server rejected the request. Retry the original mode too.
		if pending.key == "" || pending.session != session || pending.text != text || !slices.Equal(pending.options.Attachments, ids) {
			sendMode := "followUp"
			if running() {
				sendMode = mode.Get()
			}
			pending = chatSubmission{session, uuid.New().String(), text, chatSendOptions{Mode: sendMode, Attachments: ids}}
		}
		request := pending
		sending.Set(true)
		actionStatus.Set("正在发送…")
		work(cx, func(ctx context.Context) (struct{}, error) {
			return struct{}{}, sendChat(ctx, botID, request.session, request.key, request.text, request.options)
		}, func(_ struct{}, err error) {
			sending.Set(false)
			if err != nil {
				actionStatus.Set("无法确认发送结果，可用原草稿重试：" + err.Error())
				return
			}
			pending = chatSubmission{}
			if info.Get().ID == request.session {
				if strings.TrimSpace(draft.Get()) == request.text {
					setDraft("")
				}
				files.Sent(request.options.Attachments)
			}
			actionStatus.Set("已发送")
		})
	}

	canStop := func() bool {
		session, run := info.Get(), activity.Get()
		return phase.Get() == chatOnline && session.writable() && run.running && !working() && !commands.Pending() &&
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

	wide := func() bool { return cx.Env().Window().Width >= 640 }
	isOnline := func() bool { return phase.Get() == chatOnline }
	readOnly := func() bool { return info.Get().ID != 0 && !info.Get().writable() }
	menu := func() core.Node {
		return core.Show{When: func() bool { return isOnline() || len(lines.Get()) > 0 }, Then: func(*core.Scope) core.Node {
			return ui.Menu{Label: "会话操作", TestID: "chat-menu", Placement: core.PlaceTop, Align: core.AlignStart,
				Trigger: ui.Button{Icon: lucide.Ellipsis, Variant: ui.ButtonGhost, Side: 36, Tint: colors.Muted},
				Children: []core.Node{
					core.Show{When: isOnline, Then: func(*core.Scope) core.Node {
						return ui.MenuItem{Text: "重连", TestID: "connect", Disabled: func() bool { return !canConnect() }, OnSelect: connect}
					}},
					core.Show{When: func() bool { return len(lines.Get()) > 0 }, Then: func(*core.Scope) core.Node {
						return ui.MenuItem{Text: "最新消息", TestID: "latest", Icon: lucide.ArrowDown,
							OnSelect: func() { history.ScrollToEnd(false) }}
					}},
					core.Show{When: func() bool { return info.Get().writable() && activity.Get().running && hasDraft() }, Then: func(*core.Scope) core.Node {
						return ui.MenuItem{Text: "停止运行", TestID: "chat-stop", Icon: lucide.Square, Disabled: func() bool { return !canStop() }, OnSelect: stop}
					}},
				}}
		}}
	}
	sendMenu := func() core.Node {
		return core.Show{When: func() bool { return running() && hasDraft() }, Then: func(*core.Scope) core.Node {
			return ui.Menu{Label: "发送方式和停止", TestID: "chat-send-menu", Placement: core.PlaceTop, Align: core.AlignEnd,
				Trigger: ui.Button{Icon: lucide.ChevronDown, Variant: ui.ButtonGhost, Side: 32, Tint: colors.Muted}, Children: []core.Node{
					core.Show{When: func() bool { return !commands.Active() }, Then: func(*core.Scope) core.Node {
						return core.Group{
							ui.MenuItem{Text: "引导当前运行", TestID: "chat-mode-steer", Icon: lucide.CornerDownLeft,
								Checked: func() bool { return mode.Get() == "steer" }, OnSelect: func() { mode.Set("steer") }},
							ui.MenuItem{Text: "排队发送", TestID: "chat-mode-queue", Icon: lucide.ListEnd,
								Checked: func() bool { return mode.Get() == "followUp" }, OnSelect: func() { mode.Set("followUp") }},
							ui.MenuSeparator{},
						}
					}},
					ui.MenuItem{Text: "停止运行", TestID: "chat-stop", Icon: lucide.Square, Disabled: func() bool { return !canStop() }, OnSelect: stop},
				}}
		}}
	}
	status := func(style core.Style) core.Node {
		return core.Text{TestID: "connection-status", Role: core.RoleStatus, ContentFunc: connection.Get, MaxLines: 3, Style: style,
			StyleFunc: ui.Colored(colors.Muted)}
	}

	return core.View{
		Style: core.Style{FlexGrow: 1, MinHeight: core.Pt(0), AlignSelf: core.AlignCenter,
			Width: core.Pct(100), MaxWidth: core.Pt(824), Gap: 4},
		StyleFunc: func() core.Style {
			padding := core.Edges{Bottom: 12, Left: 12, Right: 12}
			if wide() {
				padding = core.Edges{Bottom: 20, Left: 32, Right: 32}
			}
			return core.Style{Padding: padding}
		},
		Children: []core.Node{
			core.Show{When: func() bool { return !isOnline() }, Then: func(*core.Scope) core.Node {
				return core.View{Style: core.Style{Gap: 4, Padding: core.All(8), CornerRadius: 8}, StyleFunc: ui.Filled(colors.Inset), Children: []core.Node{
					status(core.Style{FontSize: 13}),
					core.Show{When: func() bool { return phase.Get() != chatOpening }, Then: func(*core.Scope) core.Node {
						return ui.Button{TestID: "connect", Text: "重连", Variant: ui.ButtonGhost, Style: core.Style{AlignSelf: core.AlignStart},
							Disabled: func() bool { return !canConnect() }, OnPress: connect}
					}},
				}}
			}},
			core.Scroll{TestID: "history", Ref: &history,
				Style:    core.Style{FlexGrow: 1, FlexBasis: core.Pt(0), MinHeight: core.Pt(0)},
				OnLayout: func(bounds core.Bounds) { historyHeight.Set(bounds.Size.Height) },
				Children: []core.Node{core.View{
					Style: core.Style{JustifyContent: core.JustifyEnd, Gap: 16},
					StyleFunc: func() core.Style {
						padding := core.Symmetric(20, 4)
						if wide() {
							padding = core.Symmetric(24, 32)
						}
						return core.Style{MinHeight: core.Pt(historyHeight.Get()), Padding: padding}
					},
					Children: []core.Node{
						core.Show{When: func() bool { return isOnline() && len(lines.Get()) == 0 }, Then: func(*core.Scope) core.Node {
							return core.Text{Content: "暂无消息", StyleFunc: ui.Colored(colors.Muted)}
						}},
						core.Each(lines.Get, func(line chatLine) string { return line.ID }, func(_ *core.Scope, line func() chatLine, _ func() int) core.Node {
							return chatMessage(line, colors)
						}),
					},
				}}},
			core.Show{When: readOnly, Then: func(*core.Scope) core.Node {
				return core.View{Class: "flex-row items-center gap-1", Children: []core.Node{
					menu(),
					core.Text{Content: "此会话仅供查看", TestID: "chat-read-only", Role: core.RoleStatus, StyleFunc: ui.Colored(colors.Muted)},
				}}
			}},
			core.Show{When: func() bool { return info.Get().writable() }, Then: func(*core.Scope) core.Node {
				return core.View{
					Style: core.Style{Padding: core.Symmetric(6, 10), Gap: 2, BorderWidth: 1, CornerRadius: 14},
					StyleFunc: func() core.Style {
						return core.Style{BorderColor: colors.Border(), Background: colors.Surface()}
					},
					Children: []core.Node{
						commands.Panel(),
						files.Chips(),
						composerInput(cx, draft.Get, func() string {
							if running() {
								return "追加消息…"
							}
							if botName != "" {
								return "给 " + botName + " 发消息…"
							}
							return "输入消息…"
						}, func() bool { return !editable() || controls.Reason() != "" }, setDraft,
							func(value bool) { composing.Set(value) }, submit, commands.Navigate),
						core.Show{When: func() bool { return isOnline() && controls.Reason() != "" }, Then: func(*core.Scope) core.Node {
							return core.Text{TestID: "chat-blocked-reason", Role: core.RoleStatus, ContentFunc: controls.Reason,
								Style: core.Style{FontSize: 12}, StyleFunc: ui.Colored(colors.Muted)}
						}},
						core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 4}, Children: []core.Node{
							menu(), files.Button(),
							core.Show{When: func() bool { return info.Get().Main }, Then: func(cx *core.Scope) core.Node {
								return composerActivity(cx, botID, info.Get)
							}, Else: func(*core.Scope) core.Node { return controls.Controls() }},
							core.Show{When: isOnline, Then: func(*core.Scope) core.Node {
								return status(core.Style{FontSize: 12, MaxWidth: core.Pt(64)})
							}},
							core.Text{ContentFunc: actionStatus.Get, Role: core.RoleStatus, TestID: "chat-action-status", MaxLines: 4,
								Style: core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0), FontSize: 12}, StyleFunc: ui.Colored(colors.Muted)},
							sendMenu(),
							core.Show{When: func() bool { return running() && !hasDraft() }, Then: func(*core.Scope) core.Node {
								return ui.Button{TestID: "chat-stop", Label: "停止运行", Icon: lucide.Square, Side: 36, IconSize: 14,
									Color: colors.Text, Tint: colors.Surface, Disabled: func() bool { return !canStop() }, OnPress: stop}
							}, Else: func(*core.Scope) core.Node {
								return core.Dynamic(func(*core.Scope) core.Node {
									label, icon := "发送", lucide.ArrowUp
									if commands.Active() {
										label = "执行命令"
									} else if running() {
										label, icon = "引导当前运行", lucide.CornerDownLeft
										if mode.Get() == "followUp" {
											label, icon = "排队发送", lucide.ListEnd
										}
									}
									return ui.Button{TestID: "send", Label: label, Icon: icon, Side: 36, IconSize: 16,
										Color: colors.Text, Tint: colors.Surface, OnPress: submit, Disabled: func() bool { return !canSend() }}
								})
							}},
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
		return core.Text{ContentFunc: func() string { return line().Text }, Style: core.Style{FontSize: 15, LineHeight: 23}}
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
				core.View{Style: core.Style{Padding: core.Symmetric(9, 15), CornerRadius: 18, MaxWidth: core.Pct(100)},
					StyleFunc: func() core.Style { return core.Style{Background: colors.Inset()} }, Children: []core.Node{
						core.Show{When: func() bool { return line().Text != "" }, Then: func(*core.Scope) core.Node { return body() }},
						core.Show{When: func() bool { return line().Attachments != "" }, Then: func(*core.Scope) core.Node {
							return core.Each(func() []string { return strings.Split(line().Attachments, "\n") },
								func(name string) string { return name },
								func(_ *core.Scope, name func() string, _ func() int) core.Node { return label(name) })
						}},
					}},
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
