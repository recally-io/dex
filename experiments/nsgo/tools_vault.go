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
		feed.run("Loading secrets…", "", "Could not load secrets.", get)
	}
	canEdit := func() bool { return feed.fresh.Get() && !feed.busy.Get() }
	edit := func(form toolsVaultAction, name, value string) {
		if !canEdit() || active.Get() != form {
			return
		}
		closeForm()
		method, endpoint := http.MethodPut, path
		pending, success, failure := "Saving secret…", "Secret saved. The change applies when the bot is idle.", "Could not confirm the save. Reload secrets before you try again."
		var body *toolsVaultWrite
		if form.kind == "delete" {
			method = http.MethodDelete
			endpoint += "?" + url.Values{"scope": {toolsVaultScope}, "name": {name}}.Encode()
			pending, success, failure = "Deleting secret…", "Secret deleted.", "Could not confirm the deletion. Reload secrets before you try again."
		} else {
			body = new(toolsVaultWrite{toolsVaultScope, name, value})
		}
		feed.run(pending, success, failure, func(ctx context.Context) ([]toolsVaultEntry, error) {
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
	active.Set(create)
	return botProfileSection(cx, "Secret vault", "These secrets are for this bot. A bot secret overrides a shared secret with the same name.",
		feed.status("tools-vault-status"),
		core.Show{When: func() bool { return !feed.busy.Get() && !feed.fresh.Get() }, Then: func(*core.Scope) core.Node {
			return toolsButton("tools-vault-refresh", "Try again", ui.ButtonOutline, nil, refresh)
		}},
		core.Dynamic(func(cx *core.Scope) core.Node {
			entries := feed.value.Get()
			if entries == nil {
				return core.Group{}
			}
			rows := core.Group{}
			rows = append(rows, core.Show{When: func() bool { return active.Get() == create },
				Then: func(cx *core.Scope) core.Node {
					return toolsVaultForm(cx, create, *entries, canEdit, closeForm, edit)
				}, Else: func(*core.Scope) core.Node {
					return toolsRow(toolsButton("tools-vault-add", "Add secret", ui.ButtonSecondary,
						func() bool { return !canEdit() }, func() { active.Set(create) }))
				}})
			if len(*entries) == 0 {
				rows = append(rows, toolsRow(toolsCaption(cx, "No secrets at this level.")))
			}
			for _, entry := range *entries {
				replace := toolsVaultAction{"replace", entry.Name}
				remove := toolsVaultAction{"delete", entry.Name}
				stamp := toolsCaption(cx, entry.UpdatedAt.Local().Format("2006-01-02 15:04:05"))
				stamp.TestID = "tools-vault-updated-" + entry.Name
				rows = append(rows, toolsRow(toolsLabel("vault:"+entry.Name), stamp,
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
								return toolsRow(toolsCaption(cx, "After deletion, this Bot's tools cannot use this secret value."),
									toolsButton("tools-vault-delete-confirm", "Delete secret", ui.ButtonDanger,
										func() bool { return !canEdit() }, func() { edit(remove, entry.Name, "") }),
									toolsButton("tools-vault-cancel", "Cancel", ui.ButtonGhost, nil, closeForm))
							})
						}, Else: func(*core.Scope) core.Node {
							replaceButton := toolsButton("tools-vault-replace-"+entry.Name, "Replace", ui.ButtonSecondary,
								func() bool { return !canEdit() }, func() { active.Set(replace) })
							replaceButton.Label = "Replace " + entry.Name
							deleteButton := toolsButton("tools-vault-delete-"+entry.Name, "Delete", ui.ButtonOutline,
								func() bool { return !canEdit() }, func() { active.Set(remove) })
							deleteButton.Label = "Delete " + entry.Name
							return core.View{Class: "flex-row gap-2", Children: []core.Node{replaceButton, deleteButton}}
						}}))
			}
			return botProfilePanel(cx, rows...)
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
			return "Use letters, numbers, and underscores. Start with a letter or underscore."
		}
		for _, entry := range entries {
			if entry.Name == key {
				return "A secret with this name exists. Choose Replace to change its value."
			}
		}
		return ""
	}
	disabled := func() bool {
		return !canEdit() || strings.TrimSpace(name.Get()) == "" || nameError() != "" || !hasValue.Get()
	}
	rows := []core.Node{}
	if form.kind == "create" {
		rows = append(rows, toolsLabel("Secret name"), toolsCaption(cx, "Use letters, numbers, and underscores. Start with a letter or underscore."),
			ui.Input{TestID: "tools-vault-name", Label: "Secret name", Value: name.Get, OnChange: name.Set,
				NoAutoCorrect: true, AutoCapitalize: core.AutoCapitalizeNone, AutoComplete: core.AutoCompleteOff},
			core.Show{When: func() bool { return nameError() != "" }, Then: func(*core.Scope) core.Node {
				return core.Text{TestID: "tools-vault-name-error", Role: core.RoleStatus, ContentFunc: nameError}
			}})
	}
	return toolsRow(append(rows, toolsLabel("Secret value"), toolsCaption(cx, "Dex encrypts the value. You cannot read a saved value here."),
		ui.Input{TestID: "tools-vault-value", Label: "Secret value", Masked: true, NoAutoCorrect: true,
			AutoCapitalize: core.AutoCapitalizeNone, AutoComplete: core.AutoCompleteOff,
			OnChange: func(text string) {
				value = text
				hasValue.Set(value != "")
			}},
		toolsButton("tools-vault-save", "Save secret", ui.ButtonPrimary, disabled, func() {
			if disabled() {
				return
			}
			submitted := value
			value = ""
			hasValue.Set(false)
			edit(form, strings.TrimSpace(name.Get()), submitted)
		}),
		toolsButton("tools-vault-cancel", "Cancel", ui.ButtonGhost, nil, closeForm))...)
}
