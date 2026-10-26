package app_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/mcptest"
)

type isolationResponse struct {
	status int
	body   string
	err    error
}

func (d *dex) isolationRequest(ctx context.Context, method, path string, body any) isolationResponse {
	var input io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return isolationResponse{err: err}
		}
		input = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, d.srv.URL+path, input)
	if err != nil {
		return isolationResponse{err: err}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := d.srv.Client().Do(req)
	if err != nil {
		return isolationResponse{err: err}
	}
	defer response.Body.Close()
	encoded, err := io.ReadAll(response.Body)
	return isolationResponse{status: response.StatusCode, body: string(encoded), err: err}
}

func isolationReceipt(ctx context.Context, t *testing.T, conn *sql.DB, id, status string) sqlc.Submission {
	t.Helper()
	q := sqlc.New(conn)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	var last sqlc.Submission
	for {
		row, err := q.GetSubmission(ctx, id)
		if err == nil {
			last = row
			if row.Status == status {
				return row
			}
		} else if !errors.Is(err, sql.ErrNoRows) && ctx.Err() == nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("receipt %s did not become %s: last=%+v; %v", id, status, last, ctx.Err())
		case <-tick.C:
		}
	}
}

func TestUserOtherAgentWorksDuringSlowMCP(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"cold_open", "reload"} {
		t.Run(operation, func(t *testing.T) {
			// Given two agents and a real MCP server whose handshake is held for alpha.
			entered, release := make(chan struct{}), make(chan struct{})
			enterOnce := sync.OnceFunc(func() { close(entered) })
			releaseOnce := sync.OnceFunc(func() { close(release) })
			mcpServer := &mcptest.Server{Dir: t.TempDir()}
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				enterOnce()
				select {
				case <-release:
					mcpServer.ServeHTTP(w, req)
				case <-req.Context().Done():
				}
			}))
			t.Cleanup(endpoint.Close)
			data := t.TempDir()
			d := startDex(t, data, "")
			t.Cleanup(releaseOnce)
			d.setModel(t, "echo")
			var beta apiAgent
			d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "Beta", "provider": "faux", "modelId": "echo", "thinkingLevel": "off"}, &beta, http.StatusCreated)
			var alphaMain, betaMain chatInfo
			d.must(t, http.MethodGet, "/api/agents/default/chats/main", nil, &alphaMain)
			d.must(t, http.MethodGet, "/api/agents/"+beta.ID+"/chats/main", nil, &betaMain)
			alphaPath := fmt.Sprintf("/api/agents/default/chats/%d", alphaMain.ID)
			betaPath := fmt.Sprintf("/api/agents/%s/chats/%d", beta.ID, betaMain.ID)
			conn := sessionDB(t, d)
			warmCtx, cancelWarm := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancelWarm()
			warmID := uuid.NewV7().String()
			d.must(t, http.MethodPost, betaPath+"/messages", map[string]string{"id": warmID, "text": "warm beta"}, nil, http.StatusAccepted)
			isolationReceipt(warmCtx, t, conn, warmID, "done")
			if operation == "reload" {
				warmAlpha := uuid.NewV7().String()
				d.must(t, http.MethodPost, alphaPath+"/messages", map[string]string{"id": warmAlpha, "text": "warm alpha"}, nil, http.StatusAccepted)
				isolationReceipt(warmCtx, t, conn, warmAlpha, "done")
			}
			config, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"slow": map[string]any{"type": "http", "url": endpoint.URL}}})
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(data, "agents", "default", ".agents", "mcp.json"), string(config))
			alphaCtx, cancelAlpha := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancelAlpha()
			alphaDone := make(chan isolationResponse, 1)
			alphaID := uuid.NewV7().String()
			go func() {
				if operation == "reload" {
					alphaDone <- d.isolationRequest(alphaCtx, http.MethodPost, "/api/agents/default/extensions/reload", nil)
					return
				}
				alphaDone <- d.isolationRequest(alphaCtx, http.MethodPost, alphaPath+"/messages", map[string]string{"id": alphaID, "text": "cold alpha"})
			}()
			select {
			case <-entered:
			case result := <-alphaDone:
				t.Fatalf("alpha did not reach MCP: %+v", result)
			case <-alphaCtx.Done():
				t.Fatal("alpha did not start its MCP handshake")
			}

			// When alpha is still waiting, beta can read and complete native execution.
			betaCtx, cancelBeta := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancelBeta()
			health := d.isolationRequest(betaCtx, http.MethodGet, "/api/health", nil)
			if health.err != nil || health.status != http.StatusOK {
				t.Fatalf("health waited for unrelated MCP: %+v", health)
			}
			var ready struct {
				RuntimeReady bool `json:"runtimeReady"`
			}
			if err := json.Unmarshal([]byte(health.body), &ready); err != nil || !ready.RuntimeReady {
				t.Fatalf("healthy runtime became unavailable during agent open: %s %v", health.body, err)
			}
			history := d.isolationRequest(betaCtx, http.MethodGet, betaPath+"/history?before=9007199254740991&limit=100", nil)
			if history.err != nil || history.status != http.StatusOK {
				t.Fatalf("beta history waited for unrelated MCP: %+v", history)
			}
			betaID := uuid.NewV7().String()
			sent := d.isolationRequest(betaCtx, http.MethodPost, betaPath+"/messages", map[string]string{"id": betaID, "text": "during alpha"})
			if sent.err != nil || sent.status != http.StatusAccepted {
				t.Fatalf("beta send waited for unrelated MCP: %+v", sent)
			}
			sub, err := sqlc.New(conn).GetSubmission(betaCtx, betaID)
			if err != nil || !sub.AdmittedAt.Valid {
				t.Fatalf("beta returned before native admission: %+v %v", sub, err)
			}
			if sub := isolationReceipt(betaCtx, t, conn, betaID, "done"); sub.Answer.String != "echo: during alpha" {
				t.Fatalf("beta did not complete while alpha waited: %+v", sub)
			}
			select {
			case result := <-alphaDone:
				t.Fatalf("alpha finished before MCP release: %+v", result)
			default:
			}
			t.Log("artifact: health, beta history, native admission and answer completed before alpha MCP release")

			// The same agent keeps a stored input when its HTTP caller stops waiting.
			var queuedID string
			if operation == "reload" {
				queuedID = uuid.NewV7().String()
				requestCtx, cancelRequest := context.WithCancel(t.Context())
				defer cancelRequest()
				queuedDone := make(chan isolationResponse, 1)
				go func() {
					queuedDone <- d.isolationRequest(requestCtx, http.MethodPost, alphaPath+"/messages", map[string]string{"id": queuedID, "text": "after cancel"})
				}()
				storedCtx, cancelStored := context.WithTimeout(t.Context(), 2*time.Second)
				defer cancelStored()
				stored := isolationReceipt(storedCtx, t, conn, queuedID, "pending")
				if stored.AdmittedAt.Valid {
					t.Fatal("alpha input was admitted while its replacement was unavailable")
				}
				cancelRequest()
				select {
				case result := <-queuedDone:
					if result.err != nil && !errors.Is(result.err, context.Canceled) {
						t.Fatalf("cancelled alpha request: %+v", result)
					}
					if result.err == nil && result.status != http.StatusAccepted {
						t.Fatalf("stored alpha input was rejected: %+v", result)
					}
				case <-storedCtx.Done():
					t.Fatal("same-agent caller could not cancel its wait")
				}
			}

			// Then releasing alpha finishes the operation and reconciles its stored input.
			releaseOnce()
			select {
			case result := <-alphaDone:
				want := http.StatusAccepted
				if operation == "reload" {
					want = http.StatusOK
				}
				if result.err != nil || result.status != want {
					t.Fatalf("alpha operation after release: %+v", result)
				}
			case <-alphaCtx.Done():
				t.Fatal("alpha did not finish after MCP release")
			}
			if queuedID != "" {
				if sub := isolationReceipt(alphaCtx, t, conn, queuedID, "done"); sub.Answer.String != "echo: after cancel" {
					t.Fatalf("cancelled caller lost its stored input: %+v", sub)
				}
				t.Log("artifact: cancelled same-agent caller retained one pending input, then received its answer")
			}
		})
	}
}
