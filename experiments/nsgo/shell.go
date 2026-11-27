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
	"github.com/nsgo-dev/nsgo/base"
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
	page, bot, key string
	session        int
}

var globalPaths = map[string]string{"board": "/board", "integrations": "/integrations", "global-settings": "/settings/general", "archived": "/settings/archived"}

func (d destination) botScoped() bool {
	_, global := globalPaths[d.page]
	return !global && d.page != "task" && d.page != "missing"
}
func (d destination) address() string {
	if path, ok := globalPaths[d.page]; ok {
		return path
	}
	if d.page == "task" {
		return "/board/" + url.PathEscape(d.key)
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
	for page, path := range globalPaths {
		if path == u.Path {
			d.page = page
			return d
		}
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	switch {
	case len(parts) == 2 && parts[0] == "board":
		d.page, d.key = "task", parts[1]
	case len(parts) >= 2 && parts[0] == "b" && parts[1] != "":
		d.bot = parts[1]
		switch {
		case len(parts) == 2:
			d.page = "main"
		case len(parts) == 3:
			switch parts[2] {
			case "sessions", "automations", "artifacts":
				d.page = parts[2]
			case "bot":
				d.page = "settings"
			case "integrations":
				d.page = "tools"
			}
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
	if urlAtStart == "" || urlAtStart == "/" {
		_ = cx.SetAppURL(start.address())
	}
	stored, _ := cx.Storage().GetItem("dex-sidebar")
	w := workspaceView{
		route: core.NewSignal(cx, start), bots: core.NewSignalFunc(cx, []botListItem(nil), slices.Equal),
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
	drafts := map[string]string{}
	page := core.Dynamic(func(cx *core.Scope) core.Node {
		d := w.route.Get()
		return core.Untracked(cx, func() core.Node {
			var content core.Node
			switch d.page {
			case "main", "session":
				content = chatPage(cx, d.bot, d.session, w.name(), drafts)
			case "sessions":
				content = sessionsPage(cx, d.bot, func(id int, title string) { openChat(d.bot, id, title) })
			case "board":
				content = boardPage(cx, d.bot, func(key string) { w.goTo(destination{page: "task", bot: d.bot, key: key}, true) })
			case "task":
				content = taskPage(cx, d.bot, d.key, openChat)
			case "settings", "tools":
				var form core.Node
				if d.page == "tools" {
					form = toolsPage(cx, d.bot)
				} else {
					form = botSettingsPage(cx, d.bot, func(string) { w.reload() })
				}
				content = core.View{Style: ui.FillRest, Children: []core.Node{core.View{Style: core.Style{Direction: core.Row, Padding: core.Symmetric(4, 12), Gap: 4}, Children: []core.Node{ui.Button{Text: "Bot", Variant: ui.ButtonGhost, OnPress: func() { w.visit("settings") }}, ui.Button{Text: "Integrations", TestID: "bot-tools", Variant: ui.ButtonGhost, OnPress: func() { w.visit("tools") }}}}, form}}
			case "automations":
				content = automationsPage(cx, d.bot, func(id int, title string) { openChat(d.bot, id, title) })
			case "artifacts":
				content = artifactsPage(cx, d.bot)
			case "integrations":
				content = globalIntegrationsPage(cx)
			case "global-settings":
				content = globalSettingsPage(cx, func() { w.visit("archived") })
			case "archived":
				content = botsPage(cx, d.bot, func(id, name string) { w.reload(); openChat(id, 0, name) }, true)
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
			return base.Dialog{Label: "Navigation", TestID: "workspace-drawer", DefaultOpen: true, OnOpenChange: func(open bool, _ base.Reason) {
				if !open {
					closeOverlay()
				}
			}, BackdropStyle: core.Style{Background: ui.Black(0.10), BackdropFilters: []core.Filter{core.Blur(4)}}, LayerStyle: core.Style{AlignItems: core.AlignStart}, PopupStyle: core.Style{Height: core.Pct(100), Width: core.Pt(260), MaxWidth: core.Pct(85)}, PopupStyleFunc: func() core.Style { return core.Style{Background: w.sidebarColor(cx), Padding: core.Insets(cx)} }, Children: []core.Node{w.sidebar(cx, true)}}
		}},
		core.Show{When: func() bool { return w.overlay.Get() == "search" }, Then: func(cx *core.Scope) core.Node { return workspaceSearch(cx, w.bots.Get, closeOverlay, openChat) }}, core.Show{When: func() bool { return w.overlay.Get() == "create" }, Then: func(cx *core.Scope) core.Node {
			return botCreateDialog(cx, closeOverlay, func(id, name string) { w.reload(); openChat(id, 0, name) })
		}}}}
}
