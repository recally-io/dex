package main

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"uuid"

	"github.com/nsgo-dev/nsgo/base"
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
	away := core.NewSignal(cx, false)
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
						if !away.Get() {
							cx.Post(func() {
								if attempt.Err() == nil && !away.Get() {
									history.ScrollToEnd(false)
								}
							})
						}
						if !update.Running {
							mode.Set("steer")
						}
						if phase.Get() == chatSyncing {
							phase.Set(chatOnline)
							connection.Set("已连接")
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
	blank := func() bool { return isOnline() && info.Get().writable() && !started.Get() && len(lines.Get()) == 0 }
	name := func() string {
		if catalog := controls.catalog.Get(); catalog != nil {
			return cmp.Or(catalog.bot.Name, botName, "Dex")
		}
		return cmp.Or(botName, "Dex")
	}
	jump := func() { history.ScrollToEnd(false); away.Set(false) }
	reconnect := func() { cx.Post(connect) }
	welcome := chatLoadWelcome(cx, botID, blank)
	menu := func() core.Node {
		return core.Show{When: func() bool { return isOnline() || len(lines.Get()) > 0 }, Then: func(*core.Scope) core.Node {
			return ui.Menu{Label: "会话操作", TestID: "chat-menu", Placement: core.PlaceTop, Align: core.AlignStart,
				Trigger: ui.Button{Icon: lucide.Ellipsis, Variant: ui.ButtonGhost, Side: 36, Tint: colors.Muted},
				Children: []core.Node{
					core.Show{When: isOnline, Then: func(*core.Scope) core.Node {
						return ui.MenuItem{Text: "重连", TestID: "connect", Disabled: func() bool { return !canConnect() }, OnSelect: reconnect}
					}},
					core.Show{When: func() bool { return len(lines.Get()) > 0 }, Then: func(*core.Scope) core.Node {
						return ui.MenuItem{Text: "最新消息", TestID: "latest", Icon: lucide.ArrowDown,
							OnSelect: jump}
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
			Width: core.Pct(100), MaxWidth: core.Pt(824)},
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
			core.View{Style: core.Style{FlexGrow: 1, FlexBasis: core.Pt(0), MinHeight: core.Pt(0), Position: core.Relative},
				StyleFunc: func() core.Style {
					if blank() {
						return core.Style{Display: core.DisplayNone}
					}
					return core.Style{}
				}, Children: []core.Node{core.Scroll{TestID: "history", Ref: &history,
					Style:    core.Style{FlexGrow: 1, FlexBasis: core.Pt(0), MinHeight: core.Pt(0)},
					OnLayout: func(bounds core.Bounds) { historyHeight.Set(bounds.Size.Height) },
					OnScroll: func(event core.ScrollEvent) {
						if !event.Stale {
							away.Set(event.ContentSize.Height-event.Offset.Y-event.Size.Height > 100)
						}
					},
					Children: []core.Node{core.View{
						Style: core.Style{JustifyContent: core.JustifyEnd, Gap: 12},
						StyleFunc: func() core.Style {
							padding := core.Symmetric(32, 4)
							if wide() {
								padding = core.Symmetric(32, 32)
							}
							return core.Style{MinHeight: core.Pt(historyHeight.Get()), Padding: padding}
						},
						Children: []core.Node{
							core.Show{When: func() bool { return isOnline() && len(lines.Get()) == 0 }, Then: func(*core.Scope) core.Node {
								return core.Text{Content: "暂无消息", StyleFunc: ui.Colored(colors.Muted)}
							}},
							core.Each(lines.Get, func(line chatLine) string { return line.ID }, func(cx *core.Scope, line func() chatLine, _ func() int) core.Node {
								return chatMessage(cx, line, colors, wide)
							}),
						},
					}}}, core.Show{When: away.Get, Then: func(*core.Scope) core.Node {
					return ui.Button{TestID: "chat-jump", Label: "Jump to latest", Icon: lucide.ArrowDown, Side: 34,
						Variant: ui.ButtonSecondary, OnPress: jump, Style: core.Style{Position: core.Absolute,
							Bottom: core.Pt(10), Right: core.Pt(0), ZIndex: 1, CornerRadius: 17}}
				}}}},
			core.Show{When: readOnly, Then: func(*core.Scope) core.Node {
				return core.View{Class: "flex-row items-center gap-1", Children: []core.Node{
					menu(),
					core.Text{Content: "此会话仅供查看", TestID: "chat-read-only", Role: core.RoleStatus, StyleFunc: ui.Colored(colors.Muted)},
				}}
			}},
			core.Show{When: func() bool { return info.Get().writable() }, Then: func(*core.Scope) core.Node {
				card := core.View{
					TestID: "chat-composer",
					Style:  core.Style{Padding: core.Symmetric(8, 12), Gap: 8, BorderWidth: 1, CornerRadius: 12},
					StyleFunc: func() core.Style {
						return core.Style{BorderColor: colors.Border(), Background: colors.Surface()}
					},
					Children: []core.Node{
						core.Show{When: func() bool { return len(commands.options()) > 0 || commands.hint.Get() != "" || commands.Pending() },
							Then: func(*core.Scope) core.Node { return commands.Panel() }},
						files.Chips(),
						composerInput(cx, draft.Get, func() string {
							if running() {
								return "Add to this run…"
							}
							return "Message " + name()
						}, func() bool { return !editable() || controls.Reason() != "" }, setDraft,
							func(value bool) { composing.Set(value) }, submit, commands.Navigate),
						core.Show{When: func() bool { return isOnline() && controls.Reason() != "" }, Then: func(*core.Scope) core.Node {
							return core.Text{TestID: "chat-blocked-reason", Role: core.RoleStatus, ContentFunc: controls.Reason,
								Style: core.Style{FontSize: 12}, StyleFunc: ui.Colored(colors.Muted)}
						}},
						core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 4}, Children: []core.Node{
							files.Button(),
							core.Show{When: func() bool { return info.Get().Main }, Then: func(cx *core.Scope) core.Node {
								return composerActivity(cx, botID, info.Get, func(closePanel func()) core.Node {
									return core.View{Style: core.Style{Gap: 4}, Children: []core.Node{
										status(core.Style{FontSize: 12}),
										core.View{Class: "flex-row items-center gap-1", Children: []core.Node{
											ui.Button{Text: "Reconnect", TestID: "connect", Variant: ui.ButtonGhost,
												Disabled: func() bool { return !canConnect() }, OnPress: func() { closePanel(); reconnect() }},
											core.Show{When: func() bool { return len(lines.Get()) > 0 }, Then: func(*core.Scope) core.Node {
												return ui.Button{Text: "Jump to latest", TestID: "latest", Variant: ui.ButtonGhost,
													OnPress: func() { closePanel(); jump() }}
											}},
										}},
									}}
								})
							}, Else: func(*core.Scope) core.Node { return core.Group{menu(), controls.Controls()} }},
							core.Text{ContentFunc: actionStatus.Get, Role: core.RoleStatus, TestID: "chat-action-status", MaxLines: 4,
								Style: core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0), FontSize: 12}, StyleFunc: ui.Colored(colors.Muted)},
							sendMenu(),
							core.Show{When: func() bool { return running() && !hasDraft() }, Then: func(*core.Scope) core.Node {
								return chatComposerControl(cx, colors, "chat-stop", "Stop run", lucide.Square, 12, canStop, stop)
							}, Else: func(*core.Scope) core.Node {
								return core.Dynamic(func(*core.Scope) core.Node {
									label, icon := "Send message", lucide.ArrowUp
									if commands.Active() {
										label = "Run command"
									} else if running() {
										label, icon = "Steer", lucide.CornerDownLeft
										if mode.Get() == "followUp" {
											label, icon = "Queue", lucide.ListEnd
										}
									}
									return chatComposerControl(cx, colors, "send", label, icon, 16, canSend, submit)
								})
							}},
						}},
					},
				}
				return core.View{StyleFunc: func() core.Style {
					if blank() {
						return core.Style{FlexGrow: 1, MinHeight: core.Pt(0), JustifyContent: core.JustifyCenter}
					}
					return core.Style{}
				}, Children: []core.Node{
					core.Show{When: blank, Then: func(*core.Scope) core.Node { return chatWelcomeIntro(colors, name, info.Get) }},
					card,
					core.Show{When: func() bool { return blank() && cx.Env().KeyboardHeight() == 0 }, Then: func(*core.Scope) core.Node {
						return chatWelcomeSummary(colors, controls, welcome.Get)
					}},
				}}
			}},
		},
	}
}

type chatRun struct {
	running bool
	target  string
}

func chatMessage(cx *core.Scope, line func() chatLine, colors ui.Colors, wide func() bool) core.Node {
	hovered := core.NewSignal(cx, false)
	enter := func(core.MouseEvent) { hovered.Set(true) }
	leave := func(core.MouseEvent) { hovered.Set(false) }
	body := func(lineHeight core.Points) core.Node {
		return core.Text{ContentFunc: func() string { return line().Text }, Style: core.Style{FontSize: 15, LineHeight: lineHeight}}
	}
	label := func(text func() string) core.Node {
		return core.Text{ContentFunc: text, StyleFunc: func() core.Style {
			return core.Style{Color: colors.Muted(), FontSize: 12}
		}}
	}
	return core.Show{
		When: func() bool { return line().Role == "user" || line().Role == "queued" },
		Then: func(*core.Scope) core.Node {
			return core.View{Style: core.Style{AlignItems: core.AlignEnd, Gap: 4}, OnMouseEnter: enter, OnMouseLeave: leave, StyleFunc: func() core.Style {
				inset := core.Points(40)
				if wide() {
					inset = 96
				}
				return core.Style{Padding: core.Edges{Left: inset}}
			}, Children: []core.Node{
				core.View{TestID: "chat-user-bubble", Style: core.Style{Padding: core.Symmetric(10, 16), CornerRadius: 18, MaxWidth: core.Pct(100)},
					StyleFunc: func() core.Style { return core.Style{Background: colors.Inset()} }, Children: []core.Node{
						core.Show{When: func() bool { return line().Text != "" }, Then: func(*core.Scope) core.Node { return body(24.375) }},
						core.Show{When: func() bool { return line().Attachments != "" }, Then: func(*core.Scope) core.Node {
							return core.Each(func() []string { return strings.Split(line().Attachments, "\n") },
								func(name string) string { return name },
								func(_ *core.Scope, name func() string, _ func() int) core.Node { return label(name) })
						}},
					}},
				core.Show{When: func() bool { return line().Role == "queued" }, Then: func(*core.Scope) core.Node {
					return label(func() string { return "已排队" })
				}},
				core.Show{When: func() bool { return line().Role != "queued" }, Then: func(cx *core.Scope) core.Node {
					return chatUserFooter(cx, line, hovered.Get)
				}},
			}}
		},
		Else: func(*core.Scope) core.Node {
			return core.Show{When: func() bool { return line().Role == "assistant" }, Then: func(cx *core.Scope) core.Node {
				return core.View{TestID: "chat-assistant-turn", OnMouseEnter: enter, OnMouseLeave: leave,
					Style: core.Style{MaxWidth: core.Pt(680), Position: core.Relative}, Children: []core.Node{
						body(26.25), chatAssistantFooter(cx, line, hovered.Get),
					}}
			}, Else: func(*core.Scope) core.Node {
				return core.View{Style: core.Style{Gap: 4, MaxWidth: core.Pt(680)}, Children: []core.Node{
					label(func() string { return chatRoleLabel(line().Role) }), body(26.25),
				}}
			}}
		},
	}
}

func chatComposerControl(cx *core.Scope, colors ui.Colors, id, label string, icon func() *core.SVG, size core.Points, enabled func() bool, press func()) core.Node {
	ink := func() core.Color {
		if !enabled() && id == "send" {
			return ui.Alpha(colors.Muted(), .5)
		}
		return colors.Surface()
	}
	var glyph core.Node = ui.Icon(icon, size, ink)
	if id == "chat-stop" {
		glyph = core.View{Style: core.Style{Width: core.Pt(10), Height: core.Pt(10), CornerRadius: 2}, StyleFunc: ui.Filled(ink)}
	}
	return base.Button{TestID: id, Label: label, OnPress: func(core.PressEvent) { press() }, Disabled: func() bool { return !enabled() },
		HitSlop: core.Edges{Top: 8, Bottom: 8},
		Style: core.Style{Direction: core.Row, Width: core.Pt(28), Height: core.Pt(28), AlignItems: core.AlignCenter,
			JustifyContent: core.JustifyCenter, CornerRadius: 6},
		StyleFunc: func() core.Style {
			background := colors.Text()
			if !enabled() {
				if id == "send" {
					background = ui.Alpha(cx.Palette().Focus, .5)
				} else {
					background = ui.Alpha(background, .5)
				}
			}
			return core.Style{Background: background, FocusVisible: ui.FocusRing(cx, 2)}
		}, Children: []core.Node{glyph}}
}

type chatWelcome struct {
	channels, sessions           int
	channelsKnown, sessionsKnown bool
}

func chatLoadWelcome(cx *core.Scope, botID string, blank func() bool) *core.Signal[chatWelcome] {
	result := core.NewSignal(cx, chatWelcome{})
	requested := false
	cx.Effect(func() {
		if !blank() || requested {
			return
		}
		requested = true
		cx.Post(func() {
			work(cx, func(ctx context.Context) (chatWelcome, error) {
				var summary chatWelcome
				var bots struct {
					Bots []struct {
						ID       string `json:"id"`
						Activity struct {
							RunningChannels int `json:"runningChannels"`
						} `json:"activity"`
					} `json:"bots"`
				}
				if requestJSON(ctx, http.MethodGet, "/api/bots", nil, &bots) == nil {
					for _, bot := range bots.Bots {
						if bot.ID == botID {
							summary.channels, summary.channelsKnown = bot.Activity.RunningChannels, true
							break
						}
					}
				}
				var sessions struct {
					Sessions []chatSession `json:"sessions"`
				}
				if requestJSON(ctx, http.MethodGet, chatSessionsPath(botID), nil, &sessions) == nil {
					summary.sessions, summary.sessionsKnown = len(sessions.Sessions), true
				}
				return summary, nil
			}, func(value chatWelcome, _ error) { result.Set(value) })
		})
	})
	return result
}

func chatWelcomeIntro(colors ui.Colors, name func() string, session func() chatSession) core.Node {
	return core.View{TestID: "chat-welcome", Style: core.Style{Gap: 6, Padding: core.Symmetric(0, 8), Margin: core.Edges{Bottom: 16}}, Children: []core.Node{
		core.Text{ContentFunc: func() string { return "What should " + name() + " do?" }, Role: core.RoleHeader,
			Style: core.Style{FontSize: 20, FontWeight: core.FontWeightSemibold, LineHeight: 25, LetterSpacing: -.2}},
		core.Text{ContentFunc: func() string {
			text := "Tools run on this computer without a sandbox."
			if !session().Main {
				text = "This session has its own context. It does not see the main timeline. " + text
			}
			return text
		}, Style: core.Style{FontSize: 13, LineHeight: 21.125}, StyleFunc: ui.Colored(colors.Muted)},
	}}
}

func chatWelcomeSummary(colors ui.Colors, controls *composerContext, stats func() chatWelcome) core.Node {
	row := func(label string, value func() string, rule bool) core.Node {
		style := core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 12, Padding: core.All(8)}
		if rule {
			style.Borders = core.Edges{Top: 1}
		}
		return core.View{Style: style, StyleFunc: func() core.Style { return core.Style{BorderColor: colors.Border()} }, Children: []core.Node{
			core.Text{Content: label, Style: core.Style{FontSize: 13, LineHeight: 19.5, FlexGrow: 1}, StyleFunc: ui.Colored(colors.Muted)},
			core.Text{ContentFunc: value, Style: core.Style{FontSize: 13, LineHeight: 19.5}},
		}}
	}
	return core.View{Style: core.Style{Margin: core.Edges{Top: 24}}, Children: []core.Node{
		core.Text{Content: "This bot so far", Style: core.Style{FontSize: 12, FontWeight: core.FontWeightMedium,
			LineHeight: 18, Padding: core.Symmetric(0, 8), Margin: core.Edges{Bottom: 4}}, StyleFunc: ui.Colored(colors.Muted)},
		row("Model", func() string {
			catalog := controls.catalog.Get()
			if catalog == nil || catalog.modelErr != nil || catalog.botErr != nil {
				return "—"
			}
			if model, ok := catalog.model(catalog.primary(controls.current().Settings)); ok {
				return cmp.Or(model.Name, model.ID)
			}
			return "None chosen"
		}, false),
		row("Channels", func() string {
			s := stats()
			if !s.channelsKnown {
				return "—"
			}
			if s.channels == 0 {
				return "None connected"
			}
			return strconv.Itoa(s.channels) + " connected"
		}, true),
		row("Sessions", func() string {
			s := stats()
			if !s.sessionsKnown {
				return "—"
			}
			return strconv.Itoa(s.sessions)
		}, true),
	}}
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
