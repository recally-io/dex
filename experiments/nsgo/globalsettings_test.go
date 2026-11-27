package main

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"
	"uuid"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func settingsTestOrigin(t *testing.T) *url.URL {
	t.Helper()
	if os.Getenv("DEX_NSGO_POC_TEST") != "1" {
		t.Skip("set DEX_NSGO_POC_TEST=1 only with a disposable gateway")
	}
	origin, err := url.Parse(os.Getenv("DEX_NSGO_TEST_URL"))
	if err != nil || origin.Scheme != "http" || origin.Hostname() != "127.0.0.1" || origin.Port() != "19545" && origin.Port() != "19555" ||
		origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		t.Fatal("use a disposable Settings acceptance gateway at 127.0.0.1:19545 or 127.0.0.1:19555")
	}
	return origin
}

func TestUserNavigatesAndRefreshesAllGlobalSettings(t *testing.T) {
	settingsTestOrigin(t)
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		page := core.NewSignal(cx, "general")
		return core.Dynamic(func(cx *core.Scope) core.Node {
			tab := page.Get()
			return core.Untracked(cx, func() core.Node { return globalSettingsScreen(cx, tab, page.Set, func(string) {}) })
		})
	})

	// Given a disposable Dex, navigate all seven pages and refresh their live lists.
	const timeout = 20 * time.Second
	for _, tab := range []struct{ page, ready, feed string }{
		{"general", "global-defaults", "general"}, {"providers", "global-providers-list", "providers"},
		{"runners", "global-runners-list", "runners"}, {"secrets", "global-secret-vault", "secrets"},
		{"integrations", "global-sources", "sources"}, {"archived", "global-archived-list", "archived"},
		{"about", "global-about-server", "about"},
	} {
		app.Press("id:global-settings-tab-" + tab.page)
		app.WaitFor("id:"+tab.ready, timeout)
		app.Press("id:global-" + tab.feed + "-refresh")
		app.WaitForGone("id:global-"+tab.feed+"-status", timeout)
		app.WaitFor("id:"+tab.ready, timeout)
		switch tab.page {
		case "providers":
			app.Press("id:global-provider-add")
			app.WaitFor("id:global-provider-dialog", timeout)
			app.Press("id:global-provider-dialog-cancel")
		case "runners":
			app.Press("id:global-runner-add")
			app.WaitFor("id:global-runner-dialog", timeout)
			app.Press("id:global-runner-dialog-cancel")
		case "secrets":
			app.Press("id:global-secret-add")
			app.WaitFor("id:global-secret-dialog", timeout)
			app.Press("id:global-secret-dialog-cancel")
		}
	}
}

func TestUserSavesSourceChoiceWithoutRestart(t *testing.T) {
	if os.Getenv("DEX_NSGO_GLOBAL_WRITE_TEST") != "1" {
		t.Skip("enable only on the disposable settings write fixture")
	}
	if settingsTestOrigin(t).Port() != "19555" {
		t.Fatal("the writes fixture must use port 19555")
	}
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		return globalSettingsScreen(cx, "integrations", func(string) {}, func(string) {})
	})
	const timeout = 20 * time.Second
	const toggle = "id:global-source-github"
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	github := func() settingsSource {
		t.Helper()
		var list struct {
			Sources []settingsSource `json:"sources"`
		}
		if err := requestJSON(ctx, http.MethodGet, "/api/settings/sources", nil, &list); err != nil {
			t.Fatal(err)
		}
		for _, source := range list.Sources {
			if source.Name == "github" {
				return source
			}
		}
		t.Fatal("the fixture has no GitHub source")
		return settingsSource{}
	}
	app.WaitFor(toggle, timeout)
	original := github()
	app.Press(toggle)
	app.WaitFor("text:Source setting saved.", timeout)
	if got := github(); got.Enabled == original.Enabled || got.Running != original.Running {
		t.Fatalf("saved configuration = %+v, want changed choice and the same running state", got)
	}
	app.WaitFor("text:Restart Dex to apply source changes.", timeout)
	app.Press(toggle)
	app.WaitForGone("text:Restart Dex to apply source changes.", timeout)
	if got := github(); got != original {
		t.Fatalf("source = %+v after restoring the choice; want %+v", got, original)
	}
}

func TestUserAddsReplacesAndRemovesSharedSecret(t *testing.T) {
	if os.Getenv("DEX_NSGO_GLOBAL_WRITE_TEST") != "1" {
		t.Skip("enable only on the disposable settings write fixture")
	}
	if settingsTestOrigin(t).Port() != "19555" {
		t.Fatal("the writes fixture must use port 19555")
	}
	const timeout = 20 * time.Second
	name := "NSGO_ACCEPTANCE_" + uuid.New().String()[:8]
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		return globalSettingsScreen(cx, "secrets", func(string) {}, func(string) {})
	})

	// Given an isolated Vault, the user can add and replace a dummy shared entry.
	app.WaitFor("id:global-secret-add", timeout)
	app.Press("id:global-secret-add")
	app.Press("id:global-secret-name")
	app.Commit("id:global-secret-name", name)
	app.Press("id:global-secret-value")
	app.Commit("id:global-secret-value", "synthetic-first")
	app.Press("id:global-secret-save")
	app.WaitFor("id:global-secret-replace-"+name, timeout)
	app.Press("id:global-secret-replace-" + name)
	app.Press("id:global-secret-value")
	app.Commit("id:global-secret-value", "synthetic-replacement")
	app.Press("id:global-secret-save")
	app.WaitForGone("id:global-secret-dialog", timeout)
	app.WaitFor("text:Secret saved.", timeout)

	// Then removing that entry makes it disappear from the list.
	app.Press("id:global-secret-remove-" + name)
	app.Press("id:global-secret-confirm-remove")
	app.WaitForGone("id:global-secret-replace-"+name, timeout)
	app.WaitFor("text:Secret removed.", timeout)
}
