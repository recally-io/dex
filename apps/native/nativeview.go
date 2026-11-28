package main

import (
	"net/url"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/recally-io/dex/apps/native/locales"
)

const dexWebComponent = "DexWorkspaceWebView"

func dexWebView(cx *core.Scope, origin string, leave func(connectionPhase)) core.Node {
	active, alert := true, false
	cx.Cleanup(func() { active = false })
	return core.Native{
		Component: dexWebComponent,
		Props:     map[string]any{"origin": origin},
		TestID:    "dex-workspace",
		Style:     core.Style{FlexGrow: 1, FlexShrink: 1},
		OnEvent: func(event string, data any) {
			if !active {
				return
			}
			switch event {
			case "failure":
				leave(offline)
				return
			case "settings":
				leave(switchWorkspace)
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
