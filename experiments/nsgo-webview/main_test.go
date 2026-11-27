package main

import (
	"testing"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestMain(m *testing.M) { nsgotest.Main(m) }

func TestUserGetsDexInTheWholeViewport(t *testing.T) {
	t.Parallel()
	app := nsgotest.Start(t, root, nsgotest.Size(390, 844))
	view := app.Find("id:dex")
	if got := view.Web.Source.Src; got != "http://127.0.0.1:19681/" {
		t.Errorf("start URL = %q", got)
	}
	if got := view.Bounds(); got != (core.Bounds{Size: core.Size{Width: 390, Height: 844}}) {
		t.Errorf("Dex does not fill the viewport: %+v", got)
	}
	if !view.Web.Navigable("http://127.0.0.1:19681/b/default") {
		t.Error("native policy denies Dex navigation")
	}
	for _, target := range []string{"https://example.test/", "http://localhost:19681/", "file:///tmp/page", "data:text/html,hello", "about:blank", "blob:http://127.0.0.1:19681/id"} {
		if view.Web.Navigable(target) {
			t.Errorf("native policy permits %q", target)
		}
	}
}

func TestUserReturnsToDexAfterAnOffOriginLoadReport(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		url  string
		stay bool
	}{
		{"http://127.0.0.1:19681/b/default?tab=chat#latest", true},
		{"http://127.0.0.1:19682/", false},
		{"https://127.0.0.1:19681/", false},
	} {
		t.Run(tc.url, func(t *testing.T) {
			app := nsgotest.Start(t, root)
			app.Navigate("id:dex", core.WebNavigation{URL: tc.url})
			commands := app.Find("id:dex").WebCommands
			if tc.stay {
				if len(commands) != 0 {
					t.Fatalf("Dex navigation changed: %+v", commands)
				}
				return
			}
			if len(commands) != 2 || commands[0].Command != nsgotest.WebStopLoading || commands[1].Command != nsgotest.WebLoad {
				t.Fatalf("expected the shell to stop and return to Dex, got %+v", commands)
			}
			if source, ok := commands[1].Data.(*core.WebSource); !ok || source.Src != "http://127.0.0.1:19681/" {
				t.Fatalf("unexpected return URL: %+v", source)
			}
		})
	}
}
