package server

import (
	"crypto/subtle"
	"html/template"
	"log/slog"
	"net/http"

	"github.com/recally-io/dex/internal/channel/pairing"
)

var channelApprovalPage = template.Must(template.New("channel approval").Parse(`<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Confirm channel change · dex</title>
<main>
<h1>Confirm channel change</h1>
{{if .}}
<p>Bot: <strong>{{.BotName}}</strong> ({{.BotID}})</p>
<p>Change: {{.Description}}</p>
{{if .Destination}}<p>Target: <strong>{{.Destination}}</strong></p>{{end}}
<form method="post">
<button type="submit" name="confirm" value="yes">Apply this change</button>
</form>
<p>Close this page to leave the settings unchanged.</p>
{{else}}
<p>The change was applied.</p>
{{end}}
</main>
</html>`))

func auditDeniedChannelApproval() {
	slog.Info("channel tool mutation", "outcome", "denied", "surface", "owner review")
}

func (h *handler) channelApproval(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	deny := func(status int, message string) {
		if r.Method == http.MethodPost {
			auditDeniedChannelApproval()
		}
		http.Error(w, message, status)
	}
	// Use the web owner's session, not a credential in the link or in tool input.
	// When dex serves the local UI without authentication, the same rule applies here.
	if h.Token != "" {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(h.Token)) != 1 {
			deny(http.StatusUnauthorized, "Sign in to dex in the web UI. Then open this link again.")
			return
		}
	}
	id := r.PathValue("id")
	var review *pairing.ChannelApproval
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil || r.PostForm.Get("confirm") != "yes" {
			deny(http.StatusBadRequest, "Use the confirmation button on the review page.")
			return
		}
		if err := h.Pairing.ConfirmChannelChange(r.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
	} else {
		p, err := h.Pairing.ChannelApproval(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		review = &p
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := channelApprovalPage.Execute(w, review); err != nil {
		slog.Warn("write channel approval page")
	}
}
