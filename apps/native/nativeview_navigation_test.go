//go:build darwin && !tvos && !watchos

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/ebitengine/purego/objc"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestMain(m *testing.M) { nsgotest.Main(m) }

func TestUserWorkspaceUsesTheExactOrigin(t *testing.T) {
	t.Parallel()
	origin, _ := dexParseOrigin("https://dex.example")
	for candidate, want := range map[string]bool{
		"https://DEX.example:443/workspace": true,
		"https://dex.example:8443/":         false,
		"https://docs.dex.example/":         false,
		"http://dex.example:443/":           false,
		"https://guest@dex.example/":        false,
		"about:blank":                       false,
	} {
		got, _ := dexParseOrigin(candidate)
		if (got == origin) != want {
			t.Errorf("origin match for %q = %t, want %t", candidate, got == origin, want)
		}
	}
}

func TestUserWorkspaceStaysOnItsOrigin(t *testing.T) {
	t.Parallel()
	if nsgotest.Target() == "" {
		t.Skip("A native Apple run is required")
	}
	// Both servers are loopback fixtures. Their ports make them distinct origins.
	var arrivals atomic.Int32
	outside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		arrivals.Add(1)
		fmt.Fprint(w, "outside")
	}))
	t.Cleanup(outside.Close)
	inside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/inside":
			http.Redirect(w, r, "/ready", http.StatusFound)
		case "/outside":
			http.Redirect(w, r, outside.URL, http.StatusFound)
		case "/outside-post":
			http.Redirect(w, r, outside.URL, http.StatusTemporaryRedirect)
		case "/popup":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<a id="info" href="%s" target="_blank">Information</a><script>document.getElementById('info').click()</script>`, outside.URL)
		default:
			fmt.Fprint(w, "ready")
		}
	}))
	t.Cleanup(inside.Close)

	nsgotest.Start(t, func(*core.Scope) core.Node { return core.Text{Content: "Navigation fixture"} })

	var page *dexAppleWebView
	var hostErr error
	events := make(chan string, 8)
	if err := dexOnAppleMain(func() {
		origin, _ := dexParseOrigin(inside.URL)
		page, hostErr = newDexAppleWebView(origin, uuid.Nil(), func(event string, _ any) { events <- event })
		events <- "created"
	}); err != nil {
		t.Fatal(err)
	}
	wait := func(want string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		select {
		case got := <-events:
			if got != want {
				t.Fatalf("navigation event = %q, want %q (outside requests: %d)", got, want, arrivals.Load())
			}
		case <-ctx.Done():
			t.Fatalf("waiting for %q: %v (outside requests: %d)", want, ctx.Err(), arrivals.Load())
		}
	}
	wait("created")
	if hostErr != nil {
		t.Fatal(hostErr)
	}
	t.Cleanup(func() {
		done := make(chan struct{})
		if err := dexOnAppleMain(func() { page.close(); close(done) }); err != nil {
			t.Error(err)
			return
		}
		<-done
	})
	load := func(raw, method string) {
		t.Helper()
		if err := dexOnAppleMain(func() { page.load(raw, method) }); err != nil {
			t.Fatal(err)
		}
	}

	load(inside.URL+"/inside", http.MethodGet)
	wait("finished")
	load(inside.URL+"/outside", http.MethodGet)
	wait("blocked")
	load(inside.URL+"/outside-post", http.MethodPost)
	wait("blocked")
	load(outside.URL, http.MethodPost)
	wait("blocked")
	load(inside.URL+"/popup", http.MethodGet)
	wait("finished")
	load(inside.URL+"/ready", http.MethodGet)
	wait("finished")
	if got := arrivals.Load(); got != 0 {
		t.Fatalf("outside origin received %d requests, want zero", got)
	}
}

func TestUserOpensLocalWorkspaceInWebKit(t *testing.T) {
	t.Parallel()
	if nsgotest.Target() == "" {
		t.Skip("A native Apple run is required")
	}
	bridge := startTestBridge(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	nsgotest.Start(t, func(*core.Scope) core.Node { return core.Text{Content: "Local workspace fixture"} })
	await := func(result <-chan string) string {
		t.Helper()
		select {
		case value := <-result:
			return value
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return ""
		}
	}
	var page *dexAppleWebView
	var hostErr error
	loaded := make(chan string, 4)
	if err := dexOnAppleMain(func() {
		origin, _ := dexParseOrigin(localAppleOrigin)
		page, hostErr = newDexAppleWebView(origin, uuid.Nil(), func(event string, data any) {
			switch event {
			case "localMessage", "localStatic":
				bridge.handleMessage(ctx, data.(string), event == "localStatic", func(command string, value any) {
					if err := dexOnAppleMain(func() {
						if ctx.Err() == nil {
							page.localCommand(command, value.(string))
						}
					}); err != nil {
						t.Error(err)
					}
				})
			default:
				loaded <- event
			}
		})
		loaded <- "created"
		if hostErr == nil {
			page.load(localAppleOrigin, http.MethodGet)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if got := await(loaded); got != "created" || hostErr != nil {
		t.Fatalf("create local WebView: %s, %v", got, hostErr)
	}
	t.Cleanup(func() {
		done := make(chan struct{})
		if err := dexOnAppleMain(func() { page.close(); close(done) }); err != nil {
			t.Error(err)
			return
		}
		<-done
	})
	if got := await(loaded); got != "finished" {
		t.Fatalf("load local WebView: %s", got)
	}

	// The embedded React module must boot, and the document-start shim must
	// handle a Request and deliver a named SSE event before the stream ends.
	script := `(() => {
		window.localTestResult = "started";
		fetch(new Request("/api/health")).then(async response => {
			if (!response.ok) throw new Error("health status " + response.status);
			window.localTestResult = "health ok";
			const path = "/api/bots/default/uploads";
			const form = new FormData();
			form.append("file", new Blob([new Uint8Array([0, 255, 128])]), "test.bin");
			const uploaded = await fetch(path, { method: "POST", body: form });
			if (uploaded.status !== 201) throw new Error("upload status " + uploaded.status);
			const downloaded = await fetch(path + "/" + (await uploaded.json()).id);
			if (String(new Uint8Array(await downloaded.arrayBuffer())) !== "0,255,128") throw new Error("wrong file bytes");
			const source = new EventSource("/api/tasks/events");
			source.addEventListener("task_ready", event => {
				source.close();
				window.localTestResult = "sse " + event.lastEventId;
			});
			source.onerror = () => { window.localTestResult = "sse failed"; };
		}).catch(error => { window.localTestResult = String(error); });
	})()`
	if err := dexOnAppleMain(func() { page.localCommand("eval", script) }); err != nil {
		t.Fatal(err)
	}
	query := func() string {
		result := make(chan string, 1)
		if err := dexOnAppleMain(func() {
			block := objc.NewBlock(func(_ objc.Block, value, err objc.ID) {
				if err != 0 {
					result <- dexGoString(err.Send(dexSel("localizedDescription")))
					return
				}
				result <- dexGoString(value)
			})
			defer block.Release()
			page.view.Send(dexSel("evaluateJavaScript:completionHandler:"), dexNSString(`window.localTestResult + "|" + !!document.querySelector("#root > *")`), block)
		}); err != nil {
			t.Fatal(err)
		}
		return await(result)
	}
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		got := query()
		if got == "sse 0|true" {
			return
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatalf("local WebView did not load React, fetch, and stream: %q", got)
		}
	}
}

func TestUserSignInIsIsolatedAndForgotten(t *testing.T) {
	t.Parallel()
	if nsgotest.Target() == "" {
		t.Skip("A native Apple run is required")
	}
	seen := make(chan bool, 10)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/set":
			http.SetCookie(w, &http.Cookie{Name: "fixture", Value: "present", Path: "/", MaxAge: 3600, HttpOnly: true})
		case "/check":
			cookie, _ := r.Cookie("fixture")
			seen <- cookie != nil && cookie.Value == "present"
		}
		fmt.Fprint(w, "ready")
	})
	first := httptest.NewServer(handler)
	second := httptest.NewServer(handler)
	t.Cleanup(first.Close)
	t.Cleanup(second.Close)
	nsgotest.Start(t, func(*core.Scope) core.Node { return core.Text{Content: "Website data fixture"} })

	var view *dexAppleWebView
	main := func(fn func()) {
		t.Helper()
		done := make(chan struct{})
		if err := dexOnAppleMain(func() { fn(); close(done) }); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("Apple main queue did not complete")
		}
	}
	remove := func(origin string) error {
		done := make(chan error, 1)
		main(func() { dexRemoveStore(dexStoreID(origin), func(err error) { done <- err }) })
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			return fmt.Errorf("WebKit did not finish removing the fixture data store")
		}
	}
	t.Cleanup(func() {
		main(func() {
			if view != nil {
				view.close()
			}
		})
		if err := remove(first.URL); err != nil {
			t.Error(err)
		}
		if err := remove(second.URL); err != nil {
			t.Error(err)
		}
	})
	load := func(originURL, path string) {
		t.Helper()
		events := make(chan string, 2)
		var err error
		main(func() {
			if view != nil {
				view.close()
			}
			origin, _ := dexParseOrigin(originURL)
			view, err = newDexAppleWebView(origin, dexStoreID(originURL), func(event string, _ any) { events <- event })
			if err == nil {
				view.load(originURL+path, http.MethodGet)
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		select {
		case event := <-events:
			if event != "finished" {
				t.Fatalf("load %s: event = %q, want finished", path, event)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("load %s timed out", path)
		}
	}
	check := func(originURL string, want bool) {
		t.Helper()
		load(originURL, "/check")
		select {
		case got := <-seen:
			if got != want {
				t.Fatalf("fixture cookie present = %t, want %t", got, want)
			}
		default:
			t.Fatal("server did not see the check request")
		}
	}

	load(first.URL, "/set")
	check(first.URL, true)
	check(second.URL, false)
	if err := remove(first.URL); err != nil {
		t.Fatal(err)
	}
	check(first.URL, false)
}
