//go:build darwin && !tvos && !watchos

package main

import (
	"crypto/sha256"
	"errors"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"uuid"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/nsgo-dev/nsgo"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/recally-io/dex/clients/native/locales"
)

const dexWebComponent = "DexWorkspaceWebView"

func dexWebViewOptions() []nsgo.Option {
	return []nsgo.Option{nsgo.Component(dexWebComponent, func(cx *core.NativeContext) core.NativeComponent {
		class := "NSView"
		if runtime.GOOS == "ios" {
			class = "UIView"
		}
		return &dexWorkspaceHost{container: dexClass(class).Send(dexSel("new")), emit: cx.Emit}
	})}
}

func dexWebView(cx *core.Scope, origin string, onFailure func()) core.Node {
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
			if event == "failure" {
				onFailure()
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

func clearDexWebView(cx *core.Scope, oldOrigin string, done func(error)) {
	answer := func(err error) { cx.Post(func() { done(err) }) }
	canonical, ok := canonicalOrigin(oldOrigin, runtime.GOOS == "darwin")
	if !ok || canonical != oldOrigin {
		answer(errors.New("cannot clear the Dex data store without its origin"))
		return
	}
	if err := dexOnAppleMain(func() { dexRemoveStore(dexStoreID(canonical), answer) }); err != nil {
		answer(err)
	}
}

func dexRemoveStore(id uuid.UUID, done func(error)) {
	for _, w := range dexWebViews {
		if w.storeID == id {
			done(errors.New("the previous Dex web view is still mounted"))
			return
		}
	}
	if err := dexLoadWebKit(); err != nil {
		done(err)
		return
	}
	store, err := dexWebsiteStore(id)
	if err != nil {
		done(err)
		return
	}
	// WebKit can keep a named store alive after its last view is released.
	// Clear its data through that same live store and verify the result.
	store.Send(dexSel("retain"))
	types := dexClass("WKWebsiteDataStore").Send(dexSel("allWebsiteDataTypes")).Send(dexSel("retain"))
	answer := func(err error) {
		types.Send(dexSel("release"))
		store.Send(dexSel("release"))
		done(err)
	}
	finished := objc.NewBlock(func(objc.Block) {
		verified := objc.NewBlock(func(_ objc.Block, records objc.ID) {
			if records == 0 || objc.Send[uint](records, dexSel("count")) != 0 {
				answer(errors.New("WebKit did not clear the Dex website data"))
				return
			}
			cookies := store.Send(dexSel("httpCookieStore"))
			if cookies == 0 {
				answer(errors.New("WebKit did not return the Dex cookie store"))
				return
			}
			checked := objc.NewBlock(func(_ objc.Block, list objc.ID) {
				if list == 0 || objc.Send[uint](list, dexSel("count")) != 0 {
					answer(errors.New("WebKit did not clear the Dex cookies"))
					return
				}
				answer(nil)
			})
			defer checked.Release()
			cookies.Send(dexSel("getAllCookies:"), checked)
		})
		defer verified.Release()
		store.Send(dexSel("fetchDataRecordsOfTypes:completionHandler:"), types, verified)
	})
	defer finished.Release()
	past := dexClass("NSDate").Send(dexSel("distantPast"))
	store.Send(dexSel("removeDataOfTypes:modifiedSince:completionHandler:"), types, past, finished)
}

var dexSel = objc.RegisterName

func dexClass(name string) objc.ID { return objc.ID(objc.GetClass(name)) }

func dexNSString(text string) objc.ID {
	return dexClass("NSString").Send(dexSel("stringWithUTF8String:"), text)
}

func dexGoString(id objc.ID) string {
	if id == 0 {
		return ""
	}
	return objc.Send[string](id, dexSel("UTF8String"))
}

func dexStoreID(origin string) uuid.UUID {
	// A stable version 8 UUID keeps login on retry without sharing cookies
	// between different schemes, hosts or ports, even if the app stops during a switch.
	hash := sha256.Sum256([]byte("dex/apple/workspace/" + origin))
	id := uuid.UUID(hash[:16])
	id[6] = id[6]&0x0f | 0x80
	id[8] = id[8]&0x3f | 0x80
	return id
}

func dexStoreIdentifier(id uuid.UUID) objc.ID {
	return dexClass("NSUUID").Send(dexSel("alloc")).Send(dexSel("initWithUUIDString:"), dexNSString(id.String())).Send(dexSel("autorelease"))
}

var dexLoadWebKit = sync.OnceValue(func() error {
	_, err := purego.Dlopen("/System/Library/Frameworks/WebKit.framework/WebKit", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	return err
})

type dexMainQueue struct {
	id    uintptr
	async func(uintptr, objc.Block)
}

var dexGetMainQueue = sync.OnceValues(func() (dexMainQueue, error) {
	lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return dexMainQueue{}, err
	}
	queue, err := purego.Dlsym(lib, "_dispatch_main_q")
	if err != nil {
		return dexMainQueue{}, err
	}
	var async func(uintptr, objc.Block)
	purego.RegisterLibFunc(&async, lib, "dispatch_async")
	return dexMainQueue{queue, async}, nil
})

func dexOnAppleMain(fn func()) error {
	q, err := dexGetMainQueue()
	if err != nil {
		return err
	}
	work := objc.NewBlock(func(objc.Block) { fn() })
	defer work.Release()
	q.async(q.id, work)
	return nil
}

type dexOrigin struct {
	scheme string
	host   string
	port   uint16
}

func dexParseOrigin(raw string) (dexOrigin, *url.URL) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") {
		return dexOrigin{}, nil
	}
	var port uint64
	switch u.Scheme {
	case "https":
		port = 443
	case "http":
		port = 80
	default:
		return dexOrigin{}, nil
	}
	if p := u.Port(); p != "" {
		port, err = strconv.ParseUint(p, 10, 16)
		if err != nil || port == 0 {
			return dexOrigin{}, nil
		}
	}
	return dexOrigin{u.Scheme, strings.ToLower(u.Hostname()), uint16(port)}, u
}

type dexWorkspaceHost struct {
	container objc.ID
	page      *dexAppleWebView
	emit      func(string, any)
}

func (h *dexWorkspaceHost) View() any { return h.container }

func (h *dexWorkspaceHost) SetProp(name string, value any) {
	if name != "origin" || h.page != nil {
		return
	}
	raw, _ := value.(string)
	canonical, ok := canonicalOrigin(raw, runtime.GOOS == "darwin")
	if !ok || canonical != raw {
		h.emit("failure", nil)
		return
	}
	origin, _ := dexParseOrigin(canonical)
	page, err := newDexAppleWebView(origin, dexStoreID(canonical), h.emit)
	if err != nil {
		h.emit("failure", nil)
		return
	}
	h.page = page
	const flexibleWidthAndHeight = 2 | 16 // AppKit and UIKit
	page.view.Send(dexSel("setAutoresizingMask:"), uint(flexibleWidthAndHeight))
	page.view.Send(dexSel("setFrame:"), objc.Send[dexRect](h.container, dexSel("bounds")))
	h.container.Send(dexSel("addSubview:"), page.view)
	page.load(canonical, "GET")
}

func (*dexWorkspaceHost) Command(string, any) {}

func (h *dexWorkspaceHost) Close() {
	if h.page != nil {
		h.page.close()
	}
	h.container.Send(dexSel("release"))
}

type dexAppleWebView struct {
	view     objc.ID
	delegate objc.ID
	origin   dexOrigin
	storeID  uuid.UUID
	emit     func(string, any)
}

// Only the Apple main thread accesses the delegates and their views.
var dexWebViews = map[objc.ID]*dexAppleWebView{}

var dexWebDelegate = sync.OnceValues(func() (objc.Class, error) {
	return objc.RegisterClass("DexWorkspaceNavigationDelegate", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
		{Cmd: dexSel("webView:decidePolicyForNavigationAction:decisionHandler:"), Fn: dexNavigationAction},
		{Cmd: dexSel("webView:decidePolicyForNavigationResponse:decisionHandler:"), Fn: dexNavigationResponse},
		{Cmd: dexSel("webView:createWebViewWithConfiguration:forNavigationAction:windowFeatures:"),
			Fn: func(_ objc.ID, _ objc.SEL, _, _, _, _ objc.ID) objc.ID { return 0 }},
		{Cmd: dexSel("webView:didFinishNavigation:"), Fn: func(self objc.ID, _ objc.SEL, _, _ objc.ID) {
			if w := dexWebViews[self]; w != nil {
				w.emit("finished", nil)
			}
		}},
		{Cmd: dexSel("webView:didFailNavigation:withError:"), Fn: dexNavigationFailure},
		{Cmd: dexSel("webView:didFailProvisionalNavigation:withError:"), Fn: dexNavigationFailure},
		{Cmd: dexSel("webViewWebContentProcessDidTerminate:"), Fn: func(self objc.ID, _ objc.SEL, _ objc.ID) {
			if w := dexWebViews[self]; w != nil {
				w.emit("failure", nil)
			}
		}},
	})
})

func newDexAppleWebView(origin dexOrigin, id uuid.UUID, emit func(string, any)) (*dexAppleWebView, error) {
	if err := dexLoadWebKit(); err != nil {
		return nil, err
	}
	delegate, err := dexWebDelegate()
	if err != nil {
		return nil, err
	}
	store, err := dexWebsiteStore(id)
	if err != nil {
		return nil, err
	}
	config := dexClass("WKWebViewConfiguration").Send(dexSel("new"))
	defer config.Send(dexSel("release"))
	config.Send(dexSel("setWebsiteDataStore:"), store)
	config.Send(dexSel("preferences")).Send(dexSel("setJavaScriptCanOpenWindowsAutomatically:"), false)
	// Use a fresh configuration without user scripts or message handlers.
	view := dexClass("WKWebView").Send(dexSel("alloc")).Send(dexSel("initWithFrame:configuration:"), dexRect{}, config)
	if view == 0 {
		return nil, errors.New("WebKit could not create the Dex web view")
	}
	view.Send(dexSel("setAllowsLinkPreview:"), false)
	w := &dexAppleWebView{view: view, delegate: objc.ID(delegate).Send(dexSel("new")), origin: origin, storeID: id, emit: emit}
	dexWebViews[w.delegate] = w
	view.Send(dexSel("setNavigationDelegate:"), w.delegate)
	view.Send(dexSel("setUIDelegate:"), w.delegate)
	return w, nil
}

func dexWebsiteStore(id uuid.UUID) (objc.ID, error) {
	storeClass := dexClass("WKWebsiteDataStore")
	var store objc.ID
	if id == uuid.Nil() {
		store = storeClass.Send(dexSel("nonPersistentDataStore"))
	} else {
		get := dexSel("dataStoreForIdentifier:")
		if !objc.Send[bool](storeClass, dexSel("respondsToSelector:"), get) {
			return 0, errors.New("this Apple system does not support a dedicated Dex data store")
		}
		store = storeClass.Send(get, dexStoreIdentifier(id))
	}
	if store == 0 {
		return 0, errors.New("WebKit could not open a Dex data store")
	}
	return store, nil
}

type dexRect struct{ X, Y, Width, Height float64 }

func (w *dexAppleWebView) load(raw, method string) {
	nativeURL := dexClass("NSURL").Send(dexSel("URLWithString:"), dexNSString(raw))
	if nativeURL == 0 {
		w.emit("failure", nil)
		return
	}
	request := dexClass("NSMutableURLRequest").Send(dexSel("requestWithURL:"), nativeURL)
	request.Send(dexSel("setHTTPMethod:"), dexNSString(method))
	w.view.Send(dexSel("loadRequest:"), request)
}

func (w *dexAppleWebView) close() {
	delete(dexWebViews, w.delegate)
	w.view.Send(dexSel("stopLoading"))
	w.view.Send(dexSel("setNavigationDelegate:"), objc.ID(0))
	w.view.Send(dexSel("setUIDelegate:"), objc.ID(0))
	w.view.Send(dexSel("removeFromSuperview"))
	w.view.Send(dexSel("release"))
	w.delegate.Send(dexSel("release"))
}

func dexNavigationAction(self objc.ID, _ objc.SEL, view, action, decision objc.ID) {
	w := dexWebViews[self]
	if w == nil || w.origin == (dexOrigin{}) {
		dexDecide(decision, false)
		return
	}
	target := action.Send(dexSel("targetFrame"))
	main := target == 0 || objc.Send[bool](target, dexSel("isMainFrame"))
	request := action.Send(dexSel("request"))
	raw := dexGoString(request.Send(dexSel("URL")).Send(dexSel("absoluteString")))
	origin, u := dexParseOrigin(raw)
	if origin == w.origin && target != 0 {
		dexDecide(decision, true)
		return
	}
	external := main && origin != w.origin && u != nil && origin.scheme == "https" && !strings.ContainsAny(raw, "?#") &&
		dexGoString(request.Send(dexSel("HTTPMethod"))) == "GET" &&
		objc.Send[int](action, dexSel("navigationType")) == 0 && // WKNavigationTypeLinkActivated
		!objc.Send[bool](view, dexSel("isLoading")) &&
		!objc.Send[bool](action, dexSel("shouldPerformDownload"))
	if external {
		source := action.Send(dexSel("sourceFrame"))
		from, _ := dexParseOrigin(dexGoString(source.Send(dexSel("request")).Send(dexSel("URL")).Send(dexSel("absoluteString"))))
		external = objc.Send[bool](source, dexSel("isMainFrame")) && from == w.origin
	}
	// Never let WebKit create a popup. Do not replay the original request.
	dexDecide(decision, false)
	if external {
		w.emit("external", raw)
	} else if main && origin != w.origin {
		w.emit("blocked", nil)
	}
}

func dexNavigationResponse(self objc.ID, _ objc.SEL, _, response, decision objc.ID) {
	w := dexWebViews[self]
	u := response.Send(dexSel("response")).Send(dexSel("URL"))
	origin, _ := dexParseOrigin(dexGoString(u.Send(dexSel("absoluteString"))))
	allow := w != nil && origin != (dexOrigin{}) && origin == w.origin
	dexDecide(decision, allow)
}

func dexNavigationFailure(self objc.ID, _ objc.SEL, _, _, err objc.ID) {
	w := dexWebViews[self]
	if w == nil {
		return
	}
	domain := dexGoString(err.Send(dexSel("domain")))
	code := objc.Send[int](err, dexSel("code"))
	if domain == "NSURLErrorDomain" && code == -999 || domain == "WebKitErrorDomain" && code == 102 {
		return
	}
	w.emit("failure", nil)
}

// Objective-C supplied this block. objc.Block.Invoke works only on Go blocks.
func dexDecide(block objc.ID, allow bool) {
	type blockHeader struct {
		isa             uintptr
		flags, reserved int32
		invoke          uintptr
	}
	header := objc.Send[*blockHeader](block, dexSel("self"))
	policy := uintptr(0)
	if allow {
		policy = 1
	}
	purego.SyscallN(header.invoke, uintptr(block), policy)
}
