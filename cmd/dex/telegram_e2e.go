//go:build dex_e2e

package main

import (
	"net/http"
	"net/http/httptest"
)

// Keep the browser flow on the real Dex API. Only the Telegram boundary is fake.
func testTelegramAPI() (string, func()) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /bot456:browserFixture/getMe", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":456,"username":"DexManagerBot","is_bot":true,"can_manage_bots":true}}`))
	})
	mux.HandleFunc("POST /bot456:browserFixture/getUpdates", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
	})
	server := httptest.NewServer(mux)
	return server.URL, server.Close
}
