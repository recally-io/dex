//go:build darwin && !tvos && !watchos

package main

import (
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unsafe"
	"uuid"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/nsgo-dev/nsgo"
	"github.com/nsgo-dev/nsgo/core"
)

func dexWebViewOptions() []nsgo.Option {
	return []nsgo.Option{nsgo.Component(dexWebComponent, func(cx *core.NativeContext) core.NativeComponent {
		class := "NSView"
		if runtime.GOOS == "ios" {
			class = "UIView"
		}
		return &dexWorkspaceHost{container: dexClass(class).Send(dexSel("new")), emit: cx.Emit}
	})}
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
	case "dex-local":
		if u.Host != "workspace" {
			return dexOrigin{}, nil
		}
		return dexOrigin{scheme: u.Scheme, host: u.Host}, u
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
	address, storeID := localAppleOrigin, dexStoreID(localWorkspaceTarget)
	if raw != localWorkspaceTarget {
		canonical, ok := canonicalOrigin(raw, runtime.GOOS == "darwin")
		if !ok || canonical != raw {
			h.emit("failure", nil)
			return
		}
		address, storeID = canonical, dexStoreID(canonical)
	}
	origin, _ := dexParseOrigin(address)
	page, err := newDexAppleWebView(origin, storeID, h.emit)
	if err != nil {
		h.emit("failure", nil)
		return
	}
	h.page = page
	const flexibleWidthAndHeight = 2 | 16 // AppKit and UIKit
	page.view.Send(dexSel("setAutoresizingMask:"), uint(flexibleWidthAndHeight))
	page.view.Send(dexSel("setFrame:"), objc.Send[dexRect](h.container, dexSel("bounds")))
	h.container.Send(dexSel("addSubview:"), page.view)
	page.load(address, http.MethodGet)
}

func (h *dexWorkspaceHost) Command(name string, value any) {
	if raw, ok := value.(string); ok && h.page != nil {
		h.page.localCommand(name, raw)
	}
}

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
	local    *dexAppleLocal
}

type dexAppleLocal struct {
	controller objc.ID
	sequence   uint64
	tasks      map[string]objc.ID
}

// Only the Apple main thread accesses the delegates and their views.
var dexWebViews = map[objc.ID]*dexAppleWebView{}

var dexWebDelegate = sync.OnceValues(func() (objc.Class, error) {
	return objc.RegisterClass("DexWorkspaceNavigationDelegate", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
		{Cmd: dexSel("webView:startURLSchemeTask:"), Fn: dexStartSchemeTask},
		{Cmd: dexSel("webView:stopURLSchemeTask:"), Fn: dexStopSchemeTask},
		{Cmd: dexSel("userContentController:didReceiveScriptMessage:"), Fn: dexReceiveScriptMessage},
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
	config.Send(dexSel("setApplicationNameForUserAgent:"), dexNSString("DexWorkspace/1"))
	config.Send(dexSel("preferences")).Send(dexSel("setJavaScriptCanOpenWindowsAutomatically:"), false)
	w := &dexAppleWebView{delegate: objc.ID(delegate).Send(dexSel("new")), origin: origin, storeID: id, emit: emit}
	if origin.scheme == "dex-local" {
		controller := config.Send(dexSel("userContentController"))
		w.local = &dexAppleLocal{controller: controller, tasks: make(map[string]objc.ID)}
		config.Send(dexSel("setURLSchemeHandler:forURLScheme:"), w.delegate, dexNSString(origin.scheme))
		controller.Send(dexSel("addScriptMessageHandler:name:"), w.delegate, dexNSString("dexLocal"))
		script := dexClass("WKUserScript").Send(dexSel("alloc")).Send(dexSel("initWithSource:injectionTime:forMainFrameOnly:"), dexNSString(localBridgeJS), 0, true)
		controller.Send(dexSel("addUserScript:"), script)
		script.Send(dexSel("release"))
	}
	view := dexClass("WKWebView").Send(dexSel("alloc")).Send(dexSel("initWithFrame:configuration:"), dexRect{}, config)
	if view == 0 {
		w.delegate.Send(dexSel("release"))
		return nil, errors.New("WebKit could not create the Dex web view")
	}
	view.Send(dexSel("setAllowsLinkPreview:"), false)
	w.view = view
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
	if w.local != nil {
		w.local.controller.Send(dexSel("removeScriptMessageHandlerForName:"), dexNSString("dexLocal"))
		clear(w.local.tasks)
	}
	w.view.Send(dexSel("stopLoading"))
	w.view.Send(dexSel("setNavigationDelegate:"), objc.ID(0))
	w.view.Send(dexSel("setUIDelegate:"), objc.ID(0))
	w.view.Send(dexSel("removeFromSuperview"))
	w.view.Send(dexSel("release"))
	w.delegate.Send(dexSel("release"))
}

func dexStartSchemeTask(self objc.ID, _ objc.SEL, _, task objc.ID) {
	w := dexWebViews[self]
	if w == nil || w.local == nil {
		return
	}
	request := task.Send(dexSel("request"))
	raw := dexGoString(request.Send(dexSel("URL")).Send(dexSel("absoluteString")))
	if origin, _ := dexParseOrigin(raw); origin != w.origin {
		dexFailSchemeTask(task)
		return
	}
	w.local.sequence++
	id := strconv.FormatUint(w.local.sequence, 10)
	msg := localMessage{Op: "static", ID: id, localRequest: localRequest{
		Method: dexGoString(request.Send(dexSel("HTTPMethod"))), URL: raw,
		Header: make(http.Header),
	}}
	fields := request.Send(dexSel("allHTTPHeaderFields"))
	keys := fields.Send(dexSel("keyEnumerator"))
	for key := keys.Send(dexSel("nextObject")); key != 0; key = keys.Send(dexSel("nextObject")) {
		msg.Header.Set(dexGoString(key), dexGoString(fields.Send(dexSel("objectForKey:"), key)))
	}
	body, err := json.Marshal(msg)
	if err != nil {
		dexFailSchemeTask(task)
		return
	}
	w.local.tasks[id] = task
	w.emit("localStatic", string(body))
}

func dexStopSchemeTask(self objc.ID, _ objc.SEL, _, task objc.ID) {
	if w := dexWebViews[self]; w != nil && w.local != nil {
		for id, pending := range w.local.tasks {
			if pending == task {
				delete(w.local.tasks, id)
				return
			}
		}
	}
}

func dexFailSchemeTask(task objc.ID) {
	err := dexClass("NSError").Send(dexSel("errorWithDomain:code:userInfo:"), dexNSString("NSURLErrorDomain"), -1002, objc.ID(0))
	task.Send(dexSel("didFailWithError:"), err)
}

func dexReceiveScriptMessage(self objc.ID, _ objc.SEL, _, message objc.ID) {
	w := dexWebViews[self]
	if w == nil || w.local == nil {
		return
	}
	frame := message.Send(dexSel("frameInfo"))
	if !objc.Send[bool](frame, dexSel("isMainFrame")) {
		return
	}
	origin, _ := dexParseOrigin(dexGoString(frame.Send(dexSel("request")).Send(dexSel("URL")).Send(dexSel("absoluteString"))))
	body := message.Send(dexSel("body"))
	if origin == w.origin && objc.Send[bool](body, dexSel("isKindOfClass:"), dexClass("NSString")) {
		w.emit("localMessage", dexGoString(body))
	}
}

func (w *dexAppleWebView) localCommand(name, raw string) {
	if w.local == nil {
		return
	}
	switch name {
	case "eval":
		w.view.Send(dexSel("evaluateJavaScript:completionHandler:"), dexNSString(raw), objc.ID(0))
	case "staticResult":
		var result localStaticResult
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			return
		}
		task := w.local.tasks[result.ID]
		if task == 0 {
			return
		}
		delete(w.local.tasks, result.ID)
		request := task.Send(dexSel("request"))
		address := request.Send(dexSel("URL"))
		fields := dexClass("NSMutableDictionary").Send(dexSel("dictionary"))
		for key, values := range result.Response.Headers {
			fields.Send(dexSel("setObject:forKey:"), dexNSString(strings.Join(values, ", ")), dexNSString(key))
		}
		// WebKit can request module scripts with CORS from a custom-scheme page.
		if strings.HasPrefix(dexGoString(address.Send(dexSel("path"))), "/assets/") {
			fields.Send(dexSel("setObject:forKey:"), dexNSString("*"), dexNSString("Access-Control-Allow-Origin"))
		}
		response := dexClass("NSHTTPURLResponse").Send(dexSel("alloc")).Send(dexSel("initWithURL:statusCode:HTTPVersion:headerFields:"), address, result.Response.Status, dexNSString("HTTP/1.1"), fields)
		if response == 0 {
			dexFailSchemeTask(task)
			return
		}
		task.Send(dexSel("didReceiveResponse:"), response)
		response.Send(dexSel("release"))
		if body := result.Response.Body; len(body) != 0 {
			data := dexClass("NSData").Send(dexSel("dataWithBytes:length:"), unsafe.SliceData(body), len(body))
			runtime.KeepAlive(body)
			task.Send(dexSel("didReceiveData:"), data)
		}
		task.Send(dexSel("didFinish"))
	}
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
	if origin == w.origin && target != 0 && (w.local == nil || main) {
		dexDecide(decision, true)
		return
	}
	settings := target != 0 && main && raw == workspaceSettingsURL
	external := main && origin != w.origin && u != nil && origin.scheme == "https" && (w.local != nil || !strings.ContainsAny(raw, "?#"))
	activated := (settings || external) &&
		dexGoString(request.Send(dexSel("HTTPMethod"))) == "GET" &&
		objc.Send[int](action, dexSel("navigationType")) == 0 && // WKNavigationTypeLinkActivated
		!objc.Send[bool](view, dexSel("isLoading")) &&
		!objc.Send[bool](action, dexSel("shouldPerformDownload"))
	if activated {
		source := action.Send(dexSel("sourceFrame"))
		from, _ := dexParseOrigin(dexGoString(source.Send(dexSel("request")).Send(dexSel("URL")).Send(dexSel("absoluteString"))))
		activated = objc.Send[bool](source, dexSel("isMainFrame")) && from == w.origin
	}
	// Never let WebKit create a popup. Do not replay the original request.
	dexDecide(decision, false)
	if activated && settings {
		w.emit("settings", nil)
	} else if activated && external {
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
