// Package web serves the embedded web UI.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed dist/index.html dist/assets/*
var files embed.FS

// Handler serves build assets and the index page for client routes.
func Handler() http.Handler {
	assets, err := fs.Sub(files, "dist")
	if err != nil {
		panic(err)
	}
	server := http.FileServerFS(assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			server.ServeHTTP(w, r)
			return
		}
		page, err := files.ReadFile("dist/index.html")
		if err != nil {
			panic(err)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(page)
	})
}
