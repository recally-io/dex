package main

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

type settingsProvider struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	API        string   `json:"api"`
	BaseURL    string   `json:"baseUrl"`
	Models     []string `json:"models"`
	ModelCount int      `json:"modelCount"`
	KeySet     bool     `json:"keySet"`
	Error      string   `json:"error"`
}
type settingsConnectionData struct {
	Providers []settingsProvider `json:"providers"`
	Builtins  []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"builtins"`
	APIs map[string]string `json:"apis"`
}

func settingsProviders(cx *core.Scope) core.Node {
	f := newSettingsFeed(cx, settingsGET[settingsConnectionData]("/api/settings"))
	edit := core.NewSignal(cx, (*settingsProvider)(nil))
	remove := core.NewSignal(cx, (*settingsProvider)(nil))
	path := func(id string) string { return "/api/settings/providers/" + url.PathEscape(id) }
	return settingsShow(f, "providers", func(cx *core.Scope, data settingsConnectionData) core.Node {
		rows := []core.Node{ui.Button{Text: "Add provider", TestID: "global-provider-add", Disabled: f.busy.Get, Style: core.Style{AlignSelf: core.AlignStart}, OnPress: func() { edit.Set(new(settingsProvider{})) }}}
		if len(data.Providers) == 0 {
			rows = append(rows, globalRow(cx, "global-providers-empty", "No providers yet", "Add a built-in service or a custom endpoint."))
		}
		for _, p := range data.Providers {
			detail := p.ID + " · " + strconv.Itoa(p.ModelCount) + " models"
			if p.KeySet {
				detail += " · Key saved"
			}
			if p.Error != "" {
				detail += "\n" + p.Error
			}
			rows = append(rows, settingsRow(cx, p.Name, detail, ui.Button{Text: "Edit", TestID: "global-provider-edit-" + p.ID, Variant: ui.ButtonSecondary, Disabled: f.busy.Get, OnPress: func() { edit.Set(new(p)) }}, ui.Button{Text: "Remove", TestID: "global-provider-remove-" + p.ID, Variant: ui.ButtonGhost, Disabled: f.busy.Get, OnPress: func() { remove.Set(new(p)) }}))
		}
		return core.Group{globalSection(cx, "global-providers-list", "Connections", "", rows), globalSection(cx, "global-provider-keys", "Keys", "Write-only.", []core.Node{globalRow(cx, "global-provider-keys-info", "Shown in the web", "Never. Only whether a key is saved.")}),
			core.Dynamic(func(cx *core.Scope) core.Node {
				if p := edit.Get(); p != nil {
					return settingsProviderForm(cx, *p, data, f, func(id string, body any) {
						f.run(func(ctx context.Context) error { return requestJSON(ctx, http.MethodPut, path(id), body, nil) }, "Provider saved.", func() { edit.Set(nil) })
					}, func() { edit.Set(nil) })
				}
				return core.Group{}
			}),
			core.Dynamic(func(cx *core.Scope) core.Node {
				if p := remove.Get(); p != nil {
					return settingsConfirm(cx, "Remove "+p.Name+"?", "Bots that use this provider can lose access to their models.", "global-provider-confirm-remove", f.busy.Get, f.status.Get, func() { remove.Set(nil) }, func() {
						f.run(func(ctx context.Context) error { return requestJSON(ctx, http.MethodDelete, path(p.ID), nil, nil) }, "Provider removed.", func() { remove.Set(nil) })
					})
				}
				return core.Group{}
			}),
		}
	})
}

func settingsProviderForm(cx *core.Scope, p settingsProvider, data settingsConnectionData, f *settingsFeed[settingsConnectionData], save func(string, any), close func()) core.Node {
	kind := core.NewSignal(cx, p.API)
	id := core.NewSignal(cx, p.ID)
	name := core.NewSignal(cx, p.Name)
	endpoint := core.NewSignal(cx, p.BaseURL)
	hasKey := core.NewSignal(cx, false)
	var key string
	cx.Cleanup(func() { key = "" })
	custom := func() bool { return kind.Get() != "" }
	variants := []ui.Option[string]{{Label: "Built-in service"}}
	for _, a := range slices.Sorted(maps.Keys(data.APIs)) {
		variants = append(variants, ui.Option[string]{Value: a, Label: data.APIs[a]})
	}
	builtin := make([]ui.Option[string], 0, len(data.Builtins))
	for _, p := range data.Builtins {
		builtin = append(builtin, ui.Option[string]{Value: p.ID, Label: p.Name})
	}
	duplicate := func() bool {
		return p.ID == "" && slices.ContainsFunc(data.Providers, func(saved settingsProvider) bool { return saved.ID == strings.TrimSpace(id.Get()) })
	}
	canSave := func() bool {
		return !f.busy.Get() && !duplicate() && strings.TrimSpace(id.Get()) != "" && (!custom() || strings.TrimSpace(endpoint.Get()) != "") && (custom() || p.KeySet || hasKey.Get())
	}
	onSave := func() {
		if !canSave() {
			return
		}
		submitted := struct {
			API     string   `json:"api"`
			Name    string   `json:"name"`
			BaseURL string   `json:"baseUrl"`
			Key     string   `json:"apiKey"`
			Models  []string `json:"models"`
		}{kind.Get(), strings.TrimSpace(name.Get()), strings.TrimSpace(endpoint.Get()), key, p.Models}
		save(strings.TrimSpace(id.Get()), submitted)
	}
	title := "Add provider"
	if p.ID != "" {
		title = "Edit provider"
	}
	return settingsDialog(title, "API keys are write-only.", "global-provider-dialog", f.busy.Get, f.status.Get, close, []core.Node{
		ui.Select[string]{TestID: "global-provider-api", Label: "Provider type", Options: variants, Value: kind.Get, OnChange: func(next string) { kind.Set(next); id.Set("") }, Disabled: func() bool { return f.busy.Get() || p.ID != "" }},
		core.Show{When: custom, Then: func(*core.Scope) core.Node {
			return core.Group{ui.Input{TestID: "global-provider-id", Label: "Provider ID", Value: id.Get, OnChange: id.Set, ReadonlyFunc: func() bool { return f.busy.Get() || p.ID != "" }}, ui.Input{TestID: "global-provider-url", Label: "Base URL", Value: endpoint.Get, OnChange: endpoint.Set, ReadonlyFunc: f.busy.Get}}
		}, Else: func(*core.Scope) core.Node {
			return ui.Select[string]{Label: "Built-in provider", TestID: "global-provider-builtin", Placeholder: "Choose provider", Options: builtin, Value: id.Get, OnChange: id.Set, Disabled: func() bool { return f.busy.Get() || p.ID != "" }}
		}},
		ui.Input{TestID: "global-provider-name", Label: "Name", Value: name.Get, OnChange: name.Set, ReadonlyFunc: f.busy.Get},
		ui.Input{TestID: "global-provider-key", Label: "API key", Masked: true, ReadonlyFunc: f.busy.Get, OnChange: func(s string) { key = s; hasKey.Set(s != "") }},
		core.Show{When: duplicate, Then: func(cx *core.Scope) core.Node {
			return globalMuted(cx, "This provider is already saved. Use Edit to change it.")
		}},
	}, ui.Button{Text: "Save provider", TestID: "global-provider-save", Busy: f.busy.Get, Disabled: func() bool { return !canSave() }, OnPress: onSave})
}

type settingsRunner struct {
	ID     string         `json:"id"`
	Name   string         `json:"name"`
	Kind   string         `json:"kind"`
	Desc   string         `json:"desc"`
	Config jsontext.Value `json:"config"`
	Runner *struct {
		Online bool `json:"online"`
	} `json:"runner"`
}
type settingsRunnerList struct {
	Runners []settingsRunner `json:"runners"`
}

func settingsRunners(cx *core.Scope) core.Node {
	const endpoint = "/api/settings/runners"
	f := newSettingsFeed(cx, settingsGET[settingsRunnerList](endpoint))
	edit := core.NewSignal(cx, (*settingsRunner)(nil))
	remove := core.NewSignal(cx, (*settingsRunner)(nil))
	probe := core.NewSignal(cx, "")
	return settingsShow(f, "runners", func(cx *core.Scope, data settingsRunnerList) core.Node {
		rows := []core.Node{settingsRow(cx, "Local", "Runs tools and workspaces on this computer."), ui.Button{Text: "Add Runner", TestID: "global-runner-add", Disabled: f.busy.Get, Style: core.Style{AlignSelf: core.AlignStart}, OnPress: func() { edit.Set(new(settingsRunner{Kind: "ssh"})) }}}
		for _, r := range data.Runners {
			detail := r.Kind
			if r.Desc != "" {
				detail += " · " + r.Desc
			}
			if r.Runner != nil {
				if r.Runner.Online {
					detail += " · Online"
				} else {
					detail += " · Offline"
				}
			}
			path := endpoint + "/" + url.PathEscape(r.ID)
			rows = append(rows, settingsRow(cx, r.Name, detail,
				ui.Button{Text: "Test connection", TestID: "global-runner-test-" + r.ID, Variant: ui.ButtonSecondary, Disabled: f.busy.Get, OnPress: func() {
					if f.busy.Get() {
						return
					}
					f.busy.Set(true)
					probe.Set("Testing connection…")
					work(f.cx, func(ctx context.Context) (string, error) {
						var result struct {
							OK       bool   `json:"ok"`
							Error    string `json:"error"`
							Platform string `json:"platform"`
						}
						err := requestJSON(ctx, http.MethodPost, path+"/test", nil, &result)
						if result.OK {
							return "Connection works. " + result.Platform, err
						}
						return "Connection test failed. " + result.Error, err
					}, func(message string, err error) {
						f.busy.Set(false)
						if err != nil {
							message = err.Error()
						}
						probe.Set(message)
					})
				}},
				ui.Button{Text: "Edit", TestID: "global-runner-edit-" + r.ID, Variant: ui.ButtonGhost, Disabled: f.busy.Get, OnPress: func() { edit.Set(new(r)) }}, ui.Button{Text: "Remove", TestID: "global-runner-remove-" + r.ID, Variant: ui.ButtonGhost, Disabled: f.busy.Get, OnPress: func() { remove.Set(new(r)) }}))
		}
		rows = append(rows, core.Text{ContentFunc: probe.Get, Role: core.RoleStatus})
		return core.Group{globalSection(cx, "global-runners-list", "Available Runners", "", rows), core.Dynamic(func(cx *core.Scope) core.Node {
			if r := edit.Get(); r != nil {
				return settingsRunnerForm(cx, *r, f, func(body any) {
					method, path := http.MethodPost, endpoint
					if r.ID != "" {
						method, path = http.MethodPut, endpoint+"/"+url.PathEscape(r.ID)
					}
					f.run(func(ctx context.Context) error { return requestJSON(ctx, method, path, body, nil) }, "Runner saved.", func() { edit.Set(nil) })
				}, func() { edit.Set(nil) })
			}
			return core.Group{}
		}),
			core.Dynamic(func(cx *core.Scope) core.Node {
				if r := remove.Get(); r != nil {
					return settingsConfirm(cx, "Remove "+r.Name+"?", "A Runner in use by a bot cannot be removed.", "global-runner-confirm-remove", f.busy.Get, f.status.Get, func() { remove.Set(nil) }, func() {
						f.run(func(ctx context.Context) error {
							return requestJSON(ctx, http.MethodDelete, endpoint+"/"+url.PathEscape(r.ID), nil, nil)
						}, "Runner removed.", func() { remove.Set(nil) })
					})
				}
				return core.Group{}
			})}
	})
}

func settingsRunnerForm(cx *core.Scope, r settingsRunner, f *settingsFeed[settingsRunnerList], save func(any), close func()) core.Node {
	name, desc := core.NewSignal(cx, r.Name), core.NewSignal(cx, r.Desc)
	type sshConfig struct {
		Host      string `json:"host"`
		User      string `json:"user"`
		Workspace string `json:"workspaceDir"`
		Port      int    `json:"port"`
	}
	var config sshConfig
	if r.Kind == "ssh" && len(r.Config) > 0 {
		_ = json.Unmarshal(r.Config, &config)
	}
	host, user, dir := core.NewSignal(cx, config.Host), core.NewSignal(cx, config.User), core.NewSignal(cx, config.Workspace)
	canSave := func() bool {
		return !f.busy.Get() && strings.TrimSpace(name.Get()) != "" && (r.ID != "" || strings.TrimSpace(host.Get()) != "" && strings.TrimSpace(user.Get()) != "")
	}
	rows := []core.Node{ui.Input{TestID: "global-runner-name", Label: "Name", Value: name.Get, OnChange: name.Set, ReadonlyFunc: f.busy.Get}, ui.Input{TestID: "global-runner-desc", Label: "Description", Value: desc.Get, OnChange: desc.Set, ReadonlyFunc: f.busy.Get}}
	title := "Edit Runner"
	if r.ID == "" {
		title = "Add SSH Runner"
		rows = append(rows, ui.Input{TestID: "global-runner-host", Label: "Host", Value: host.Get, OnChange: host.Set, ReadonlyFunc: f.busy.Get}, ui.Input{TestID: "global-runner-user", Label: "User", Value: user.Get, OnChange: user.Set, ReadonlyFunc: f.busy.Get}, ui.Input{TestID: "global-runner-dir", Label: "Workspace directory", Value: dir.Get, OnChange: dir.Set, ReadonlyFunc: f.busy.Get})
	}
	return settingsDialog(title, "", "global-runner-dialog", f.busy.Get, f.status.Get, close, rows, ui.Button{Text: "Save Runner", TestID: "global-runner-save", Busy: f.busy.Get, Disabled: func() bool { return !canSave() }, OnPress: func() {
		if !canSave() {
			return
		}
		var saved any = r.Config
		if len(r.Config) == 0 || r.ID == "" {
			saved = sshConfig{strings.TrimSpace(host.Get()), strings.TrimSpace(user.Get()), strings.TrimSpace(dir.Get()), 22}
		}
		save(struct {
			Name   string `json:"name"`
			Desc   string `json:"desc"`
			Kind   string `json:"kind"`
			Config any    `json:"config"`
		}{strings.TrimSpace(name.Get()), desc.Get(), r.Kind, saved})
	}})
}

func settingsDialog(title, description, id string, busy func() bool, status func() string, close func(), fields []core.Node, action ui.Button) core.Node {
	cancel := func() {
		if !busy() {
			close()
		}
	}
	fields = append(fields, core.Text{ContentFunc: status, Role: core.RoleStatus})
	return ui.Dialog{Title: title, Description: description, TestID: id, Open: func() bool { return true }, OnOpenChange: func(open bool, _ base.Reason) {
		if !open {
			cancel()
		}
	}, NoCloseOnBackdropPress: true, Children: fields, Footer: []core.Node{ui.Button{Text: "Cancel", TestID: id + "-cancel", Variant: ui.ButtonSecondary, Disabled: busy, OnPress: cancel}, action}}
}

func settingsConfirm(cx *core.Scope, title, description, id string, busy func() bool, status func() string, close, confirm func()) core.Node {
	return settingsDialog(title, description, id+"-dialog", busy, status, close, nil, ui.Button{Text: "Remove", TestID: id, Variant: ui.ButtonDanger, Busy: busy, OnPress: confirm})
}
