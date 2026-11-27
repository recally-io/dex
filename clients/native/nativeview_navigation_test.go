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

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestMain(m *testing.M) { nsgotest.Main(m) }

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
