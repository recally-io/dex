// Package server serves the HTTP API and the web UI.
package server

import (
	"context"
	"encoding/json/v2"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/vaayne/dex/internal/chat"
	"github.com/vaayne/dex/web"
)

// The web UI is one chat on the default agent.
const webChannel = "web:default"

// New returns the handler for the API under /api/ and the web UI at /.
func New(chats *chat.Service, agentID string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Message string `json:"message"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := json.UnmarshalRead(r.Body, &body); err != nil {
			writeError(w, http.StatusBadRequest, "body must be JSON with a message field")
			return
		}
		message := strings.TrimSpace(body.Message)
		if message == "" {
			writeError(w, http.StatusBadRequest, "message is empty")
			return
		}
		// A run with tool calls can take minutes.
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		reply, err := chats.Send(ctx, agentID, webChannel, message)
		if err != nil {
			slog.Error("chat", "err", err)
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, reply)
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(web.Index)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.MarshalWrite(w, v); err != nil {
		slog.Error("write response", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
