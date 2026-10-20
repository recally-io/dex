package host_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/host"
)

func TestUserProxyAddsTokenAndSameOrigin(t *testing.T) {
	var backend *httptest.Server
	backend = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer desktop-token" {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("Origin"); got != backend.URL {
			t.Errorf("origin = %q, want %q", got, backend.URL)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()

	proxy, err := host.StartProxy(t.Context(), backend.URL, "desktop-token")
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = proxy.Close(context.Background())
		}
	})

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, proxy.URL()+"/api/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read proxy response: read=%v close=%v", readErr, closeErr)
	}
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("proxy response: status=%d body=%s", resp.StatusCode, body)
	}

	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := proxy.Close(shutdown); err != nil {
		t.Fatal(err)
	}
	closed = true
	select {
	case err := <-proxy.Done():
		if err != nil {
			t.Fatal(err)
		}
	case <-shutdown.Done():
		t.Fatal("proxy did not stop")
	}
}
