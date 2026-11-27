package main

import (
	"cmp"
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

type composerDirectoryEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type composerDirectory struct {
	Path      string                   `json:"path"`
	Parent    string                   `json:"parent"`
	Truncated bool                     `json:"truncated"`
	Entries   []composerDirectoryEntry `json:"entries"`
}

func (c *composerContext) projectPane(cx *core.Scope) core.Node {
	cfg := core.Untracked(cx, c.current)
	draft := core.NewSignal(cx, cmp.Or(cfg.Cwd, "~"))
	listing := core.NewSignal(cx, (*composerDirectory)(nil))
	loading := core.NewSignal(cx, false)
	status := core.NewSignal(cx, "")
	recent := core.NewSignalFunc(cx, []string(nil), nil)
	recentStatus := core.NewSignal(cx, "正在读取最近目录…")
	params := url.Values{"botId": {c.botID}}
	if cfg.RunnerID != "" {
		params.Set("runnerId", cfg.RunnerID)
	}
	load := func(path string) {
		if loading.Get() || c.saving.Get() != "" {
			return
		}
		draft.Set(path)
		listing.Set(nil)
		loading.Set(true)
		status.Set("正在读取目录…")
		query := params.Clone()
		query.Set("path", path)
		composerContextWork(cx, func(ctx context.Context) (composerDirectory, error) {
			var result composerDirectory
			err := requestJSON(ctx, http.MethodGet, "/api/directories?"+query.Encode(), nil, &result)
			return result, err
		}, func(result composerDirectory, err error) {
			loading.Set(false)
			if err != nil {
				status.Set("读取目录失败：" + err.Error())
				return
			}
			listing.Set(new(result))
			if draft.Get() == path {
				draft.Set(result.Path)
			}
			status.Set("")
			if len(result.Entries) == 0 {
				status.Set("此目录没有可见的子目录。")
			} else if result.Truncated {
				status.Set("仅列出前 500 个子目录。")
			}
		})
	}
	cx.Post(func() {
		load(draft.Get())
		endpoint := botPath(c.botID) + "/projects"
		if cfg.RunnerID != "" {
			endpoint += "?" + url.Values{"runnerId": {cfg.RunnerID}}.Encode()
		}
		work(cx, func(ctx context.Context) ([]string, error) {
			var result struct {
				Projects []string `json:"projects"`
			}
			err := requestJSON(ctx, http.MethodGet, endpoint, nil, &result)
			return result.Projects, err
		}, func(rows []string, err error) {
			if err != nil {
				recentStatus.Set("无法读取最近目录，可在下方直接浏览：" + err.Error())
				return
			}
			recent.Set(rows)
			recentStatus.Set("")
			if len(rows) == 0 {
				recentStatus.Set("暂无最近目录。")
			}
		})
	})
	locked := func() bool { return c.saving.Get() != "" || !c.session().writable() }
	save := func(path string) {
		if locked() || c.current().RunnerID != cfg.RunnerID {
			return
		}
		c.save("project", struct {
			Cwd string `json:"cwd"`
		}{path}, func() { c.panel.Set("settings") })
	}
	return core.View{Style: core.Style{Gap: 8}, Children: []core.Node{
		ui.Button{Text: "返回会话设置", TestID: "composer-project-back", Variant: ui.ButtonGhost, Disabled: func() bool { return c.saving.Get() != "" },
			OnPress: func() { c.status.Set(""); c.panel.Set("settings") }},
		core.Text{Content: "项目目录"},
		ui.Input{Label: "目录路径", TestID: "composer-project-path", Value: draft.Get, OnChange: draft.Set,
			ReadonlyFunc: func() bool { return c.saving.Get() != "" }},
		ui.Button{Text: "浏览此目录", TestID: "composer-project-browse", Variant: ui.ButtonSecondary, Busy: loading.Get,
			Disabled: func() bool { return strings.TrimSpace(draft.Get()) == "" || c.saving.Get() != "" }, OnPress: func() { load(strings.TrimSpace(draft.Get())) }},
		ui.Button{Text: "使用此目录", TestID: "composer-project-save", Disabled: func() bool { return locked() || strings.TrimSpace(draft.Get()) == "" },
			OnPress: func() { save(strings.TrimSpace(draft.Get())) }},
		ui.Button{Text: "恢复 Runner 工作区", TestID: "composer-project-reset", Variant: ui.ButtonSecondary, Disabled: locked, OnPress: func() { save("") }},
		core.Text{ContentFunc: status.Get, Role: core.RoleStatus},
		core.Show{When: func() bool { return listing.Get() != nil && listing.Get().Parent != "" }, Then: func(*core.Scope) core.Node {
			return ui.Button{Text: "上一级目录", TestID: "composer-project-up", Variant: ui.ButtonGhost,
				OnPress: func() {
					if data := listing.Get(); data != nil {
						load(data.Parent)
					}
				}}
		}},
		core.Each(func() []composerDirectoryEntry {
			if data := listing.Get(); data != nil {
				return data.Entries
			}
			return nil
		}, func(entry composerDirectoryEntry) string { return entry.Path },
			func(_ *core.Scope, entry func() composerDirectoryEntry, _ func() int) core.Node {
				return ui.Button{TextFunc: func() string { return entry().Name }, Variant: ui.ButtonGhost,
					Disabled: func() bool { return c.saving.Get() != "" }, OnPress: func() { load(entry().Path) }}
			}),
		core.Text{Content: "最近目录"},
		core.Text{ContentFunc: recentStatus.Get, Role: core.RoleStatus},
		core.Each(recent.Get, func(path string) string { return path }, func(_ *core.Scope, path func() string, _ func() int) core.Node {
			return ui.Button{TextFunc: path, Variant: ui.ButtonGhost, Disabled: func() bool { return loading.Get() || c.saving.Get() != "" },
				OnPress: func() { load(path()) }}
		}),
	}}
}
