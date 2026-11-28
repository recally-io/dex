package main

import (
	"errors"

	"github.com/nsgo-dev/nsgo"
	"github.com/nsgo-dev/nsgo/core"
)

func dexWebViewOptions() []nsgo.Option {
	return []nsgo.Option{nsgo.Component(dexWebComponent, func(cx *core.NativeContext) core.NativeComponent {
		return androidWorkspace{emit: cx.Emit}
	})}
}

type androidWorkspace struct{ emit func(string, any) }

func (androidWorkspace) View() any           { return "io.recally.dex.WorkspaceView" }
func (androidWorkspace) SetProp(string, any) {}
func (androidWorkspace) Command(string, any) {}
func (androidWorkspace) Close()              {}

func (w androidWorkspace) ViewEvent(name string, args []any) {
	switch name {
	case "settings", "failure", "blocked":
		w.emit(name, nil)
	case "external", "localMessage", "localStatic":
		if len(args) == 1 {
			w.emit(name, args[0])
		}
	}
}

func clearDexWebView(cx *core.Scope, oldOrigin string, done func(error)) {
	// Android shares cookies across ports. Wait for the delete and flush before
	// the next view loads, even if the two servers have the same hostname.
	cx.ClearWebsiteData(func(err error) {
		if err != nil {
			done(err)
			return
		}
		cx.WebCookies(core.WebCookieFilter{URL: oldOrigin + "/api/health"}, func(cookies []core.WebCookie, err error) {
			if err == nil && len(cookies) != 0 {
				err = errors.New("Android did not clear the workspace cookies")
			}
			done(err)
		})
	})
}
