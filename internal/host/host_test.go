package host_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/host"
)

func TestUserServerServesHealthAndStops(t *testing.T) {
	dataDir := t.TempDir()
	running, err := host.Start(t.Context(), host.Config{
		Addr: "127.0.0.1:0",
		App: app.Config{
			DataDir:   dataDir,
			Home:      t.TempDir(),
			ToolEnv:   app.ToolEnv(os.Environ()),
			FakeModel: true,
			Version:   "test",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if closed {
			return
		}
		_ = running.Close(context.Background())
	})

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, running.URL()+"/api/health", nil)
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
		t.Fatalf("read health response: read=%v close=%v", readErr, closeErr)
	}
	if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"status":"ok"`)) {
		t.Fatalf("health response: status=%d body=%s", resp.StatusCode, body)
	}

	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := running.Close(shutdown); err != nil {
		t.Fatal(err)
	}
	closed = true
	select {
	case err := <-running.Done():
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop")
	}
}
