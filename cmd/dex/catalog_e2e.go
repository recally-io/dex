//go:build dex_e2e

package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/recally-io/dex/internal/mcptest"
)

func testCatalogAPI() (string, func()) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/skills/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query()
		switch {
		case q.Get("q") != "pdf":
			_, _ = io.WriteString(w, `{"skills":[],"total":0,"limit":20,"offset":0}`)
		case q.Get("offset") == "0":
			_, _ = io.WriteString(w, `{"skills":[{"id":"pdf","name":"PDF tools","description":"Read PDFs","sourceUrl":"https://github.com/anthropics/skills/tree/main/skills/pdf","metadata":{"directoryPath":"skills/pdf"}}],"total":21,"limit":20,"offset":0}`)
		default:
			_, _ = io.WriteString(w, `{"skills":[{"id":"pdf-forms","name":"PDF forms","description":"Read forms","sourceUrl":"https://github.com/acme/skills/tree/main/forms"}],"total":21,"limit":20,"offset":20}`)
		}
	})
	mux.HandleFunc("GET /v0/servers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query()
		switch {
		case q.Get("search") != "fixture":
			_, _ = io.WriteString(w, `{"servers":[],"metadata":{}}`)
		case q.Get("cursor") == "":
			_, _ = fmt.Fprintf(w, `{"servers":[{"server":{"name":"example.com/fixture-auth","title":"Registry fixture","description":"Local Streamable HTTP fixture","version":"1","remotes":[{"type":"streamable-http","url":%q,"headers":[{"name":"Authorization","description":"Enter a Bearer token","isRequired":true,"isSecret":true}]}]}}],"metadata":{"nextCursor":"fixture-next"}}`, "http://"+r.Host+"/mcp")
		default:
			_, _ = io.WriteString(w, `{"servers":[{"server":{"name":"example.com/local","title":"Local package fixture","version":"1","packages":[{}]}}],"metadata":{}}`)
		}
	})
	notes := &mcptest.Server{}
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer registry-secret-value" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		notes.ServeHTTP(w, r)
	})
	server := httptest.NewServer(mux)
	return server.URL, server.Close
}
