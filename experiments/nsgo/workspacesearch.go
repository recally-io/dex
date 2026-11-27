package main

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

type workspaceSearchSession struct {
	chatSession
	UpdatedAt int64 `json:"updatedAt"`
}

type workspaceSearchHit struct {
	Bot, Name, Title string
	ID               int
	UpdatedAt        int64
	Archived         bool
}

func (h workspaceSearchHit) key() string { return h.Bot + "/" + strconv.Itoa(h.ID) }

func workspaceSearchMatches(bots []botListItem, lists map[string][]workspaceSearchSession, query string) (mains, sessions []workspaceSearchHit) {
	words := strings.Fields(strings.ToLower(query))
	match := func(text string) bool {
		text = strings.ToLower(text)
		for _, word := range words {
			if !strings.Contains(text, word) {
				return false
			}
		}
		return true
	}
	names := make(map[string]string, len(bots))
	for _, bot := range bots {
		names[bot.ID] = bot.Name
		if match(bot.Name + " 主对话 main") {
			mains = append(mains, workspaceSearchHit{Bot: bot.ID, Name: bot.Name, Title: bot.Name})
		}
	}
	seen := make(map[string]bool)
	for _, bot := range bots {
		for _, session := range lists[bot.ID] {
			if session.Main || session.ID <= 0 {
				continue
			}
			owner := cmp.Or(session.BotID, bot.ID)
			hit := workspaceSearchHit{Bot: owner, Name: cmp.Or(names[owner], session.BotName, owner),
				Title: cmp.Or(session.Title, "新会话"), ID: session.ID, UpdatedAt: session.UpdatedAt, Archived: session.ArchivedAt != 0}
			key := hit.key()
			if !seen[key] && match(hit.Name+" "+hit.Title) {
				seen[key] = true
				sessions = append(sessions, hit)
			}
		}
	}
	slices.SortFunc(sessions, func(a, b workspaceSearchHit) int {
		return cmp.Or(cmp.Compare(b.UpdatedAt, a.UpdatedAt), cmp.Compare(b.ID, a.ID), strings.Compare(a.Bot, b.Bot))
	})
	return mains, sessions
}

func workspaceSearch(cx *core.Scope, bots func() []botListItem, close func(), open func(bot string, id int, title string)) core.Node {
	query := core.NewSignal(cx, "")
	lists := core.NewSignalFunc(cx, map[string][]workspaceSearchSession(nil), nil)
	failed := core.NewSignalFunc(cx, []string(nil), nil)
	pending := core.NewSignal(cx, 0)
	var generation int
	load := func(current []botListItem) {
		generation++
		attempt := generation
		pending.Set(len(current))
		failed.Set(nil)
		results := make(map[string][]workspaceSearchSession, len(current))
		lists.Set(results)
		for _, bot := range current {
			work(cx, func(ctx context.Context) ([]workspaceSearchSession, error) {
				var response struct {
					Sessions []workspaceSearchSession `json:"sessions"`
				}
				err := requestJSON(ctx, http.MethodGet, chatSessionsPath(bot.ID), nil, &response)
				return response.Sessions, err
			}, func(sessions []workspaceSearchSession, err error) {
				if attempt != generation {
					return
				}
				pending.Set(pending.Get() - 1)
				if err != nil {
					failed.Set(append(failed.Get(), bot.Name))
					return
				}
				results[bot.ID] = sessions
				lists.Set(results)
			})
		}
	}
	cx.Effect(func() {
		current := bots()
		cx.Untrack(func() { load(current) })
	})
	type hits struct{ mains, sessions []workspaceSearchHit }
	matches := core.NewMemoFunc(cx, func() hits {
		main, sessions := workspaceSearchMatches(bots(), lists.Get(), query.Get())
		return hits{main, sessions}
	}, nil)
	return ui.Dialog{
		Title: "搜索工作区", Description: "按 Bot 名称和会话标题搜索，不搜索消息内容。", TestID: "workspace-search",
		Open: func() bool { return true }, OnOpenChange: func(isOpen bool, _ base.Reason) {
			if !isOpen {
				close()
			}
		},
		Children: []core.Node{
			core.View{Style: core.Style{FlexShrink: 1, MinHeight: core.Pt(0)}, StyleFunc: func() core.Style {
				env := cx.Env()
				return core.Style{Height: core.Pt(max(96, min(320, (env.Window().Height-env.KeyboardHeight())*0.45)))}
			}, Children: []core.Node{
				ui.Command{SearchLabel: "Bot 名称或会话标题", Placeholder: "搜索 Bot 或会话", Label: "搜索结果", Empty: "没有匹配结果。",
					TestID: "workspace-search-command", Style: ui.FillRest, AutoFocus: true, NoFilter: true, OnQueryChange: query.Set, OnCancel: close,
					Items: func() []base.CommandItem {
						hits := matches.Get()
						items := make([]base.CommandItem, 0, len(hits.mains)+len(hits.sessions))
						for _, hit := range hits.mains {
							items = append(items, base.CommandItem{ID: hit.key(), Label: hit.Title, Description: "主对话", Group: "Bot", Icon: lucide.Bot()})
						}
						for _, hit := range hits.sessions {
							description := hit.Name
							if hit.Archived {
								description += " · 已归档"
							}
							items = append(items, base.CommandItem{ID: hit.key(), Label: hit.Title, Description: description, Group: "会话", Icon: lucide.MessageCircle()})
						}
						return items
					}, OnSelect: func(key string) {
						hits := matches.Get()
						for _, hit := range slices.Concat(hits.mains, hits.sessions) {
							if hit.key() == key {
								close()
								open(hit.Bot, hit.ID, hit.Title)
								return
							}
						}
					}},
			}},
			core.Show{When: func() bool { return pending.Get() > 0 }, Then: func(*core.Scope) core.Node {
				return core.Text{TestID: "workspace-search-loading", Content: "正在加载会话…", Role: core.RoleStatus}
			}},
			core.Show{When: func() bool { return len(failed.Get()) > 0 }, Then: func(*core.Scope) core.Node {
				return core.View{Style: core.Style{Gap: 6}, Children: []core.Node{
					core.Text{TestID: "workspace-search-error", Role: core.RoleStatus, MaxLines: 2, ContentFunc: func() string {
						return fmt.Sprintf("未能加载以下 Bot 的会话：%s", strings.Join(failed.Get(), "、"))
					}},
					ui.Button{Text: "重试加载", TestID: "workspace-search-retry", Variant: ui.ButtonSecondary,
						Disabled: func() bool { return pending.Get() > 0 }, OnPress: func() { load(bots()) }},
				}}
			}},
		},
		Footer: []core.Node{ui.Button{Text: "关闭", TestID: "workspace-search-close", Variant: ui.ButtonSecondary, OnPress: close}},
	}
}
