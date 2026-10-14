// Package server serves the HTTP API and the web UI.
package server

import (
	"context"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/vaayne/dex/internal/chat"
	"github.com/vaayne/dex/internal/settings"
	"github.com/vaayne/dex/web"
)

// The web UI is one chat on the default agent.
const webChannel = "web:default"

// New returns the handler for the API under /api/ and the web UI at /. The API has no
// authentication; it refuses only cross-site writes, so a web page in the user's browser cannot
// send messages or change settings.
func New(chats *chat.Service, cfg *settings.Service, agentID string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Message string `json:"message"`
		}
		if !decode(w, r, &body) {
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
	mux.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) {
		v, err := cfg.Get(r.Context())
		respond(w, v, err)
	})
	mux.HandleFunc("GET /api/models", func(w http.ResponseWriter, r *http.Request) {
		models, err := cfg.Models(r.Context())
		respond(w, map[string]any{"models": models}, err)
	})
	mux.HandleFunc("PUT /api/settings/agent", func(w http.ResponseWriter, r *http.Request) {
		var body settings.Agent
		if decode(w, r, &body) {
			respond(w, nil, cfg.SetAgent(r.Context(), body))
		}
	})
	mux.HandleFunc("PUT /api/settings/providers/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body settings.Provider
		if decode(w, r, &body) {
			body.ID = r.PathValue("id")
			body.BaseURL, body.APIKey = strings.TrimSpace(body.BaseURL), strings.TrimSpace(body.APIKey)
			respond(w, nil, cfg.SetProvider(r.Context(), body))
		}
	})
	mux.HandleFunc("DELETE /api/settings/providers/{id}", func(w http.ResponseWriter, r *http.Request) {
		respond(w, nil, cfg.DeleteProvider(r.Context(), r.PathValue("id")))
	})
	mux.HandleFunc("PUT /api/settings/telegram", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Token        string  `json:"token"`
			AllowedUsers []int64 `json:"allowedUsers"`
		}
		if decode(w, r, &body) {
			respond(w, nil, cfg.SetTelegram(r.Context(), body.Token, body.AllowedUsers))
		}
	})
	mux.HandleFunc("DELETE /api/settings/telegram", func(w http.ResponseWriter, r *http.Request) {
		respond(w, nil, cfg.DeleteTelegram(r.Context()))
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	mux.Handle("/", web.Handler())
	return http.NewCrossOriginProtection().Handler(mux)
}

// decode reads a JSON body of at most 64 KiB into v. On failure it writes a 400 response and
// returns false.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.UnmarshalRead(r.Body, v); err != nil {
		writeError(w, http.StatusBadRequest, "the body must be a JSON object")
		return false
	}
	return true
}

// respond writes v, or the error: 400 for refused settings, 500 for the rest.
func respond(w http.ResponseWriter, v any, err error) {
	switch {
	case errors.Is(err, settings.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		slog.Error("api", "err", err)
		writeError(w, http.StatusInternalServerError, err.Error())
	case v == nil:
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	default:
		writeJSON(w, http.StatusOK, v)
	}
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
