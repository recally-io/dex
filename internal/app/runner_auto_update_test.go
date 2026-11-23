package app_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MichaelKinsy/PiG/durable/env/node"
	"github.com/coder/websocket"
	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/execenv/runner"
)

func buildRunnerArchive(t *testing.T, tag string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	script := "#!/bin/sh\nprintf 'dex-agent " + tag + "\\n'\n"
	if err := tw.WriteHeader(&tar.Header{
		Name:     "dex-agent",
		Typeflag: tar.TypeReg,
		Mode:     0o755,
		Size:     int64(len(script)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(tw, script); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestUserRunnerHandshakeReportsVersionAndCachesReleaseProxy(t *testing.T) {
	t.Parallel()
	// Given a Dex v2.0.0 server backed by a local release server with a dex-agent archive.
	archiveData := buildRunnerArchive(t, "v2.0.0")
	archiveName := fmt.Sprintf("dex-agent_v2.0.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	sum := sha256.Sum256(archiveData)
	checksums := hex.EncodeToString(sum[:]) + "  " + archiveName + "\n"
	var assetDownloads atomic.Int32

	var relSrv *httptest.Server
	relSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/asset":
			if assetDownloads.Add(1) == 1 {
				_, _ = io.WriteString(w, "incomplete asset")
				return
			}
			_, _ = w.Write(archiveData)
		case "/checksums":
			_, _ = io.WriteString(w, checksums)
		default:
			_ = json.MarshalWrite(w, map[string]any{
				"tag_name": "v2.0.0",
				"assets": []map[string]string{
					{"name": archiveName, "url": relSrv.URL + "/asset"},
					{"name": "checksums.txt", "url": relSrv.URL + "/checksums"},
				},
			})
		}
	}))
	t.Cleanup(relSrv.Close)

	d := startDexWith(t, app.Config{
		DataDir:     t.TempDir(),
		Version:     "v2.0.0",
		ReleaseBase: relSrv.URL,
	})

	// Create and enroll a runner environment.
	var created api.Environment
	d.must(t, http.MethodPost, "/api/settings/environments", map[string]any{
		"name":   "Build Mac",
		"kind":   "runner",
		"config": map[string]any{},
	}, &created, http.StatusCreated)
	var issued struct {
		Code string `json:"code"`
	}
	d.must(t, http.MethodPost, "/api/settings/environments/"+created.Id+"/runner-pairing", nil, &issued)

	pairReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, d.srv.URL+"/machine/"+created.Id+"/pair", nil)
	if err != nil {
		t.Fatal(err)
	}
	pairReq.Header.Set("Authorization", "Bearer "+issued.Code)
	pairResp, err := http.DefaultClient.Do(pairReq)
	if err != nil {
		t.Fatal(err)
	}
	defer pairResp.Body.Close()
	if pairResp.StatusCode != http.StatusOK {
		t.Fatalf("pair status = %d", pairResp.StatusCode)
	}
	var paired struct {
		Token string `json:"token"`
	}
	if err := json.UnmarshalRead(pairResp.Body, &paired); err != nil || paired.Token == "" {
		t.Fatalf("pair response = %+v, err=%v", paired, err)
	}

	// When a runner dials with an incompatible protocol, 426 still carries X-Dex-Version so it can self-update.
	wsURL := "ws" + strings.TrimPrefix(d.srv.URL, "http") + "/machine/" + created.Id + "/connect"
	_, badRes, _ := websocket.Dial(t.Context(), wsURL, &websocket.DialOptions{ //nolint:bodyclose // closed below when non-nil
		HTTPHeader:   http.Header{"Authorization": []string{"Bearer " + paired.Token}},
		Subprotocols: []string{"dex-agent.v0"},
	})
	if badRes != nil && badRes.Body != nil {
		_ = badRes.Body.Close()
	}
	if badRes == nil || badRes.StatusCode != http.StatusUpgradeRequired || badRes.Header.Get("X-Dex-Version") != "v2.0.0" {
		t.Fatalf("expected 426 with X-Dex-Version=v2.0.0, got %+v", badRes)
	}

	// When a runner connects normally and reports v1.0.0 in Ready, the API exposes its live runner status.
	conn, okRes, err := websocket.Dial(t.Context(), wsURL, &websocket.DialOptions{ //nolint:bodyclose // websocket.Dial owns body on upgrade
		HTTPHeader:   http.Header{"Authorization": []string{"Bearer " + paired.Token}},
		Subprotocols: []string{runner.Version},
	})
	if err != nil {
		t.Fatal(err)
	}
	if okRes.Header.Get("X-Dex-Version") != "v2.0.0" {
		t.Fatalf("connect X-Dex-Version = %q, want v2.0.0", okRes.Header.Get("X-Dex-Version"))
	}
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		_ = runner.ServeWithInfo(t.Context(), conn, node.NewNodeExecutionEnv(node.NodeExecutionEnvOptions{Cwd: t.TempDir()}), runner.Ready{
			Version:       "v1.0.0",
			CanSelfUpdate: true,
		})
	}()
	t.Cleanup(func() {
		_ = conn.Close(websocket.StatusNormalClosure, "test done")
		<-serveDone
	})

	eventually(t, "runner status visible in environments API", func() (bool, any) {
		var list api.EnvironmentList
		d.must(t, http.MethodGet, "/api/settings/environments", nil, &list)
		for _, env := range list.Environments {
			if env.Id == created.Id && env.Runner != nil && env.Runner.Online {
				return env.Runner.Version != nil && *env.Runner.Version == "v1.0.0" && env.Runner.CanSelfUpdate != nil && *env.Runner.CanSelfUpdate, env.Runner
			}
		}
		return false, list.Environments
	})

	// Unauthenticated release proxy requests are refused.
	unauthReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, d.srv.URL+"/machine/"+created.Id+"/release/checksums.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	unauthResp, err := http.DefaultClient.Do(unauthReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = unauthResp.Body.Close()
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth release status = %d, want 401", unauthResp.StatusCode)
	}

	fetchArchive := func(t *testing.T, wantStatus int) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, d.srv.URL+"/machine/"+created.Id+"/release/"+archiveName, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+paired.Token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != wantStatus || (wantStatus == http.StatusOK && !bytes.Equal(got, archiveData)) {
			t.Fatalf("proxy archive fetch: status=%d, want %d; len=%d err=%v", resp.StatusCode, wantStatus, len(got), err)
		}
	}

	// Reject a bad upstream asset. Concurrent retries should share the verified copy.
	fetchArchive(t, http.StatusBadGateway)
	t.Run("concurrent retries", func(t *testing.T) {
		for i := range 4 {
			t.Run(fmt.Sprint(i), func(t *testing.T) {
				t.Parallel()
				fetchArchive(t, http.StatusOK)
			})
		}
	})
	if assetDownloads.Load() != 2 {
		t.Fatalf("expected 1 upstream download due to caching, got %d", assetDownloads.Load())
	}
}
