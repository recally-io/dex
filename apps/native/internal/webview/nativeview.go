// Package webview hosts the Dex workspace in a native view.
package webview

import (
	"net/http"
	"net/url"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/recally-io/dex/apps/native/internal/bridge"
	"github.com/recally-io/dex/apps/native/locales"
)

const dexWebComponent = "DexWorkspaceWebView"

// LocalTarget identifies the local workspace in the saved workspace selection.
const LocalTarget = "local"

// SettingsURL opens the app's workspace settings.
const SettingsURL = "dex-workspace://settings"

// View opens a workspace. Only LocalTarget uses the local handler.
func View(cx *core.Scope, origin string, handler http.Handler, onFailure, onSettings func()) core.Node {
	active, alert := true, false
	cx.Cleanup(func() { active = false })
	ref := new(core.Ref)
	var local *bridge.Bridge
	if origin == LocalTarget {
		local = bridge.New(handler)
	}
	ctx := cx.Context()
	send := func(command string, value any) {
		cx.Post(func() { ref.Command(command, value) })
	}
	return core.Native{
		Component: dexWebComponent,
		Props:     map[string]any{"origin": origin},
		Ref:       ref,
		TestID:    "dex-workspace",
		Style:     core.Style{FlexGrow: 1, FlexShrink: 1},
		OnEvent: func(event string, data any) {
			if !active {
				return
			}
			switch event {
			case "localMessage", "localStatic":
				if raw, ok := data.(string); ok && local != nil {
					local.HandleMessage(ctx, raw, event == "localStatic", send)
				}
				return
			case "failure":
				onFailure()
				return
			case "settings":
				onSettings()
				return
			}
			if alert {
				return
			}
			var title, message, target string
			switch event {
			case "external":
				link, ok := data.(string)
				if !ok {
					return
				}
				u, err := url.Parse(link)
				if err != nil {
					return
				}
				title, message, target = locales.NavExternalTitle(cx), locales.NavExternalMessage(cx, u.Host), link
			case "blocked":
				if local != nil {
					return
				}
				title, message, target = locales.NavBlockedTitle(cx), locales.NavBlockedMessage(cx), origin
			default:
				return
			}
			// An activated link is not proof of trusted input. Only the native
			// confirmation may open a URL. The browser gets no request headers.
			alert = true
			cx.ShowAlert(core.AlertConfig{
				Title: title, Message: message, Cancelable: true,
				Actions: []core.AlertAction{{Label: locales.NavCancel(cx)}, {Label: locales.NavOpenBrowser(cx)}},
			}, func(index int) {
				alert = false
				if active && index == 1 {
					cx.OpenURL(target, nil)
				}
			})
		},
	}
}
