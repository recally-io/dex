package settings

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserModelChecksDistinguishPendingDiscoveryFromInvalidModels(t *testing.T) {
	// Given saved dynamic and static providers in real business SQLite.
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"remote"}]}`))
	}))
	defer endpoint.Close()
	defer unblock()
	conn, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	q := sqlc.New(conn)
	for _, row := range []sqlc.UpsertProviderParams{
		{ID: "dynamic", Api: "openai-completions", BaseUrl: endpoint.URL},
		{ID: "static", Api: "openai-completions", BaseUrl: endpoint.URL, Models: "fixed"},
	} {
		if err := q.UpsertProvider(t.Context(), row); err != nil {
			t.Fatal(err)
		}
	}
	catalog := engine.NewCatalog(true)
	defer catalog.Close()
	service := New(q, catalog)
	discovery, err := service.RegisterProviders(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("discovery did not start")
	}
	// When Settings validates a not-yet-discovered model, the result is retryable.
	if err := service.CheckModel(t.Context(), "dynamic", "remote"); !errors.Is(err, engine.ErrModelsPending) || errors.Is(err, ErrInvalid) {
		t.Fatalf("pending model check: %v", err)
	}
	if err := service.CheckChat(t.Context(), chat.Settings{Provider: "dynamic", ModelID: "remote"}); !errors.Is(err, engine.ErrModelsPending) {
		t.Fatalf("pending chat check: %v", err)
	}
	// Static and builtin checks remain usable while unrelated discovery is held.
	for _, selection := range [][2]string{{"static", "fixed"}, {"faux", "echo"}} {
		if err := service.CheckModel(t.Context(), selection[0], selection[1]); err != nil {
			t.Fatalf("available model check: %v", err)
		}
	}
	// Then completed discovery validates the model and rejects a truly unknown one.
	unblock()
	if _, err := discovery.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := service.CheckModel(t.Context(), "dynamic", "remote"); err != nil {
		t.Fatal(err)
	}
	if err := service.CheckModel(t.Context(), "dynamic", "unknown"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown model: %v", err)
	}
	t.Log("artifact: pending checks are retryable; available static/faux models validate immediately")
}
