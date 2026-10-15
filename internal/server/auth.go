package server

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/recally-io/dex/internal/api"
)

const sessionCookie = "dex_session"

const signIn = "sign in with the access token; run `dex token` on the dex host to print it"

// requireToken lets a request reach the API only with the token, as a Bearer token or as the
// session cookie. The web UI files stay public, so the browser can show the sign-in form.
func requireToken(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		public := !strings.HasPrefix(r.URL.Path, "/api/") || r.Method == http.MethodPost && r.URL.Path == "/api/session"
		if !public && !authorized(r, token) {
			writeError(w, http.StatusUnauthorized, signIn)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func authorized(r *http.Request, token string) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			return false
		}
		got = c.Value
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

func (h *handler) GetSession(context.Context, api.GetSessionRequestObject) (api.GetSessionResponseObject, error) {
	return api.GetSession204Response{}, nil // requireToken refused an unauthorized request
}

func (h *handler) CreateSession(_ context.Context, req api.CreateSessionRequestObject) (api.CreateSessionResponseObject, error) {
	if h.Token == "" {
		return api.CreateSession204Response{}, nil
	}
	if subtle.ConstantTimeCompare([]byte(req.Body.Token), []byte(h.Token)) != 1 {
		return api.CreateSession401JSONResponse{Error: "the token is not correct"}, nil
	}
	// No Domain: the browser sends the cookie only to this host. The cookie expires when the token
	// changes.
	//nolint:gosec // G124: no Secure flag, because dex serves plain HTTP; a Secure cookie would never return.
	cookie := (&http.Cookie{Name: sessionCookie, Value: h.Token, Path: "/", MaxAge: 400 * 24 * 60 * 60, HttpOnly: true, SameSite: http.SameSiteStrictMode}).String()
	return api.CreateSession204Response{Headers: api.CreateSession204ResponseHeaders{SetCookie: &cookie}}, nil
}
