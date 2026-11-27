package main

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

const toolsVaultScope = "agent"

var toolsVaultName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type toolsVaultEntry struct {
	Name      string    `json:"name"`
	Scope     string    `json:"scope"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type toolsVaultAction struct{ kind, name string }

type toolsVaultWrite struct {
	Scope string `json:"scope"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

func toolsVaultSection(cx *core.Scope, path string) core.Node {
	feed := toolsNewFeed[[]toolsVaultEntry](cx)
	active := core.NewSignal(cx, toolsVaultAction{})
	closeForm := func() { active.Set(toolsVaultAction{}) }
	get := func(ctx context.Context) ([]toolsVaultEntry, error) {
		entries, err := toolsGet[[]toolsVaultEntry](ctx, path)
		if err != nil {
			return nil, err
		}
		botEntries := make([]toolsVaultEntry, 0, len(entries))
		for _, entry := range entries {
			if entry.Scope == toolsVaultScope {
				botEntries = append(botEntries, entry)
			}
		}
		return botEntries, nil
	}
	refresh := func() {
		closeForm()
		feed.run("正在读取密钥…", "", "读取密钥失败。请刷新密钥。", get)
	}
	canEdit := func() bool { return feed.fresh.Get() && !feed.busy.Get() }
	edit := func(form toolsVaultAction, name, value string) {
		if !canEdit() || active.Get() != form {
			return
		}
		closeForm()
		method, endpoint := http.MethodPut, path
		verb := "保存"
		var body *toolsVaultWrite
		if form.kind == "delete" {
			method = http.MethodDelete
			endpoint += "?" + url.Values{"scope": {toolsVaultScope}, "name": {name}}.Encode()
			verb = "删除"
		} else {
			body = new(toolsVaultWrite{toolsVaultScope, name, value})
		}
		feed.run("正在"+verb+"密钥…", "密钥已"+verb+"。", "未能确认"+verb+"结果。请刷新密钥后再试。", func(ctx context.Context) ([]toolsVaultEntry, error) {
			var requestBody any
			if body != nil {
				requestBody = body
			}
			err := requestJSON(ctx, method, endpoint, requestBody, nil)
			if body != nil {
				body.Value = ""
			}
			if err != nil {
				return nil, err
			}
			return get(ctx)
		})
	}
	refresh()
	create := toolsVaultAction{kind: "create"}
	return toolsSection(cx, "Bot 专属密钥",
		toolsCaption(cx, "只显示名称和更新时间。工具可用 vault:名称 引用密钥。"),
		toolsButton("tools-vault-refresh", "刷新密钥", ui.ButtonOutline, feed.busy.Get, refresh),
		feed.status("tools-vault-status"),
		core.Dynamic(func(cx *core.Scope) core.Node {
			entries := feed.value.Get()
			if entries == nil {
				return core.Group{}
			}
			rows := core.Group{}
			if len(*entries) == 0 {
				rows = append(rows, toolsCaption(cx, "暂无 Bot 专属密钥。"))
			}
			rows = append(rows, core.Show{When: func() bool { return active.Get() == create },
				Then: func(cx *core.Scope) core.Node {
					return toolsVaultForm(cx, create, *entries, canEdit, closeForm, edit)
				}, Else: func(*core.Scope) core.Node {
					return toolsButton("tools-vault-add", "新增密钥", ui.ButtonSecondary,
						func() bool { return !canEdit() }, func() { active.Set(create) })
				}})
			for _, entry := range *entries {
				replace := toolsVaultAction{"replace", entry.Name}
				remove := toolsVaultAction{"delete", entry.Name}
				stamp := toolsCaption(cx, "更新于 "+entry.UpdatedAt.Local().Format("2006-01-02 15:04:05"))
				stamp.TestID = "tools-vault-updated-" + entry.Name
				rows = append(rows, toolsRow(toolsLabel(entry.Name), stamp,
					core.Show{When: func() bool { return active.Get() == replace || active.Get() == remove },
						Then: func(*core.Scope) core.Node {
							return core.Dynamic(func(cx *core.Scope) core.Node {
								action := active.Get()
								if action == replace {
									return toolsVaultForm(cx, action, *entries, canEdit, closeForm, edit)
								}
								if action != remove {
									return core.Group{}
								}
								return toolsRow(toolsCaption(cx, "删除后，工具将无法使用此 Bot 的密钥值。"),
									toolsButton("tools-vault-delete-confirm", "确认删除密钥", ui.ButtonDanger,
										func() bool { return !canEdit() }, func() { edit(remove, entry.Name, "") }),
									toolsButton("tools-vault-cancel", "取消", ui.ButtonGhost, nil, closeForm))
							})
						}, Else: func(*core.Scope) core.Node {
							replaceButton := toolsButton("tools-vault-replace-"+entry.Name, "替换", ui.ButtonSecondary,
								func() bool { return !canEdit() }, func() { active.Set(replace) })
							replaceButton.Label = "替换密钥 " + entry.Name
							deleteButton := toolsButton("tools-vault-delete-"+entry.Name, "删除", ui.ButtonDanger,
								func() bool { return !canEdit() }, func() { active.Set(remove) })
							deleteButton.Label = "删除密钥 " + entry.Name
							return core.View{Class: "flex-row gap-2", Children: []core.Node{replaceButton, deleteButton}}
						}}))
			}
			return rows
		}),
	)
}

func toolsVaultForm(cx *core.Scope, form toolsVaultAction, entries []toolsVaultEntry,
	canEdit func() bool, closeForm func(), edit func(toolsVaultAction, string, string)) core.Node {
	name := core.NewSignal(cx, form.name)
	hasValue := core.NewSignal(cx, false)
	// The mounted field owns the value. Do not retain it as a draft or signal.
	var value string
	cx.Cleanup(func() { value = "" })
	nameError := func() string {
		key := strings.TrimSpace(name.Get())
		if form.kind != "create" || key == "" {
			return ""
		}
		if !toolsVaultName.MatchString(key) {
			return "名称必须以字母或下划线开头，只能包含英文字母、数字和下划线。"
		}
		for _, entry := range entries {
			if entry.Name == key {
				return "同名密钥已存在。请取消并选择替换。"
			}
		}
		return ""
	}
	disabled := func() bool {
		return !canEdit() || strings.TrimSpace(name.Get()) == "" || nameError() != "" || !hasValue.Get()
	}
	rows := []core.Node{}
	if form.kind == "create" {
		rows = append(rows, toolsLabel("密钥名称"),
			ui.Input{TestID: "tools-vault-name", Label: "密钥名称", Value: name.Get, OnChange: name.Set,
				NoAutoCorrect: true, AutoCapitalize: core.AutoCapitalizeNone, AutoComplete: core.AutoCompleteOff},
			core.Show{When: func() bool { return nameError() != "" }, Then: func(*core.Scope) core.Node {
				return core.Text{TestID: "tools-vault-name-error", Role: core.RoleStatus, ContentFunc: nameError}
			}})
	}
	return toolsRow(append(rows, toolsLabel("密钥值"),
		ui.Input{TestID: "tools-vault-value", Label: "密钥值", Masked: true, NoAutoCorrect: true,
			AutoCapitalize: core.AutoCapitalizeNone, AutoComplete: core.AutoCompleteOff,
			OnChange: func(text string) {
				value = text
				hasValue.Set(value != "")
			}},
		toolsButton("tools-vault-save", "保存密钥", ui.ButtonPrimary, disabled, func() {
			if disabled() {
				return
			}
			submitted := value
			value = ""
			hasValue.Set(false)
			edit(form, strings.TrimSpace(name.Get()), submitted)
		}),
		toolsButton("tools-vault-cancel", "取消", ui.ButtonGhost, nil, closeForm))...)
}
