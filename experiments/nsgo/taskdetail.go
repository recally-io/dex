package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
	"uuid"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

type taskDetail struct {
	Task     boardTask           `json:"task"`
	Entries  []taskDetailEntry   `json:"entries"`
	Sessions []taskDetailSession `json:"sessions"`
}

type taskDetailEntry struct {
	ID       int64  `json:"id"`
	Kind     string `json:"kind"`
	Actor    string `json:"actorKind"`
	ActorBot string `json:"actorBotId"`
	Content  string `json:"content"`
	Status   string `json:"status"`
}

type taskDetailSession struct {
	ID      int    `json:"sessionId"`
	BotID   string `json:"botId"`
	BotName string `json:"botName"`
	Title   string `json:"title"`
	Primary bool   `json:"isPrimary"`
	State   string `json:"state"`
}

type taskDetailMessage struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	TaskID string `json:"taskId"`
}

func taskPage(cx *core.Scope, _ string, key string, onChat func(botID string, id int, title string)) core.Node {
	path := boardTasksPath + "/" + url.PathEscape(key)
	colors := ui.ColorsOf(cx)
	detail := core.NewSignalFunc(cx, taskDetail{}, nil)
	busy := core.NewSignal(cx, "")
	loadError := core.NewSignal(cx, "")
	notice := core.NewSignal(cx, "")
	noticeAction := core.NewSignal(cx, "")
	draft := core.NewSignal(cx, "")
	composing := core.NewSignal(cx, false)
	type submission struct{ id, bot, task, text string }
	var pending submission

	load := func() {
		if busy.Get() != "" {
			return
		}
		busy.Set("load")
		loadError.Set("")
		work(cx, func(ctx context.Context) (taskDetail, error) {
			var value taskDetail
			err := requestJSON(ctx, http.MethodGet, path, nil, &value)
			return value, err
		}, func(value taskDetail, err error) {
			busy.Set("")
			if err != nil {
				loadError.Set("无法读取任务详情：" + err.Error())
				return
			}
			detail.Set(value)
		})
	}
	canWrite := func() bool { return busy.Get() == "" && loadError.Get() == "" && !composing.Get() }
	canPost := func() bool {
		n := utf8.RuneCountInString(strings.TrimSpace(draft.Get()))
		return canWrite() && n > 0 && n <= 32000
	}
	mutate := func(action, fail, success string, run func(context.Context) error, saved func()) {
		if !canWrite() {
			return
		}
		busy.Set(action)
		notice.Set("")
		noticeAction.Set(action)
		work(cx, func(ctx context.Context) (struct{}, error) { return struct{}{}, run(ctx) }, func(_ struct{}, err error) {
			busy.Set("")
			if err != nil {
				notice.Set(fail + "：" + err.Error())
			} else {
				if saved != nil {
					saved()
				}
				notice.Set(success)
			}
			// Refresh the revision and timeline before allowing the next write.
			load()
		})
	}
	note := func() {
		if !canPost() {
			return
		}
		revision := detail.Get().Task.Revision
		body := struct {
			Content string `json:"content"`
		}{Content: strings.TrimSpace(draft.Get())}
		mutate("note", "未能确认备注是否保存。重试前请检查任务记录", "备注已保存", func(ctx context.Context) error {
			return taskDetailWrite(ctx, http.MethodPost, path+"/entries", taskDetailRevision(revision), body)
		}, func() { draft.Set("") })
	}
	send := func() {
		task := detail.Get().Task
		if !canPost() || !taskDetailCanSend(task.Status) {
			return
		}
		text := strings.TrimSpace(draft.Get())
		// Reuse the message ID if the server accepted the message but the response was lost.
		if pending.id == "" || pending.bot != task.Assignee || pending.task != task.ID || pending.text != text {
			pending = submission{id: uuid.New().String(), bot: task.Assignee, task: task.ID, text: text}
		}
		req := pending
		mutate("send", "未能确认消息是否发送。草稿已保留，可重试", "消息已发送到负责 Bot 的主会话", func(ctx context.Context) error {
			base := botPath(req.bot) + "/sessions/"
			var main struct {
				ID int `json:"id"`
			}
			if err := requestJSON(ctx, http.MethodGet, base+"main", nil, &main); err != nil {
				return err
			}
			if main.ID <= 0 {
				return errors.New("服务端未返回有效的主会话编号")
			}
			body := taskDetailMessage{ID: req.id, Text: req.text, TaskID: req.task}
			return requestJSON(ctx, http.MethodPost, base+strconv.Itoa(main.ID)+"/messages", body, nil)
		}, func() {
			pending = submission{}
			draft.Set("")
		})
	}
	changeStatus := func(expected string) {
		task := detail.Get().Task
		next := taskDetailNextAction(task)
		if expected != next.status || expected == "" {
			return
		}
		body := struct {
			Status string `json:"status"`
		}{Status: next.status}
		mutate("status", "未能确认状态变更，已尝试刷新。请确认当前状态后重试", "任务状态已更新", func(ctx context.Context) error {
			return taskDetailWrite(ctx, http.MethodPatch, path, taskDetailRevision(task.Revision), body)
		}, nil)
	}
	openMain := func() { onChat(detail.Get().Task.Assignee, 0, "负责 Bot 的主会话") }

	cx.Untrack(load)
	return core.View{TestID: "task-page", Style: core.Style{FlexGrow: 1, FlexBasis: core.Pt(0), MinHeight: core.Pt(0)}, Children: []core.Node{
		core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, AlignItems: core.AlignCenter, Padding: core.All(14), Gap: 8}, Children: []core.Node{
			core.Text{Content: "任务详情", Role: core.RoleHeader, Style: core.Style{FlexGrow: 1, FontSize: 20, FontWeight: core.FontWeightSemibold}},
			ui.Button{TestID: "task-refresh", TextFunc: func() string {
				if loadError.Get() != "" {
					return "重试加载"
				}
				return "刷新详情"
			}, Disabled: func() bool { return busy.Get() != "" }, OnPress: load},
		}},
		core.Scroll{TestID: "task-scroll", Style: core.Style{FlexGrow: 1, FlexBasis: core.Pt(0), MinHeight: core.Pt(0), Padding: core.All(14), Gap: 12}, Children: []core.Node{
			core.Show{When: func() bool { return busy.Get() == "load" }, Then: func(*core.Scope) core.Node {
				return core.Text{TestID: "task-loading", Content: "正在读取任务详情…", Role: core.RoleStatus}
			}},
			core.Show{When: func() bool { return loadError.Get() != "" }, Then: func(*core.Scope) core.Node {
				return core.Text{TestID: "task-load-error", ContentFunc: loadError.Get, Role: core.RoleStatus}
			}},
			core.Show{When: func() bool { return detail.Get().Task.ID != "" }, Then: func(*core.Scope) core.Node {
				return core.View{Style: core.Style{Gap: 14}, Children: []core.Node{
					core.View{Style: core.Style{Gap: 5}, Children: []core.Node{
						core.Text{TestID: "task-key", ContentFunc: func() string { return detail.Get().Task.Key },
							StyleFunc: func() core.Style { return core.Style{Color: colors.Muted(), FontSize: 12} }},
						core.Text{TestID: "task-title", ContentFunc: func() string { return detail.Get().Task.Title }, Role: core.RoleHeader,
							Style: core.Style{FontSize: 24, FontWeight: core.FontWeightSemibold}},
						core.Text{TestID: "task-status", ContentFunc: func() string { return "状态：" + boardStatus(detail.Get().Task.Status) }},
						core.Text{TestID: "task-assignee", ContentFunc: func() string { return "负责 Bot：" + detail.Get().Task.Assignee }},
						core.Show{When: func() bool { return detail.Get().Task.Blocked }, Then: func(*core.Scope) core.Node {
							return core.Text{Content: "任务有阻塞", TestID: "task-blocked"}
						}},
					}},
					core.Each(func() []taskDetailAction {
						if action := taskDetailNextAction(detail.Get().Task); action.status != "" {
							return []taskDetailAction{action}
						}
						return nil
					}, func(action taskDetailAction) string { return action.status }, func(_ *core.Scope, action func() taskDetailAction, _ func() int) core.Node {
						status := action().status
						return ui.Button{TestID: "task-status-" + status, TextFunc: func() string { return action().label },
							Busy: func() bool { return busy.Get() == "status" }, Disabled: func() bool { return !canWrite() },
							Style: core.Style{AlignSelf: core.AlignStart}, OnPress: func() { changeStatus(status) }}
					}),
					core.Show{When: func() bool { return noticeAction.Get() == "status" && notice.Get() != "" }, Then: func(*core.Scope) core.Node {
						return core.Text{TestID: "task-transition-status", ContentFunc: notice.Get, Role: core.RoleStatus}
					}},
					core.View{Style: core.Style{Gap: 7}, Children: []core.Node{
						taskDetailHeading("关联会话"),
						core.Show{When: func() bool { return len(detail.Get().Sessions) == 0 }, Then: func(*core.Scope) core.Node {
							return core.Text{TestID: "task-no-sessions", Content: "暂无关联会话。打开主会话不会建立关联。"}
						}},
						core.Each(func() []taskDetailSession { return detail.Get().Sessions }, func(session taskDetailSession) int { return session.ID },
							func(_ *core.Scope, session func() taskDetailSession, _ func() int) core.Node {
								return core.View{Style: core.Style{Gap: 5, Padding: core.All(9), CornerRadius: 8, BorderWidth: 1},
									StyleFunc: func() core.Style { return core.Style{BorderColor: colors.Border()} }, Children: []core.Node{
										core.Text{ContentFunc: func() string { return taskDetailSessionTitle(session()) }},
										core.Text{ContentFunc: func() string {
											s := session()
											meta := cmp.Or(s.BotName, s.BotID) + " · " + taskDetailSessionState(s.State)
											if s.Primary {
												meta += " · 主要关联"
											}
											return meta
										}, StyleFunc: func() core.Style { return core.Style{Color: colors.Muted(), FontSize: 12} }},
										ui.Button{Text: "打开关联会话", TestID: "task-open-session-" + strconv.Itoa(session().ID),
											Style: core.Style{AlignSelf: core.AlignStart}, Disabled: func() bool { return busy.Get() != "" }, OnPress: func() {
												s := session()
												onChat(s.BotID, s.ID, taskDetailSessionTitle(s))
											}},
									}}
							}),
						ui.Button{Text: "打开负责 Bot 的主会话", TestID: "task-open-main", Style: core.Style{AlignSelf: core.AlignStart},
							Disabled: func() bool { return busy.Get() != "" || loadError.Get() != "" }, OnPress: openMain},
					}},
					core.View{Style: core.Style{Gap: 7}, Children: []core.Node{
						taskDetailHeading("备注与消息"),
						ui.Input{TestID: "task-composer", Label: "备注或消息", Value: draft.Get, Multiline: true,
							Style: core.Style{Height: core.Pt(76)}, SubmitBehavior: core.SubmitBehaviorNewline,
							ReadonlyFunc: func() bool { return busy.Get() != "" }, OnChange: func(value string) { draft.Set(value) },
							OnCompositionChange: func(value core.Composition) { composing.Set(value.Composing) }},
						core.Show{When: func() bool { return utf8.RuneCountInString(strings.TrimSpace(draft.Get())) > 32000 }, Then: func(*core.Scope) core.Node {
							return core.Text{Content: "正文最多 32000 个字符。", Role: core.RoleStatus}
						}},
						core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, Gap: 8}, Children: []core.Node{
							ui.Button{Text: "添加备注", TestID: "task-note", OnPress: note,
								Busy: func() bool { return busy.Get() == "note" }, Disabled: func() bool { return !canPost() }},
							core.Show{When: func() bool { return taskDetailCanSend(detail.Get().Task.Status) }, Then: func(*core.Scope) core.Node {
								return ui.Button{Text: "发送给负责 Bot", TestID: "task-send", Variant: ui.ButtonPrimary, OnPress: send,
									Busy: func() bool { return busy.Get() == "send" }, Disabled: func() bool { return !canPost() }}
							}},
						}},
						core.Show{When: func() bool { return noticeAction.Get() != "status" && notice.Get() != "" }, Then: func(*core.Scope) core.Node {
							return core.Text{TestID: "task-action-status", ContentFunc: notice.Get, Role: core.RoleStatus}
						}},
					}},
					core.View{Style: core.Style{Gap: 7}, Children: []core.Node{
						taskDetailHeading("任务记录"),
						core.Show{When: func() bool { return len(detail.Get().Entries) == 0 }, Then: func(*core.Scope) core.Node {
							return core.Text{TestID: "task-no-entries", Content: "暂无记录。可添加备注。"}
						}},
						core.Each(func() []taskDetailEntry { return detail.Get().Entries }, func(entry taskDetailEntry) int64 { return entry.ID },
							func(_ *core.Scope, entry func() taskDetailEntry, _ func() int) core.Node {
								return core.View{TestID: "task-entry-" + strconv.FormatInt(entry().ID, 10), Style: core.Style{Gap: 3, Padding: core.All(8)}, Children: []core.Node{
									core.Text{ContentFunc: func() string { return taskDetailEntryLabel(entry()) },
										StyleFunc: func() core.Style { return core.Style{Color: colors.Muted(), FontSize: 12} }},
									core.Show{When: func() bool { return entry().Content != "" }, Then: func(*core.Scope) core.Node {
										return core.Text{ContentFunc: func() string { return entry().Content }}
									}},
								}}
							}),
					}},
				}}
			}},
		}},
	}}
}

func taskDetailHeading(text string) core.Text {
	return core.Text{Content: text, Role: core.RoleHeader, Style: core.Style{FontSize: 16, FontWeight: core.FontWeightSemibold}}
}

func taskDetailRevision(revision int64) http.Header {
	return http.Header{"If-Match": {strconv.FormatInt(revision, 10)}}
}

func taskDetailWrite(ctx context.Context, method, path string, headers http.Header, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	res, err := gatewayRequest(ctx, method, path, "application/json", bytes.NewReader(data), "application/json", headers)
	if err != nil {
		return err
	}
	closeChatBody(ctx, res.Body)()
	return nil
}

type taskDetailAction struct{ status, label string }

func taskDetailNextAction(task boardTask) taskDetailAction {
	switch task.Status {
	case "backlog", "ready":
		return taskDetailAction{"in_progress", "开始任务"}
	case "in_progress":
		if task.Reviewer != "" {
			return taskDetailAction{"in_review", "提交评审"}
		}
		return taskDetailAction{"done", "完成任务"}
	case "done", "cancelled":
		return taskDetailAction{"in_progress", "重新打开任务"}
	}
	return taskDetailAction{}
}

func taskDetailCanSend(status string) bool {
	switch status {
	case "backlog", "ready", "in_progress", "in_review":
		return true
	}
	return false
}

func taskDetailSessionTitle(session taskDetailSession) string {
	return cmp.Or(session.Title, "会话 #"+strconv.Itoa(session.ID))
}

func taskDetailSessionState(state string) string {
	switch state {
	case "running":
		return "运行中"
	case "completed":
		return "已完成"
	case "failed":
		return "失败"
	case "stale":
		return "已失效"
	case "cancelled":
		return "已取消"
	}
	return state
}

func taskDetailEntryLabel(entry taskDetailEntry) string {
	var actor string
	switch entry.Actor {
	case "owner":
		actor = "用户"
	case "bot":
		actor = cmp.Or(entry.ActorBot, "Bot")
	case "system":
		actor = "系统"
	default:
		actor = "外部来源"
	}
	var event string
	switch entry.Kind {
	case "note":
		event = "备注"
	case "artifact":
		event = "产物"
	case "status":
		event = "状态变更"
	case "assign":
		event = "分配变更"
	case "review":
		event = "评审"
	case "execution":
		event = "执行记录"
	case "external":
		event = "外部记录"
	default:
		event = "任务记录"
	}
	if entry.Status != "" {
		event += " · " + boardStatus(entry.Status)
	}
	return actor + " · " + event
}
