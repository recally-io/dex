package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

// newWorkspaceSession inherits the displayed session's project. A zero session ID selects Main.
func newWorkspaceSession(ctx context.Context, botID string, sessionID int) (chatSession, error) {
	var project struct {
		Cwd string `json:"cwd"`
	}
	if err := requestJSON(ctx, http.MethodGet, chatSessionPath(botID, sessionID), nil, &project); err != nil {
		return chatSession{}, fmt.Errorf("读取当前项目：%w", err)
	}
	var created chatSession
	if err := requestJSON(ctx, http.MethodPost, chatSessionsPath(botID), nil, &created); err != nil {
		return chatSession{}, err
	}
	if created.ID <= 0 {
		return chatSession{}, errors.New("新会话没有有效 ID")
	}
	// POST can reuse an empty session, so also write an empty cwd to clear its old project.
	var saved chatSession
	if err := requestJSON(ctx, http.MethodPut, chatSessionPath(botID, created.ID)+"/project", project, &saved); err != nil {
		return chatSession{}, fmt.Errorf("设置新会话的项目：%w", err)
	}
	if saved.ID != created.ID {
		return chatSession{}, errors.New("项目响应与新会话不符")
	}
	return saved, nil
}

func sessionsPage(cx *core.Scope, botID string, onOpen func(id int, title string)) core.Node {
	rows := core.NewSignalFunc(cx, []chatSession(nil), slices.Equal)
	loading := core.NewSignal(cx, false)
	writing := core.NewSignal(cx, false)
	loaded := core.NewSignal(cx, false)
	showArchived := core.NewSignal(cx, false)
	status := core.NewSignal(cx, "")
	colors := ui.ColorsOf(cx)
	busy := func() bool { return loading.Get() || writing.Get() }
	load := func() {
		if busy() {
			return
		}
		loading.Set(true)
		status.Set("正在加载会话…")
		work(cx, func(ctx context.Context) ([]chatSession, error) {
			var list struct {
				Sessions []chatSession `json:"sessions"`
			}
			err := requestJSON(ctx, http.MethodGet, chatSessionsPath(botID), nil, &list)
			return list.Sessions, err
		}, func(sessions []chatSession, err error) {
			loading.Set(false)
			if err != nil {
				status.Set("加载失败，可点刷新重试：" + err.Error())
				return
			}
			rows.Set(sessions)
			loaded.Set(true)
			status.Set("")
		})
	}

	canArchive := func(row chatSession) bool {
		return row.writable() && !row.Main && (row.BotID == "" || row.BotID == botID)
	}
	changeArchive := func(row chatSession) {
		if busy() || !canArchive(row) {
			return
		}
		method, verb := http.MethodPut, "归档"
		if row.ArchivedAt != 0 {
			method, verb = http.MethodDelete, "恢复"
		}
		writing.Set(true)
		status.Set("正在" + verb + "…")
		work(cx, func(ctx context.Context) (chatSession, error) {
			var session chatSession
			err := requestJSON(ctx, method, chatSessionPath(botID, row.ID)+"/archive", nil, &session)
			if err == nil && (session.ID != row.ID || (session.ArchivedAt != 0) != (method == http.MethodPut)) {
				err = errors.New("响应未确认会话状态，请刷新列表")
			}
			return session, err
		}, func(session chatSession, err error) {
			writing.Set(false)
			if err != nil {
				status.Set(verb + "失败：" + err.Error())
				return
			}
			rows.Update(func(list []chatSession) []chatSession {
				next := slices.Clone(list)
				for i := range next {
					if next[i].ID == session.ID {
						next[i] = session
						break
					}
				}
				return next
			})
			status.Set("已" + verb + "会话")
		})
	}

	section := func(archived bool) core.Node {
		selectRows := func() []chatSession {
			var selected []chatSession
			for _, row := range rows.Get() {
				if (row.ArchivedAt != 0) == archived {
					selected = append(selected, row)
				}
			}
			return selected
		}
		empty, action, idPrefix := "暂无会话。可从页头新建会话。", "归档", "sessions-archive-"
		if archived {
			empty, action, idPrefix = "暂无归档会话。", "恢复", "sessions-restore-"
		}
		return core.View{Style: core.Style{Gap: 8}, Children: []core.Node{
			core.Show{When: func() bool { return loaded.Get() && len(selectRows()) == 0 }, Then: func(*core.Scope) core.Node {
				return core.Text{Content: empty, StyleFunc: func() core.Style { return core.Style{Color: colors.Muted()} }}
			}},
			core.Each(selectRows, func(row chatSession) int { return row.ID }, func(_ *core.Scope, row func() chatSession, _ func() int) core.Node {
				id := strconv.Itoa(row().ID)
				local := func() bool { return row().BotID == "" || row().BotID == botID }
				return core.View{
					Style:     core.Style{Padding: core.All(8), Gap: 6, BorderWidth: 1, CornerRadius: 8},
					StyleFunc: func() core.Style { return core.Style{BorderColor: colors.Border()} },
					Children: []core.Node{
						core.Text{ContentFunc: func() string { return cmp.Or(row().Title, "新会话") }, MaxLines: 2,
							Style: core.Style{FontWeight: core.FontWeightSemibold}},
						core.Text{ContentFunc: func() string {
							session := row()
							if !local() {
								return "请切换到 Bot：" + cmp.Or(session.BotName, session.BotID)
							}
							return sessionsRowStatus(session)
						}, StyleFunc: func() core.Style { return core.Style{Color: colors.Muted()} }},
						core.Show{When: local, Then: func(*core.Scope) core.Node {
							return core.View{Class: "flex-row items-center gap-2", Children: []core.Node{
								ui.Button{Text: "打开", TestID: "sessions-open-" + id, Disabled: busy, OnPress: func() {
									if !busy() && local() {
										s := row()
										onOpen(s.ID, cmp.Or(s.Title, "新会话"))
									}
								}},
								core.Show{When: func() bool { return canArchive(row()) }, Then: func(*core.Scope) core.Node {
									return ui.Button{Text: action, TestID: idPrefix + id, Disabled: busy,
										OnPress: func() { changeArchive(row()) }}
								}},
							}}
						}},
					},
				}
			}),
		}}
	}

	load()
	return core.View{Style: core.Style{FlexGrow: 1, MinHeight: core.Pt(0), Padding: core.All(12), Gap: 8}, Children: []core.Node{

		core.Show{When: func() bool { return status.Get() != "" }, Then: func(*core.Scope) core.Node {
			return core.Text{ContentFunc: status.Get, Role: core.RoleStatus, TestID: "sessions-status"}
		}},
		core.View{Class: "flex-row items-center gap-2", Children: []core.Node{
			ui.Button{Text: "未归档", TestID: "sessions-active", Disabled: func() bool { return !showArchived.Get() },
				OnPress: func() { showArchived.Set(false) }},
			ui.Button{Text: "已归档", TestID: "sessions-archived", Disabled: showArchived.Get,
				OnPress: func() { showArchived.Set(true) }},
			ui.Button{Text: "刷新", TestID: "sessions-refresh", OnPress: load, Disabled: busy, Variant: ui.ButtonGhost},
		}},
		core.Scroll{TestID: "sessions-list", Style: core.Style{FlexGrow: 1, FlexBasis: core.Pt(0), Gap: 8},
			Children: []core.Node{
				core.Show{When: showArchived.Get, Then: func(*core.Scope) core.Node { return section(true) },
					Else: func(*core.Scope) core.Node { return section(false) }},
			}},
	}}
}

func sessionsRowStatus(session chatSession) string {
	label := "普通会话"
	if session.Channel == "automation" {
		label = "自动化运行"
	}
	if !session.writable() {
		label += " · 只读"
	}
	if session.Busy {
		label += " · 运行中"
	}
	return label
}
