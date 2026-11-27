package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nsgo-dev/nsgo"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

func main() { nsgo.Run(Root) }

func Root(cx *core.Scope) core.Node {
	cx.SetTheme(workspaceTheme)
	state := core.NewSignal(cx, "loading")
	problem := core.NewSignal(cx, "")
	check := func() {
		work(cx, func(ctx context.Context) (bool, error) {
			return true, requestJSON(ctx, http.MethodGet, "/api/health", nil, nil)
		}, func(_ bool, err error) {
			switch {
			case err == nil:
				state.Set("ready")
			case errors.Is(err, errSignIn):
				state.Set("login")
			default:
				problem.Set(err.Error())
				state.Set("error")
			}
		})
	}
	check()
	return core.Dynamic(func(cx *core.Scope) core.Node {
		switch state.Get() {
		case "ready":
			return core.Untracked(cx, func() core.Node { return workspace(cx, check) })
		case "login":
			return workspaceLogin(cx, check)
		case "error":
			return core.View{Style: core.Style{Padding: core.All(24), Gap: 12}, Children: []core.Node{core.Text{Content: "无法连接工作区"}, core.Text{ContentFunc: problem.Get}, ui.Button{Text: "重试", OnPress: check}}}
		default:
			return ui.Text("正在连接 dex…", 14, ui.Regular, ui.ColorsOf(cx).Muted)
		}
	})
}

func workspaceLogin(cx *core.Scope, connected func()) core.Node {
	token, note := core.NewSignal(cx, ""), core.NewSignal(cx, "")
	busy := core.NewSignal(cx, false)
	return core.View{Style: core.Style{Padding: core.All(24), Gap: 12}, Children: []core.Node{
		core.Text{Content: "登录 dex", Role: core.RoleHeader, Style: core.Style{FontSize: 24}}, core.Text{Content: "使用 dex 主机的访问令牌连接工作区。"}, ui.Input{Label: "访问令牌", Masked: true, Value: token.Get, OnChange: token.Set}, ui.Button{Text: "登录", Busy: busy.Get, Disabled: func() bool { return token.Get() == "" }, OnPress: func() {
			value := strings.TrimSpace(token.Get())
			token.Set("")
			busy.Set(true)
			work(cx, func(ctx context.Context) (bool, error) {
				return true, requestJSON(ctx, http.MethodPost, "/api/session", map[string]string{"token": value}, nil)
			}, func(_ bool, err error) {
				busy.Set(false)
				if err != nil {
					note.Set("登录失败：" + err.Error())
					return
				}
				connected()
			})
		}}, core.Text{ContentFunc: note.Get, Role: core.RoleStatus}}}
}

type destination struct {
	page, bot, key, query string
	session               int
}

func (d destination) botScoped() bool {
	switch d.page {
	case "board", "task", "board-result", "global-settings", "integrations", "missing":
		return false
	}
	return true
}

func (d destination) address() string {
	path := d.path()
	if d.query != "" {
		return path + "?" + d.query
	}
	return path
}

func (d destination) path() string {
	switch d.page {
	case "board", "integrations":
		return "/" + d.page
	case "global-settings":
		if d.key == "" {
			return "/settings/general"
		}
		return "/settings/" + d.key
	case "task":
		return "/board/" + url.PathEscape(d.key)
	case "board-result":
		return "/board/results/" + url.PathEscape(d.key)
	}
	base := "/b/" + url.PathEscape(d.bot)
	switch d.page {
	case "main":
		return base
	case "session":
		return base + "/s/" + strconv.Itoa(d.session)
	case "settings":
		return base + "/bot"
	case "tools":
		return base + "/integrations"
	case "automation":
		return base + "/automations/" + url.PathEscape(d.key)
	case "channels":
		if d.key != "" {
			return base + "/channels/" + url.PathEscape(d.key)
		}
	}
	return base + "/" + d.page
}
func workspaceLink(raw string) destination {
	d := destination{page: "missing", bot: "default"}
	u, err := url.Parse(raw)
	if err != nil {
		return d
	}
	if u.Path == "" || u.Path == "/" {
		d.page = "main"
		return d
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	switch {
	case u.Path == "/board" || u.Path == "/integrations":
		d.page, d.query = parts[0], u.RawQuery
	case u.Path == "/settings" || u.Path == "/settings/general":
		d.page = "global-settings"
	case len(parts) == 2 && parts[0] == "settings":
		for _, tab := range settingsSections {
			if tab.key == parts[1] {
				d.page, d.key = "global-settings", tab.key
				break
			}
		}
	case len(parts) == 3 && parts[0] == "board" && parts[1] == "results":
		d.page, d.key = "board-result", parts[2]
	case len(parts) == 2 && parts[0] == "board":
		d.page, d.key = "task", parts[1]
	case len(parts) >= 2 && parts[0] == "b" && parts[1] != "":
		d.bot = parts[1]
		switch {
		case len(parts) == 2:
			d.page = "main"
		case len(parts) == 3:
			switch parts[2] {
			case "sessions", "automations", "artifacts", "channels", "secrets":
				d.page = parts[2]
			case "bot":
				d.page = "settings"
			case "integrations":
				d.page, d.query = "tools", u.RawQuery
			case "skills":
				d.page, d.query = "tools", "section=skills"
			}
		case len(parts) == 4 && parts[2] == "automations":
			d.page, d.key = "automation", parts[3]
		case len(parts) == 4 && parts[2] == "channels" && slices.Contains([]string{"telegram", "wechat", "feishu"}, parts[3]):
			d.page, d.key = "channels", parts[3]
		case len(parts) == 4 && parts[2] == "s":
			if id, err := strconv.Atoi(parts[3]); err == nil && id > 0 {
				d.page, d.session = "session", id
			}
		}
	}
	return d
}

type workspaceView struct {
	route              *core.Signal[destination]
	bots               *core.Signal[[]botListItem]
	marks              *core.Signal[workspaceMarks]
	listError, overlay *core.Signal[string]
	collapsed, parent  *core.Signal[bool]
	goTo               func(destination, bool)
	back, reload       func()
	newSession         *core.Signal[chatSession]
}

func (w *workspaceView) compact(cx *core.Scope) bool { return cx.Env().Window().Width < 1024 }
func (w *workspaceView) name() string {
	id := w.route.Get().bot
	for _, b := range w.bots.Get() {
		if b.ID == id {
			return b.Name
		}
	}
	return id
}
func (w *workspaceView) visit(page string) {
	w.goTo(destination{page: page, bot: w.route.Get().bot}, false)
}

func workspace(cx *core.Scope, reconnect func()) core.Node {
	urlAtStart := cx.AppURL()
	start := workspaceLink(urlAtStart)
	if start.page != "missing" && (urlAtStart == "" || urlAtStart == "/" || urlAtStart == "/settings" || strings.HasSuffix(urlAtStart, "/skills")) {
		_ = cx.SetAppURL(start.address())
	}
	stored, _ := cx.Storage().GetItem("dex-sidebar")
	w := workspaceView{
		newSession: core.NewSignal(cx, chatSession{}), route: core.NewSignal(cx, start), bots: core.NewSignalFunc(cx, []botListItem(nil), slices.Equal),
		marks: core.NewSignalFunc(cx, workspaceMarks{}, nil), listError: core.NewSignal(cx, ""),
		overlay: core.NewSignal(cx, ""), collapsed: core.NewSignal(cx, stored == "collapsed"), parent: core.NewSignal(cx, false),
	}
	var parents []destination
	set := func(d destination) { w.overlay.Set(""); w.parent.Set(len(parents) > 0); w.route.Set(d) }
	w.goTo = func(d destination, child bool) {
		old := w.route.Get()
		if d == old {
			w.overlay.Set("")
			return
		}
		if child {
			parents = append(parents, old)
		} else {
			parents = nil
		}
		set(d)
		_ = cx.SetAppURL(d.address(), core.AppURLOptions{History: core.HistoryPush})
	}
	w.back = func() {
		if len(parents) > 0 {
			d := parents[len(parents)-1]
			parents = parents[:len(parents)-1]
			set(d)
			_ = cx.SetAppURL(d.address())
		}
	}
	cx.OnAppURLChange(func(change core.AppURLChange) {
		d := workspaceLink(change.URL)
		if !d.botScoped() {
			d.bot = w.route.Get().bot
		}
		parents = nil
		set(d)
	})
	fetching := false
	w.reload = func() {
		if fetching {
			return
		}
		fetching = true
		work(cx, func(ctx context.Context) ([]botListItem, error) {
			var result struct {
				Bots []botListItem `json:"bots"`
			}
			err := requestJSON(ctx, http.MethodGet, "/api/bots", nil, &result)
			return result.Bots, err
		}, func(list []botListItem, err error) {
			fetching = false
			if errors.Is(err, errSignIn) {
				reconnect()
				return
			}
			if err != nil {
				w.listError.Set("Could not load the bots.")
				return
			}
			w.listError.Set("")
			w.bots.Set(list)
		})
	}
	var poll func()
	poll = func() {
		w.reload()
		work(cx, loadWorkspaceMarks, func(m workspaceMarks, err error) {
			if err == nil {
				w.marks.Set(m)
			}
			cx.AfterFunc(10*time.Second, poll)
		})
	}
	poll()
	openChat := func(bot string, id int, _ string) {
		d := destination{page: "main", bot: bot}
		if id > 0 {
			d.page, d.session = "session", id
		}
		w.goTo(d, true)
	}
	cx.OnKeyWith("mod+k", core.KeyOptions{When: core.KeyAlways}, func() { w.overlay.Set("search") })
	type draftKey struct {
		bot string
		id  int
	}
	drafts := map[draftKey]*core.Signal[string]{}
	draftFor := func(k draftKey) *core.Signal[string] {
		if drafts[k] == nil {
			drafts[k] = core.NewSignal(cx, "")
		}
		return drafts[k]
	}
	page := core.Dynamic(func(cx *core.Scope) core.Node {
		d := w.route.Get()
		return core.Untracked(cx, func() core.Node {
			var content core.Node
			backToBoard := func() {
				if w.parent.Get() {
					w.back()
				} else {
					w.visit("board")
				}
			}
			switch d.page {
			case "main", "session":
				key := draftKey{d.bot, d.session}
				draft := draftFor(key)
				cx.Cleanup(func() {
					if drafts[key] == draft && draft.Get() == "" {
						delete(drafts, key)
					}
				})
				info := core.NewSignal(cx, chatSession{})
				closeProject := func() { w.overlay.Set("") }
				content = core.Group{chatPage(cx, d.bot, d.session, w.name(), draft, info),
					// Mount the modal after the session loads. If it shares the route push, closing it pops the session URL on Web.
					core.Show{When: func() bool { return w.overlay.Get() == "project" && info.Get().ID == d.session }, Then: func(cx *core.Scope) core.Node {
						return newSessionProject(cx, d.bot, w.newSession.Get(), closeProject, info.Set)
					}},
				}
			case "sessions":
				content = sessionsPage(cx, d.bot, openChat)
			case "board":
				filter, _ := url.ParseQuery(d.query)
				content = boardPage(cx, d.bot, func(key string) { w.goTo(destination{page: "task", bot: d.bot, key: key}, true) }, boardNavigation{
					FilterBot: filter.Get("bot"), ClearFilter: func() { w.visit("board") }, OpenChat: openChat,
					OpenBotSettings: func(bot string) { w.goTo(destination{page: "settings", bot: bot}, true) },
					OpenResult:      func(key string) { w.goTo(destination{page: "board-result", bot: d.bot, key: key}, true) },
				})
			case "task":
				content = taskPage(cx, d.bot, d.key, openChat, backToBoard)
			case "board-result":
				content = boardResultPage(cx, d.key, backToBoard, openChat)
			case "settings", "tools", "channels", "secrets":
				tab := strings.TrimPrefix(d.address(), "/b/"+url.PathEscape(d.bot)+"/")
				content = botProfileScreen(cx, d.bot, tab, func(next string) {
					w.goTo(workspaceLink("/b/"+url.PathEscape(d.bot)+"/"+next), false)
				}, func(string) { w.reload() })
			case "automations":
				content = automationsPage(cx, d.bot, func(id int, title string) { openChat(d.bot, id, title) }, func(key string) { w.goTo(destination{page: "automation", bot: d.bot, key: key}, true) })
			case "automation":
				content = automationDetailPage(cx, d.bot, d.key, func(id int, title string) { openChat(d.bot, id, title) }, func() { w.visit("automations") })
			case "artifacts":
				content = artifactsPage(cx, d.bot)
			case "integrations":
				content = integrationsScreen(cx, d.query, func(query string) { next := d; next.query = query; w.goTo(next, false) }, openChat)
			case "global-settings":
				content = globalSettingsScreen(cx, d.key, func(tab string) { next := workspaceLink("/settings/" + tab); next.bot = d.bot; w.goTo(next, false) }, func(bot string) { w.reload(); openChat(bot, 0, "") })
			default:
				content = ui.Button{Text: "Open Dex", OnPress: func() { w.goTo(destination{page: "main", bot: "default"}, false) }}
			}
			return core.View{TestID: "page-" + d.page, Style: ui.FillRest, Children: []core.Node{w.header(cx, d, openChat), content}}
		})
	})
	closeOverlay := func() { w.overlay.Set("") }
	colors := ui.ColorsOf(cx)
	return core.View{Style: ui.FillRest.Merge(core.Style{Direction: core.Row}), StyleFunc: func() core.Style {
		p := core.Insets(cx)
		p.Bottom = max(p.Bottom, cx.Env().KeyboardHeight())
		return core.Style{Padding: p, Background: colors.Background()}
	}, Children: []core.Node{
		core.Show{When: func() bool { return !w.compact(cx) && !w.collapsed.Get() }, Then: func(cx *core.Scope) core.Node { return w.sidebar(cx, false) }}, page,
		core.Show{When: func() bool { return w.compact(cx) && w.overlay.Get() == "drawer" }, Then: func(cx *core.Scope) core.Node {
			return w.drawer(cx, closeOverlay)
		}},
		core.Show{When: func() bool { return w.overlay.Get() == "artifacts" }, Then: func(cx *core.Scope) core.Node {
			d := w.route.Get()
			return artifactsPanel(cx, d.bot, d.session, closeOverlay, func(id int, text string) {
				draftFor(draftKey{d.bot, id}).Set(text)
				closeOverlay()
				if id != d.session {
					openChat(d.bot, id, "")
				}
			})
		}},
		core.Show{When: func() bool { return w.overlay.Get() == "search" }, Then: func(cx *core.Scope) core.Node { return workspaceSearch(cx, w.bots.Get, closeOverlay, openChat) }}, core.Show{When: func() bool { return w.overlay.Get() == "create" }, Then: func(cx *core.Scope) core.Node {
			return botCreateDialog(cx, closeOverlay, func(id, name string) { w.reload(); openChat(id, 0, name) })
		}}}}
}
