// Package server serves the HTTP API of api/openapi.yaml.
package server

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/artifact"
	"github.com/recally-io/dex/internal/automation"
	"github.com/recally-io/dex/internal/bot"
	"github.com/recally-io/dex/internal/channel/feishu"
	"github.com/recally-io/dex/internal/channel/pairing"
	"github.com/recally-io/dex/internal/channel/telegram"
	"github.com/recally-io/dex/internal/channel/wechat"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/host/sysops"
	"github.com/recally-io/dex/internal/live"
	"github.com/recally-io/dex/internal/plugin"
	"github.com/recally-io/dex/internal/runner"
	"github.com/recally-io/dex/internal/session"
	"github.com/recally-io/dex/internal/settings"
	tasksvc "github.com/recally-io/dex/internal/task"
	"github.com/recally-io/dex/internal/upload"
)

// Config holds what the handler serves.
type Config struct {
	Sessions    *session.Service
	Automations *automation.Service
	Tasks       *tasksvc.Service
	TaskEvents  *tasksvc.Broker
	Settings    *settings.Service
	Bots        *bot.Service
	RestoreBot  func(context.Context, string) error
	Pairing     *pairing.Service
	Telegram    *telegram.SetupService
	Feishu      *feishu.SetupService
	Wechat      *wechat.SetupService
	Artifacts   *artifact.Service
	Live        *live.Hub
	DataDir     string
	Version     string
	ReleaseBase string

	// Catalog origin overrides for local upstreams in tests.
	SkillCatalogURL string
	McpRegistryURL  string
	// Engine owns native execution and extensions.
	Engine *engine.Bots
	Envs   *runner.Manager
	// RunnerChanged requests a reload after a saved environment change.
	RunnerChanged func(context.Context, string)
	// Runners enrolls remote machines and serves their sessions.
	Runners *runner.Runners
	// System owns server upgrades, restarts, backups, and recent logs.
	System *sysops.Manager

	// Plugins add hook and API routes. AvailablePlugins names every plugin of this build.
	Plugins          []plugin.Plugin
	AvailablePlugins []string
	// PluginErrors maps an enabled plugin that does not run to the reason, for example a failed
	// migration.
	PluginErrors map[string]string

	// WebHandler serves the web UI at /. If nil, no web UI is served.
	WebHandler http.Handler

	// ChannelWebhooks authenticates provider callbacks independently of the admin token.
	ChannelWebhooks http.Handler

	// Token is the access token that the API requires, or empty for no authentication.
	Token string
}

// errInvalid is a request that the handler refuses with 400.
var errInvalid = errors.New("invalid request")

// New returns the handler for the API under /api/ and the optional web UI at /. The API requires
// cfg.Token when it is set. It refuses cross-site writes, so a web page in the user's browser
// cannot send messages or change settings, and cannot sign in.
func New(cfg Config) http.Handler {
	mux := http.NewServeMux()
	h := &handler{Config: cfg}
	strict := api.NewStrictHandlerWithOptions(h, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() != nil {
				return
			}
			// Decoder errors can quote a value with the wrong JSON type.
			if taskExternalPath(r) || taskTokenResponse(r) || strings.HasSuffix(r.URL.Path, "/vault") || strings.HasSuffix(r.URL.Path, "/oauth") || strings.HasSuffix(r.URL.Path, "/integrations/mcp") {
				writeError(w, http.StatusBadRequest, "the request is not valid")
				return
			}
			writeError(w, http.StatusBadRequest, "the request is not valid: "+err.Error())
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() != nil {
				return
			}
			status := http.StatusInternalServerError
			switch {
			case errors.Is(err, settings.ErrInvalid), errors.Is(err, bot.ErrInvalid), errors.Is(err, pairing.ErrNotificationTargetUnavailable), errors.Is(err, telegram.ErrInvalid), errors.Is(err, feishu.ErrInvalid), errors.Is(err, wechat.ErrInvalid), errors.Is(err, errInvalid), errors.Is(err, automation.ErrInvalid), errors.Is(err, sysops.ErrInvalid),
				errors.Is(err, artifact.ErrInvalidName), errors.Is(err, upload.ErrInvalidName), errors.Is(err, session.ErrQuestionInvalid), errors.Is(err, engine.ErrInvalidInput), errors.Is(err, session.ErrRejected), errors.Is(err, session.ErrProjectInvalid), errors.Is(err, session.ErrRunnerInvalid):
				status = http.StatusBadRequest
			case errors.Is(err, tasksvc.ErrTaskToken):
				status = http.StatusUnauthorized
			case errors.Is(err, tasksvc.ErrClaimLost), errors.Is(err, artifact.ErrConflict), errors.Is(err, sysops.ErrBusy), errors.Is(err, sysops.ErrUnsupported), errors.Is(err, tasksvc.ErrConflict):
				status = http.StatusConflict
			case errors.Is(err, tasksvc.ErrRevision):
				status = http.StatusPreconditionFailed
			case errors.Is(err, upload.ErrTooLarge):
				status = http.StatusRequestEntityTooLarge
			case errors.Is(err, engine.ErrModelsPending):
				status = http.StatusServiceUnavailable
			case errors.Is(err, errReadOnly), errors.Is(err, session.ErrNotArchivable), errors.Is(err, session.ErrProjectForbidden), errors.Is(err, session.ErrSettingsForbidden), errors.Is(err, tasksvc.ErrForbidden):
				status = http.StatusForbidden
			case os.IsNotExist(err), errors.Is(err, sql.ErrNoRows), errors.Is(err, bot.ErrNotFound), errors.Is(err, settings.ErrNotFound), errors.Is(err, session.ErrNotFound), errors.Is(err, automation.ErrNotFound), errors.Is(err, upload.ErrNotFound), errors.Is(err, db.ErrBotArchived), errors.Is(err, engine.ErrItemNotFound):
				status = http.StatusNotFound
			case errors.Is(err, settings.ErrConflict), errors.Is(err, telegram.ErrCredentialInUse), errors.Is(err, feishu.ErrCredentialInUse), errors.Is(err, wechat.ErrCredentialInUse), errors.Is(err, session.ErrMessageID), errors.Is(err, session.ErrResetID), errors.Is(err, session.ErrOperationID), errors.Is(err, session.ErrNoModel), errors.Is(err, session.ErrMaintenanceBusy), errors.Is(err, session.ErrSessionBusy), errors.Is(err, session.ErrAlreadyStarted), errors.Is(err, session.ErrQuestionClosed), errors.Is(err, session.ErrTaskConflict), errors.Is(err, session.ErrRunnerLocked), errors.Is(err, bot.ErrRunnerBusy), errors.Is(err, session.ErrNothingToContinue), errors.Is(err, session.ErrNothingToCompact), errors.Is(err, engine.ErrBusy), errors.Is(err, engine.ErrRunnerResetting), errors.Is(err, engine.ErrMaintenance), errors.Is(err, engine.ErrStaleRunTarget), errors.Is(err, engine.ErrMCPServerExists):
				status = http.StatusConflict
			default:
				slog.Error("api", "err", err)
			}
			// A stream or download can fail after its status and body have started.
			if !w.(*responseWriter).started { //nolint:forcetypeassert // limited wraps every API response.
				writeError(w, status, err.Error())
			}
		},
	})
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{BaseRouter: mux, Middlewares: []api.MiddlewareFunc{h.withBot}})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	// A machine reads these paths before it has a token, and it authenticates with its own
	// token afterwards, so they sit outside the admin API.
	mux.HandleFunc("POST /machine/{id}/pair", h.machinePair)
	mux.HandleFunc("GET /machine/{id}/connect", h.machineConnect)
	mux.HandleFunc("GET /machine/{id}/release/{file}", h.machineRunnerRelease)
	mux.HandleFunc("GET "+pairing.ChannelApprovalPath+"{id}", h.channelApproval)
	mux.HandleFunc("POST "+pairing.ChannelApprovalPath+"{id}", h.channelApproval)
	mux.HandleFunc("GET /install-machine.sh", h.machineFile)
	mux.HandleFunc("GET "+mcpCallbackPath, h.finishMcpOAuth)
	mux.HandleFunc("GET /machine-release/{file}", h.machineRelease)
	for _, p := range cfg.Plugins {
		if p.Hooks != nil {
			mux.Handle("/hooks/"+p.Name+"/", http.MaxBytesHandler(p.Hooks, p.HookLimit))
		}
		if p.API != nil {
			mux.Handle("/api/plugins/"+p.Name+"/", p.API)
		}
	}
	if cfg.ChannelWebhooks != nil {
		mux.Handle("POST /hooks/channels/{platform}/{botId}", http.MaxBytesHandler(cfg.ChannelWebhooks, 4<<20))
	}
	mux.HandleFunc("/hooks/", http.NotFound)
	if cfg.WebHandler != nil {
		mux.Handle("/", cfg.WebHandler)
	}
	// The longest message is 32000 characters of up to 4 bytes. An upload has room for its
	// multipart headers.
	small, uploads := http.MaxBytesHandler(mux, 256<<10), http.MaxBytesHandler(mux, upload.MaxSize+64<<10)
	limited := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w = &responseWriter{ResponseWriter: w}
		parts := strings.Split(r.URL.Path, "/")
		switch {
		case strings.HasPrefix(r.URL.Path, "/hooks/"):
			// Each hook sets its own body limit.
			mux.ServeHTTP(w, r)
		case len(parts) == 5 && parts[1] == "api" && parts[2] == "bots" && parts[4] == "uploads":
			uploads.ServeHTTP(w, r)
		default:
			small.ServeHTTP(w, r)
		}
	})
	origins := http.NewCrossOriginProtection()
	origins.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, pairing.ChannelApprovalPath) {
			auditDeniedChannelApproval()
		}
		http.Error(w, origins.Check(r).Error(), http.StatusForbidden)
	}))
	return origins.Handler(requireToken(cfg.Token, limited))
}

type responseWriter struct {
	http.ResponseWriter
	started bool
}

func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *responseWriter) WriteHeader(status int) {
	if status >= 200 || status == http.StatusSwitchingProtocols {
		w.started = true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseWriter) Write(body []byte) (int, error) {
	if !w.started {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *responseWriter) Flush() {
	if !w.started {
		w.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.MarshalWrite(w, api.Error{Error: message}); err != nil {
		slog.Error("write response", "err", err)
	}
}

type handler struct {
	Config
	runnerReleaseMu sync.Mutex
	mcpOAuth        mcpOAuthRequests
}

type botKey struct{}

func botID(ctx context.Context) string {
	return ctx.Value(botKey{}).(string) //nolint:forcetypeassert // withBot sets this value on every bot route.
}

// withBot checks the path before the strict handler decodes the body or opens storage.
func (h *handler) withBot(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("botId")
		if id != "" {
			archived, err := h.Bots.Bot(r.Context(), id)
			// An archived bot is hidden: only its restore route answers.
			if err == nil && archived.ArchivedAt != 0 && !isRestore(r) {
				err = bot.ErrNotFound
			}
			if err != nil {
				if r.Context().Err() != nil {
					return
				}
				status := http.StatusInternalServerError
				if errors.Is(err, bot.ErrNotFound) {
					status = http.StatusNotFound
				} else {
					slog.Error("read request bot", "err", err)
				}
				writeError(w, status, err.Error())
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), botKey{}, id))
		}
		if (id != "" && strings.HasSuffix(r.URL.Path, "/oauth")) || r.Pattern == "POST /api/integrations/mcp/{name}/oauth" {
			w.Header().Set("Cache-Control", "no-store")
			r = r.WithContext(context.WithValue(r.Context(), mcpOAuthRequestKey{}, mcpOAuthOrigin{origin: r.Header.Get("Origin"), host: r.Host}))
		}
		next.ServeHTTP(w, r)
	})
}

// isRestore matches DELETE /api/bots/{botId}/archive.
func isRestore(r *http.Request) bool {
	return r.Method == http.MethodDelete && r.Pattern == "DELETE /api/bots/{botId}/archive"
}

var ok = api.Ok{Status: api.OkStatusOk}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
