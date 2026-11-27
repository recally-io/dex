package main

import (
	"net/url"

	"github.com/nsgo-dev/nsgo"
	"github.com/nsgo-dev/nsgo/core"
)

const (
	dexHost   = "127.0.0.1"
	dexPort   = "19681"
	dexOrigin = "http://" + dexHost + ":" + dexPort
)

func main() { nsgo.Run(root, nsgo.WindowSize(1280, 800)) }

func root(cx *core.Scope) core.Node {
	cx.ProvideAccess(dexAccess{})
	ref := new(core.Ref)
	source := core.WebSrc(dexOrigin + "/")
	return core.WebView{
		Source: source,
		Style:  core.Style{Width: core.Pct(100), Height: core.Pct(100)},
		Ref:    ref,
		TestID: "dex",
		OnLoadStart: func(nav core.WebNavigation) {
			// NSGo checks the host before navigation, but not the scheme or port.
			// This can stop a reported load. It cannot prevent its first request.
			if nav.URL != "" && !isDexURL(nav.URL) {
				ref.StopLoading()
				ref.Load(source)
			}
		},
	}
}

type dexAccess struct{}

func (dexAccess) Allows(req core.AccessRequest) bool {
	return req.Permission == core.AccessWebView && req.Method == "GET" && isDexURL(req.Resource)
}

func (dexAccess) WebHosts() []string   { return []string{dexHost} }
func (dexAccess) WebSchemes() []string { return nil }

func isDexURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "http" && u.Host == dexHost+":"+dexPort && u.User == nil
}
