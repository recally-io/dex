package engine

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestUserProviderEditRejectsOldCatalogAndLateDiscovery(t *testing.T) {
	// Given a successful discovery whose SDK store contains models from the old URL.
	staleEntered, newEntered := make(chan struct{}), make(chan struct{})
	staleRelease, newRelease := make(chan struct{}), make(chan struct{})
	releaseStale := sync.OnceFunc(func() { close(staleRelease) })
	releaseNew := sync.OnceFunc(func() { close(newRelease) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stale/models":
			close(staleEntered)
			<-staleRelease
			http.Error(w, "PRIVATE", http.StatusUnauthorized)
			return
		case "/new/models":
			close(newEntered)
			select {
			case <-newRelease:
			case <-r.Context().Done():
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"new"}]}`))
		default:
			_, _ = w.Write([]byte(`{"data":[{"id":"old"}]}`))
		}
	}))
	defer server.Close()
	defer releaseStale()
	defer releaseNew()
	catalog := NewCatalog(false)
	defer catalog.Close()
	p := ProviderSettings{ID: "local", API: "openai-completions", BaseURL: server.URL + "/old"}
	if _, err := catalog.Configure(t.Context(), []ProviderSettings{p}); err != nil {
		t.Fatal(err)
	}
	// When the provider is edited twice while the middle discovery is held.
	p.BaseURL = server.URL + "/stale"
	stale, err := catalog.ConfigureAsync(t.Context(), []ProviderSettings{p})
	if err != nil {
		t.Fatal(err)
	}
	awaitDiscoverySignal(t, staleEntered)
	p.BaseURL = server.URL + "/new"
	current, err := catalog.ConfigureAsync(t.Context(), []ProviderSettings{p})
	if err != nil {
		t.Fatal(err)
	}
	awaitDiscoverySignal(t, newEntered)
	// Then the old URL's SDK catalog is not restored into the edited provider.
	if model := catalog.Models.GetModel("local", "old"); model != nil {
		t.Fatalf("restored stale URL model: %+v", model.ProviderMeta)
	}
	if _, err := stale.Wait(t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatalf("superseded discovery: %v", err)
	}
	releaseNew()
	if failures, err := current.Wait(t.Context()); err != nil || len(failures) != 0 {
		t.Fatalf("new discovery: %v %v", failures, err)
	}
	releaseStale()
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	model := catalog.Models.GetModel("local", "new")
	if model == nil || model.ProviderMeta.BaseURL != p.BaseURL || catalog.Failure("local") != "" {
		t.Fatalf("late discovery replaced new result: model=%+v failure=%s", model, catalog.Failure("local"))
	}
	t.Log("artifact: cached old URL and cancelled late response did not replace edited provider")
}

func TestUserUnrelatedSaveKeepsCatalogAndOpenAgentUsable(t *testing.T) {
	// Given a discovered provider and an already open native agent.
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/held/models" {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"known"}]}`))
	}))
	defer server.Close()
	defer unblock()
	a := coreEngine(t, Config{})
	p := ProviderSettings{ID: "known", API: "openai-completions", BaseURL: server.URL}
	if _, err := a.Catalog.Configure(t.Context(), []ProviderSettings{p}); err != nil {
		t.Fatal(err)
	}
	ref := coreCreate(t, a, "alpha", coreOptions("echo"))
	// When an unrelated dynamic provider starts discovery.
	discovery, err := a.Catalog.ConfigureAsync(t.Context(), []ProviderSettings{p, {ID: "held", API: "openai-completions", BaseURL: server.URL + "/held"}})
	if err != nil {
		t.Fatal(err)
	}
	awaitDiscoverySignal(t, entered)
	// Then the unchanged catalog remains available and the existing harness can answer.
	if model := a.Catalog.Models.GetModel("known", "known"); model == nil {
		t.Fatal("unrelated save emptied known catalog")
	}
	if got := coreWait(t, coreSubmit(t, a, ref, "unrelated-save", "still usable", "echo")); got.Answer != "echo: still usable" {
		t.Fatalf("open harness: %+v", got)
	}
	if _, err := a.Create(t.Context(), "cold", coreOptions("echo")); !errors.Is(err, ErrModelsPending) {
		t.Fatalf("cold native admission: %v", err)
	}
	unblock()
	if _, err := discovery.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	cold := coreCreate(t, a, "cold", coreOptions("echo"))
	if got := coreWait(t, coreSubmit(t, a, cold, "after-ready", "ready", "echo")); got.Answer != "echo: ready" {
		t.Fatalf("recovered native admission: %+v", got)
	}
	t.Log("artifact: existing native agent stayed usable; cold admission recovered after discovery")
}

func awaitDiscoverySignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("discovery did not reach controlled endpoint")
	}
}
