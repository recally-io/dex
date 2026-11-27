package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

var settingsSecretName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type settingsSecret struct {
	Name      string    `json:"name"`
	Scope     string    `json:"scope"`
	UpdatedAt time.Time `json:"updatedAt"`
}
type settingsSecretsData struct {
	entries        []settingsSecret
	mode           string
	cli            bool
	onePasswordErr bool
}

func settingsSecrets(cx *core.Scope) core.Node {
	path := botPath("default") + "/vault"
	f := newSettingsFeed(cx, func(ctx context.Context) (settingsSecretsData, error) {
		var d settingsSecretsData
		var err error
		var reads sync.WaitGroup
		reads.Go(func() {
			var entries []settingsSecret
			err = requestJSON(ctx, http.MethodGet, path, nil, &entries)
			for _, e := range entries {
				if e.Scope == "dex" {
					d.entries = append(d.entries, e)
				}
			}
		})
		reads.Go(func() {
			var state struct {
				Mode string `json:"mode"`
				CLI  string `json:"cli"`
			}
			d.onePasswordErr = requestJSON(ctx, http.MethodGet, "/api/settings/onepassword", nil, &state) != nil
			d.mode, d.cli = state.Mode, state.CLI != ""
		})
		reads.Wait()
		return d, err
	})
	type action struct{ kind, name string }
	form := core.NewSignal(cx, action{})
	close := func() { form.Set(action{}) }
	return settingsShow(f, "secrets", func(cx *core.Scope, data settingsSecretsData) core.Node {
		rows := []core.Node{ui.Button{Text: "Add secret", TestID: "global-secret-add", Style: core.Style{AlignSelf: core.AlignStart}, Disabled: f.busy.Get, OnPress: func() { form.Set(action{kind: "add"}) }}}
		if len(data.entries) == 0 {
			rows = append(rows, globalMuted(cx, "No shared secrets yet."))
		}
		for _, entry := range data.entries {
			updated := ""
			if !entry.UpdatedAt.IsZero() {
				updated = "Updated " + entry.UpdatedAt.Local().Format("2 Jan 2006, 15:04")
			}
			rows = append(rows, settingsRow(cx, entry.Name, updated,
				ui.Button{Text: "Replace", TestID: "global-secret-replace-" + entry.Name, Variant: ui.ButtonSecondary, Disabled: f.busy.Get, OnPress: func() { form.Set(action{"replace", entry.Name}) }},
				ui.Button{Text: "Remove", TestID: "global-secret-remove-" + entry.Name, Variant: ui.ButtonGhost, Disabled: f.busy.Get, OnPress: func() { form.Set(action{"remove", entry.Name}) }}))
		}
		state := "Mode: " + data.mode
		if data.onePasswordErr {
			state = "Could not load 1Password status."
		} else if data.cli {
			state += " · CLI available"
		} else {
			state += " · CLI unavailable"
		}
		return core.Group{globalSection(cx, "global-secret-vault", "Secret vault", "Values are write-only. Refer to shared secrets with vault:NAME.", rows),
			globalSection(cx, "global-onepassword", "1Password", "Shared 1Password connection status.", []core.Node{globalMuted(cx, state)}),
			core.Dynamic(func(cx *core.Scope) core.Node {
				a := form.Get()
				switch a.kind {
				case "remove":
					return settingsConfirm(cx, "Remove "+a.name+"?", "Bots that use this shared secret will no longer have access to it.", "global-secret-confirm-remove", f.busy.Get, f.status.Get, close, func() {
						f.run(func(ctx context.Context) error {
							q := url.Values{"scope": {"dex"}, "name": {a.name}}
							return requestJSON(ctx, http.MethodDelete, path+"?"+q.Encode(), nil, nil)
						}, "Secret removed.", close)
					})
				case "add", "replace":
					return settingsSecretForm(cx, a.kind, a.name, data.entries, f.busy.Get, f.status.Get, close, func(name, value string) {
						body := struct {
							Scope string `json:"scope"`
							Name  string `json:"name"`
							Value string `json:"value"`
						}{"dex", name, value}
						f.run(func(ctx context.Context) error {
							err := requestJSON(ctx, http.MethodPut, path, body, nil)
							body.Value = ""
							if err != nil {
								return errors.New("Could not save the secret. Refresh before retrying.")
							}
							return nil
						}, "Secret saved.", close)
					})
				default:
					return core.Group{}
				}
			}),
		}
	})
}

func settingsSecretForm(cx *core.Scope, kind, original string, entries []settingsSecret, busy func() bool, status func() string, close func(), save func(string, string)) core.Node {
	name := core.NewSignal(cx, original)
	hasValue := core.NewSignal(cx, false)
	var value string
	cx.Cleanup(func() { value = "" })
	valid := func() bool {
		key := strings.TrimSpace(name.Get())
		return settingsSecretName.MatchString(key) && (kind != "add" || !slices.ContainsFunc(entries, func(s settingsSecret) bool { return s.Name == key }))
	}
	disabled := func() bool { return busy() || !valid() || !hasValue.Get() }
	title := "Add secret"
	var rows []core.Node
	if kind == "add" {
		rows = append(rows, ui.Input{Label: "Name", TestID: "global-secret-name", Value: name.Get, OnChange: name.Set, ReadonlyFunc: busy}, globalMuted(cx, "Use letters, digits and underscores. Start with a letter or underscore."))
	} else {
		title = "Replace " + original
	}
	rows = append(rows, ui.Input{Label: "Value", TestID: "global-secret-value", Masked: true, ReadonlyFunc: busy, OnChange: func(text string) { value = text; hasValue.Set(value != "") }})
	return settingsDialog(title, "The saved value is never shown.", "global-secret-dialog", busy, status, close, rows, ui.Button{Text: "Save secret", TestID: "global-secret-save", Disabled: disabled, Busy: busy, OnPress: func() {
		if !disabled() {
			save(strings.TrimSpace(name.Get()), value)
		}
	}})
}

type settingsAboutData struct {
	Version   string `json:"version"`
	StartedAt int64  `json:"startedAt"`
	State     string `json:"state"`
	Channel   string `json:"updateChannel"`
	Available bool   `json:"updateAvailable"`
	Backups   []struct {
		Name    string `json:"name"`
		Size    int64  `json:"sizeBytes"`
		Created int64  `json:"createdAt"`
	} `json:"backups"`
}

func settingsAbout(cx *core.Scope) core.Node {
	f := newSettingsFeed(cx, settingsGET[settingsAboutData]("/api/system/status"))
	prefs := []ui.Option[core.Appearance]{{Value: core.AppearanceSystem, Label: "System"}, {Value: core.AppearanceLight, Label: "Light"}, {Value: core.AppearanceDark, Label: "Dark"}}
	appearance := globalSection(cx, "global-about-appearance", "Appearance", "Applies to this app window.", []core.Node{settingsRow(cx, "Theme", "", ui.Select[core.Appearance]{Label: "Theme", TestID: "global-about-theme", Options: prefs, Value: func() core.Appearance { return cx.Env().Appearance() }, OnChange: cx.SetAppearance})})
	details := settingsShow(f, "about", func(cx *core.Scope, data settingsAboutData) core.Node {
		started := "—"
		if data.StartedAt != 0 {
			started = time.UnixMilli(data.StartedAt).Local().Format("2 Jan 2006, 15:04")
		}
		state := map[string]string{"idle": "Running", "checking": "Checking…", "downloading": "Downloading…", "waiting_idle": "Waiting for active runs", "restarting": "Restarting…", "error": "Attention needed"}[data.State]
		if state == "" {
			state = data.State
		}
		update := "No update reported"
		if data.Available {
			update = "Update available"
		}
		var backups []core.Node
		for _, b := range data.Backups {
			backups = append(backups, settingsRow(cx, b.Name, fmt.Sprintf("%d bytes · %s", b.Size, time.UnixMilli(b.Created).Local().Format("2 Jan 2006, 15:04"))))
		}
		if len(backups) == 0 {
			backups = append(backups, globalMuted(cx, "No database backups yet."))
		}
		return core.Group{globalSection(cx, "global-about-server", "This server", "", []core.Node{settingsRow(cx, "Version", data.Version), settingsRow(cx, "Status", state), settingsRow(cx, "Started", started), settingsRow(cx, "Update channel", data.Channel), globalMuted(cx, update)}),
			globalSection(cx, "global-about-backups", "Database backups", "Backups of the Dex control database.", backups)}
	})
	return core.Group{appearance, details}
}
