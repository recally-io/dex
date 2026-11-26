// Package server serves the HTTP API of api/openapi.yaml and the web UI.
package server

import (
	"cmp"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/artifact"
	"github.com/recally-io/dex/internal/automation"
	"github.com/recally-io/dex/internal/bot"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/live"
	"github.com/recally-io/dex/internal/plugin"
	"github.com/recally-io/dex/internal/runner"
	"github.com/recally-io/dex/internal/session"
	"github.com/recally-io/dex/internal/settings"
	"github.com/recally-io/dex/internal/sysops"
	tasksvc "github.com/recally-io/dex/internal/task"
	"github.com/recally-io/dex/internal/upload"
	"github.com/recally-io/dex/web"
)

// Config holds what the handler serves.
type Config struct {
	Sessions    *session.Service
	Automations *automation.Service
	Tasks       *tasksvc.Service
	TaskEvents  *tasksvc.Broker
	Settings    *settings.Service
	Bots        *bot.Service
	Artifacts   *artifact.Service
	Live        *live.Hub
	DataDir     string
	Version     string
	ReleaseBase string
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

	// ChannelWebhooks authenticates provider callbacks independently of the admin token.
	ChannelWebhooks http.Handler

	// Token is the access token that the API requires, or empty for no authentication.
	Token string
}

// The longest message, in characters, and the most attachments of one message, as in the contract.
const (
	maxMessage     = 32000
	maxAttachments = 10
)

//go:embed install-machine.sh
var machineInstaller []byte

// errInvalid is a request that the handler refuses with 400.
var errInvalid = errors.New("invalid request")

// errReadOnly is a message to a session that the web UI cannot write to.
var errReadOnly = errors.New("this session is read-only; send messages in Main, a separate Web session, or a child session")

// New returns the handler for the API under /api/ and the web UI at /. The API requires
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
			case errors.Is(err, settings.ErrInvalid), errors.Is(err, bot.ErrInvalid), errors.Is(err, errInvalid), errors.Is(err, automation.ErrInvalid), errors.Is(err, sysops.ErrInvalid),
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
			case errors.Is(err, settings.ErrConflict), errors.Is(err, bot.ErrCredentialInUse), errors.Is(err, session.ErrMessageID), errors.Is(err, session.ErrResetID), errors.Is(err, session.ErrOperationID), errors.Is(err, session.ErrNoModel), errors.Is(err, session.ErrMaintenanceBusy), errors.Is(err, session.ErrSessionBusy), errors.Is(err, session.ErrAlreadyStarted), errors.Is(err, session.ErrQuestionClosed), errors.Is(err, session.ErrTaskConflict), errors.Is(err, session.ErrRunnerLocked), errors.Is(err, bot.ErrRunnerBusy), errors.Is(err, session.ErrNothingToContinue), errors.Is(err, session.ErrNothingToCompact), errors.Is(err, engine.ErrBusy), errors.Is(err, engine.ErrRunnerResetting), errors.Is(err, engine.ErrMaintenance), errors.Is(err, engine.ErrStaleRunTarget), errors.Is(err, engine.ErrMCPServerExists):
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
	mux.HandleFunc("GET "+bot.ChannelApprovalPath+"{id}", h.channelApproval)
	mux.HandleFunc("POST "+bot.ChannelApprovalPath+"{id}", h.channelApproval)
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
	mux.Handle("/", web.Handler())
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
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, bot.ChannelApprovalPath) {
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
		if id := r.PathValue("botId"); id != "" {
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
			ctx := context.WithValue(r.Context(), botKey{}, id)
			if strings.HasSuffix(r.URL.Path, "/oauth") {
				w.Header().Set("Cache-Control", "no-store")
				ctx = context.WithValue(ctx, mcpOAuthRequestKey{}, mcpOAuthOrigin{origin: r.Header.Get("Origin"), host: r.Host})
			}
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

// isRestore matches DELETE /api/bots/{botId}/archive.
func isRestore(r *http.Request) bool {
	return r.Method == http.MethodDelete && r.Pattern == "DELETE /api/bots/{botId}/archive"
}

func (h *handler) uploads(ctx context.Context) *upload.Store {
	return upload.New(bot.Home(h.DataDir, botID(ctx)))
}

var ok = api.Ok{Status: api.OkStatusOk}

func (h *handler) GetHealth(ctx context.Context, _ api.GetHealthRequestObject) (api.GetHealthResponseObject, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	ready := h.Engine != nil && h.Engine.Health(ctx) == nil
	plugins := make([]string, 0, len(h.Plugins))
	for _, p := range h.Plugins {
		plugins = append(plugins, p.Name)
	}
	var startedAt int64
	if h.System != nil {
		startedAt = h.System.StartedAt()
	}
	return api.GetHealth200JSONResponse{Status: api.HealthStatusOk, Version: h.Version, StartedAt: startedAt, DataDir: h.DataDir, RuntimeReady: ready, Plugins: plugins}, nil
}
func (h *handler) AcquireMaintenance(ctx context.Context, _ api.AcquireMaintenanceRequestObject) (api.AcquireMaintenanceResponseObject, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := h.Sessions.AcquireMaintenance(ctx); err != nil {
		return nil, err
	}
	return api.AcquireMaintenance200JSONResponse(ok), nil
}
func (h *handler) ReleaseMaintenance(ctx context.Context, _ api.ReleaseMaintenanceRequestObject) (api.ReleaseMaintenanceResponseObject, error) {
	if err := h.Sessions.ReleaseMaintenance(ctx); err != nil {
		return nil, err
	}
	return api.ReleaseMaintenance200JSONResponse(ok), nil
}

func (h *handler) integrationView(ctx context.Context) (api.BotIntegrations, error) {
	info, err := h.Engine.Extensions(ctx, botID(ctx))
	if err != nil {
		return api.BotIntegrations{}, err
	}
	var out api.BotIntegrations
	raw, err := json.Marshal(info)
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}
func (h *handler) GetBotIntegrations(ctx context.Context, _ api.GetBotIntegrationsRequestObject) (api.GetBotIntegrationsResponseObject, error) {
	out, err := h.integrationView(ctx)
	return api.GetBotIntegrations200JSONResponse(out), err
}
func (h *handler) CreateMcpServer(ctx context.Context, req api.CreateMcpServerRequestObject) (api.CreateMcpServerResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	in := engine.MCPServerInput{Name: req.Body.Name, URL: req.Body.Url}
	if o := req.Body.Oauth; o != nil {
		in.OAuth = &engine.MCPOAuthInput{}
		if o.ClientId != nil {
			in.OAuth.ClientID = *o.ClientId
		}
		if o.ClientSecret != nil {
			in.OAuth.ClientSecret = *o.ClientSecret
		}
		if o.Scope != nil {
			in.OAuth.Scope = *o.Scope
		}
	}
	if err := h.Engine.CreateMCPServer(ctx, botID(ctx), in); err != nil {
		return nil, err
	}
	return api.CreateMcpServer201JSONResponse(ok), nil
}
func (h *handler) ListVault(ctx context.Context, _ api.ListVaultRequestObject) (api.ListVaultResponseObject, error) {
	list, err := h.Engine.VaultList(botID(ctx))
	if err != nil {
		return nil, err
	}
	out := make(api.ListVault200JSONResponse, 0, len(list))
	for _, entry := range list {
		out = append(out, api.VaultEntry{Name: entry.Name, Scope: api.IntegrationScope(entry.Scope), UpdatedAt: entry.UpdatedAt})
	}
	return out, nil
}
func (h *handler) PutVault(ctx context.Context, req api.PutVaultRequestObject) (api.PutVaultResponseObject, error) {
	if req.Body == nil || req.Body.Value == nil {
		return nil, errInvalid
	}
	err := h.Engine.VaultEdit(botID(ctx), string(req.Body.Scope), req.Body.Name, req.Body.Value)
	return api.PutVault200JSONResponse(ok), err
}
func (h *handler) DeleteVault(ctx context.Context, req api.DeleteVaultRequestObject) (api.DeleteVaultResponseObject, error) {
	err := h.Engine.VaultEdit(botID(ctx), string(req.Params.Scope), req.Params.Name, nil)
	return api.DeleteVault200JSONResponse(ok), err
}
func (h *handler) ReloadBotIntegrations(ctx context.Context, _ api.ReloadBotIntegrationsRequestObject) (api.ReloadBotIntegrationsResponseObject, error) {
	if err := h.Sessions.ReloadExtensions(ctx, botID(ctx)); err != nil {
		return nil, err
	}
	out, err := h.integrationView(ctx)
	return api.ReloadBotIntegrations200JSONResponse(out), err
}

func sessionView(c session.Session) api.Session {
	out := api.Session{
		Id: c.ID, Channel: api.SessionChannelWeb, Title: c.Title, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, Busy: c.Busy,
		Cwd: c.Cwd, RunnerId: optional(c.RunnerID), Settings: c.Settings, Main: c.ChannelKey == session.MainKey, ReadOnly: !c.Writable,
	}
	if strings.HasPrefix(c.ChannelKey, "telegram:") {
		out.Channel = api.SessionChannelTelegram
	}
	if strings.HasPrefix(c.ChannelKey, "wechat:") {
		out.Channel = api.SessionChannelWechat
	}
	if strings.HasPrefix(c.ChannelKey, "feishu:") {
		out.Channel = api.SessionChannelFeishu
	}
	if id, ok := automation.SessionAutomation(c.ChannelKey); ok {
		out.Channel, out.AutomationId = api.SessionChannelAutomation, &id
	}
	if c.ArchivedAt != 0 {
		out.ArchivedAt = &c.ArchivedAt
	}
	if c.ParentID != 0 {
		out.ParentId = &c.ParentID
	}
	if c.StoppedByParent {
		out.StoppedWithParent = &c.StoppedByParent
	}
	if c.BotID != "" {
		out.BotId, out.BotName = &c.BotID, &c.BotName
	}
	if c.ParentBotID != "" {
		out.ParentBotId, out.ParentBotName = &c.ParentBotID, &c.ParentBotName
	}
	if o := c.ForkedFrom; o != nil {
		out.ForkedFrom = &api.SessionOrigin{Id: o.ID, Title: o.Title, Main: o.Main}
	}
	if q := c.Question; q != nil {
		out.Question = &api.SessionQuestion{Id: q.ID, Text: q.Text, Options: q.Options, Status: api.SessionQuestionStatus(q.Status)}
		if q.Answer != "" {
			out.Question.Answer = &q.Answer
		}
		if q.Action != "" {
			action := api.SessionQuestionAction(q.Action)
			out.Question.Action = &action
		}
		if q.Outcome != "" {
			out.Question.Outcome = &q.Outcome
		}
	}
	return out
}

func (h *handler) ArchiveSession(ctx context.Context, req api.ArchiveSessionRequestObject) (api.ArchiveSessionResponseObject, error) {
	c, err := h.Sessions.SetArchived(ctx, botID(ctx), req.SessionId, true)
	if err != nil {
		return nil, err
	}
	return api.ArchiveSession200JSONResponse(sessionView(c)), nil
}

func (h *handler) UnarchiveSession(ctx context.Context, req api.UnarchiveSessionRequestObject) (api.UnarchiveSessionResponseObject, error) {
	c, err := h.Sessions.SetArchived(ctx, botID(ctx), req.SessionId, false)
	if err != nil {
		return nil, err
	}
	return api.UnarchiveSession200JSONResponse(sessionView(c)), nil
}

func (h *handler) ListSessions(ctx context.Context, _ api.ListSessionsRequestObject) (api.ListSessionsResponseObject, error) {
	return h.listSessions(ctx, botID(ctx))
}

func (h *handler) listSessions(ctx context.Context, botID string) (api.ListSessions200JSONResponse, error) {
	sessions, err := h.Sessions.List(ctx, botID)
	if err != nil {
		return api.ListSessions200JSONResponse{}, err
	}
	out := api.ListSessions200JSONResponse{Sessions: make([]api.Session, len(sessions))}
	// The list has no execution ID. Only a busy session needs one, to tell the web UI that it stalled.
	var refs []engine.Ref
	refOf := map[int]engine.Ref{}
	for i, c := range sessions {
		out.Sessions[i] = sessionView(c)
		if !c.Busy {
			continue
		}
		// A child in another bot belongs to that bot.
		owner := cmp.Or(c.BotID, botID)
		full, err := h.Sessions.Get(ctx, owner, c.ID)
		if err != nil {
			return api.ListSessions200JSONResponse{}, err
		}
		if full.ExecutionID > 0 {
			refOf[i] = engine.Ref{BotID: owner, ID: full.ExecutionID}
			refs = append(refs, refOf[i])
		}
	}
	activities := h.Engine.Activity(refs)
	for i, ref := range refOf {
		out.Sessions[i].LastActivityAt = activities[ref].LastActivityAt
		out.Sessions[i].ToolStartedAt = activities[ref].ToolStartedAt
	}
	return out, nil
}

func (h *handler) GetSession(ctx context.Context, req api.GetSessionRequestObject) (api.GetSessionResponseObject, error) {
	c, err := h.Sessions.Get(ctx, botID(ctx), req.SessionId)
	if err != nil {
		return nil, err
	}
	return api.GetSession200JSONResponse(sessionView(c)), nil
}

// message validates a message from the client. The text can be empty when the message has an
// attachment.
func (h *handler) message(ctx context.Context, in api.MessageInput) (session.Message, error) {
	id, err := uuid.Parse(in.Id)
	if err != nil {
		return session.Message{}, errors.Join(errInvalid, errors.New("the message ID must be a UUID"))
	}
	var ids []string
	if in.Attachments != nil {
		ids = *in.Attachments
	}
	if len(ids) > maxAttachments {
		return session.Message{}, errors.Join(errInvalid, errors.New("a message can have at most 10 attachments"))
	}
	m := session.Message{ID: id.String(), Content: session.NormalizeText(in.Text)}
	if in.TaskId != nil {
		m.TaskID = *in.TaskId
	}
	if in.AnswersQuestionId != nil {
		m.AnswersQuestion = *in.AnswersQuestionId
	}
	if in.Mode != nil {
		if !in.Mode.Valid() {
			return session.Message{}, errors.Join(errInvalid, errors.New("the mode must be followUp, steer or reject"))
		}
		m.WhenBusy = string(*in.Mode)
	}
	if (m.Content == "" && len(ids) == 0) || utf8.RuneCountInString(m.Content) > maxMessage {
		return session.Message{}, errors.Join(errInvalid, errors.New("the message must have 1 to 32000 characters"))
	}
	for i, uploadID := range ids {
		if slices.Contains(ids[:i], uploadID) {
			return session.Message{}, errors.Join(errInvalid, fmt.Errorf("the attachment %q is in the message twice", uploadID))
		}
		a, err := h.uploads(ctx).Get(uploadID)
		if errors.Is(err, upload.ErrNotFound) {
			return session.Message{}, errors.Join(errInvalid, fmt.Errorf("no upload %q; upload the file again", uploadID))
		}
		if err != nil {
			return session.Message{}, err
		}
		m.Attachments = append(m.Attachments, a)
	}
	return m, nil
}

func (h *handler) CreateBotSession(ctx context.Context, _ api.CreateBotSessionRequestObject) (api.CreateBotSessionResponseObject, error) {
	c, err := h.Sessions.Create(ctx, botID(ctx))
	if err != nil {
		return nil, err
	}
	return api.CreateBotSession201JSONResponse(sessionView(c)), nil
}

func (h *handler) SendMessage(ctx context.Context, req api.SendMessageRequestObject) (api.SendMessageResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	m, err := h.message(ctx, *req.Body)
	if err != nil {
		return nil, err
	}
	c, err := h.writableSession(ctx, req.SessionId)
	if err != nil {
		return nil, err
	}
	if _, err := h.Sessions.Send(ctx, botID(ctx), c.ChannelKey, m); err != nil {
		return nil, err
	}
	return api.SendMessage202JSONResponse{SubmissionId: m.ID}, nil
}

func (h *handler) WithdrawMessage(ctx context.Context, req api.WithdrawMessageRequestObject) (api.WithdrawMessageResponseObject, error) {
	if _, err := h.writableSession(ctx, req.SessionId); err != nil {
		return nil, err
	}
	m, err := h.Sessions.Withdraw(ctx, botID(ctx), req.SessionId, req.MessageId)
	if err != nil {
		return nil, err
	}
	out := api.WithdrawMessage200JSONResponse{Id: m.ID, Text: m.Content, Attachments: m.Attachments}
	if out.Attachments == nil {
		out.Attachments = []api.Attachment{}
	}
	return out, nil
}

// writableSession returns session id, or errReadOnly for a session that the web UI cannot write to.
func (h *handler) writableSession(ctx context.Context, id int64) (session.Session, error) {
	c, err := h.Sessions.Get(ctx, botID(ctx), id)
	if err != nil {
		return session.Session{}, err
	}
	if !c.Writable {
		return session.Session{}, errReadOnly
	}
	return c, nil
}

func (h *handler) SetSessionProject(ctx context.Context, req api.SetSessionProjectRequestObject) (api.SetSessionProjectResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	if _, err := h.Sessions.SetProject(ctx, botID(ctx), req.SessionId, req.Body.Cwd); err != nil {
		return nil, err
	}
	c, err := h.Sessions.Get(ctx, botID(ctx), req.SessionId)
	if err != nil {
		return nil, err
	}
	return api.SetSessionProject200JSONResponse(sessionView(c)), nil
}

func (h *handler) SetSessionRunner(ctx context.Context, req api.SetSessionRunnerRequestObject) (api.SetSessionRunnerResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	c, err := h.Sessions.SetRunner(ctx, botID(ctx), req.SessionId, strings.TrimSpace(req.Body.RunnerId))
	if err != nil {
		return nil, err
	}
	return api.SetSessionRunner200JSONResponse(sessionView(c)), nil
}

func (h *handler) ListBotRunners(ctx context.Context, _ api.ListBotRunnersRequestObject) (api.ListBotRunnersResponseObject, error) {
	list, err := h.Settings.SessionRunners(ctx, botID(ctx))
	if err != nil {
		return nil, err
	}
	return api.ListBotRunners200JSONResponse{Runners: list}, nil
}

func (h *handler) SetSessionSettings(ctx context.Context, req api.SetSessionSettingsRequestObject) (api.SetSessionSettingsResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	if err := h.Sessions.SetSession(ctx, botID(ctx), req.SessionId, *req.Body); err != nil {
		return nil, err
	}
	c, err := h.Sessions.Get(ctx, botID(ctx), req.SessionId)
	if err != nil {
		return nil, err
	}
	return api.SetSessionSettings200JSONResponse(sessionView(c)), nil
}

// CreateUpload stores the first part named file. The browser sends the file's base name.
func (h *handler) CreateUpload(ctx context.Context, req api.CreateUploadRequestObject) (api.CreateUploadResponseObject, error) {
	for {
		part, err := req.Body.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, errors.Join(errInvalid, errors.New("the request has no file"))
		}
		if err != nil {
			return nil, uploadError(err)
		}
		if part.FormName() != "file" {
			continue
		}
		// Part.FileName drops the directories of the name; the raw parameter shows them, so a name
		// with a path is refused, not changed.
		_, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		if err != nil {
			return nil, errors.Join(errInvalid, err)
		}
		a, err := h.uploads(ctx).Save(params["filename"], part)
		if err != nil {
			return nil, uploadError(err)
		}
		return api.CreateUpload201JSONResponse(a), nil
	}
}

// uploadError reports a body that is larger than the server reads as a file that is too large.
func uploadError(err error) error {
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return upload.ErrTooLarge
	}
	return err
}

// Images that browsers show inline. Other types, SVG too, can run scripts in a browser, so they come
// only as downloads.
var inlineTypes = []string{"image/png", "image/jpeg", "image/gif", "image/webp"}

func (h *handler) GetUpload(ctx context.Context, req api.GetUploadRequestObject) (api.GetUploadResponseObject, error) {
	f, a, err := h.uploads(ctx).Open(req.UploadId)
	if err != nil {
		return nil, err
	}
	contentType, disposition := "application/octet-stream", "attachment"
	if slices.Contains(inlineTypes, a.MimeType) {
		contentType, disposition = a.MimeType, "inline"
	}
	disposition = mime.FormatMediaType(disposition, map[string]string{"filename": a.Name})
	nosniff := "nosniff"
	return api.GetUpload200AsteriskResponse{
		Body: f, ContentType: contentType, ContentLength: a.Size,
		Headers: api.GetUpload200ResponseHeaders{ContentDisposition: &disposition, XContentTypeOptions: &nosniff},
	}, nil
}

func (h *handler) ListArtifacts(ctx context.Context, _ api.ListArtifactsRequestObject) (api.ListArtifactsResponseObject, error) {
	rows, err := h.Artifacts.List(botID(ctx))
	if err != nil {
		return nil, err
	}
	out := make([]api.Artifact, 0, len(rows))
	for _, a := range rows {
		out = append(out, api.Artifact{Name: a.Name, Size: a.Size, ModifiedAt: a.ModifiedAt})
	}
	return api.ListArtifacts200JSONResponse{Artifacts: out}, nil
}

func (h *handler) RenameArtifact(ctx context.Context, req api.RenameArtifactRequestObject) (api.RenameArtifactResponseObject, error) {
	a, err := h.Artifacts.Rename(botID(ctx), req.ArtifactName, req.Body.Name)
	if err != nil {
		return nil, err
	}
	return api.RenameArtifact200JSONResponse{Name: a.Name, Size: a.Size, ModifiedAt: a.ModifiedAt}, nil
}

func (h *handler) DeleteArtifact(ctx context.Context, req api.DeleteArtifactRequestObject) (api.DeleteArtifactResponseObject, error) {
	if err := h.Artifacts.Delete(botID(ctx), req.ArtifactName); err != nil {
		return nil, err
	}
	return api.DeleteArtifact200JSONResponse{Status: api.OkStatusOk}, nil
}

func (h *handler) GetArtifact(ctx context.Context, req api.GetArtifactRequestObject) (api.GetArtifactResponseObject, error) {
	a, f, err := h.Artifacts.Open(botID(ctx), req.ArtifactName)
	if os.IsNotExist(err) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, err
	}
	head := make([]byte, 512)
	n, err := f.ReadAt(head, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		_ = f.Close()
		return nil, err
	}
	contentType, disposition := "application/octet-stream", "attachment"
	if detected := http.DetectContentType(head[:n]); slices.Contains(inlineTypes, detected) {
		contentType, disposition = detected, "inline"
	}
	disposition = mime.FormatMediaType(disposition, map[string]string{"filename": a.Name})
	nosniff := "nosniff"
	return api.GetArtifact200AsteriskResponse{Body: f, ContentType: contentType, ContentLength: a.Size, Headers: api.GetArtifact200ResponseHeaders{ContentDisposition: &disposition, XContentTypeOptions: &nosniff}}, nil
}

func (h *handler) AbortSession(ctx context.Context, req api.AbortSessionRequestObject) (api.AbortSessionResponseObject, error) {
	if _, err := h.writableSession(ctx, req.SessionId); err != nil {
		return nil, err
	}
	if err := h.Sessions.Stop(ctx, botID(ctx), req.SessionId, req.Params.RunTarget); err != nil {
		return nil, err
	}
	return api.AbortSession200JSONResponse{Status: api.OkStatusOk}, nil
}

func (h *handler) ListSessionMonitors(ctx context.Context, req api.ListSessionMonitorsRequestObject) (api.ListSessionMonitorsResponseObject, error) {
	monitors, err := h.Sessions.ListMonitors(ctx, botID(ctx), req.SessionId)
	if err != nil {
		return nil, err
	}
	out := make([]api.Monitor, len(monitors))
	for i, t := range monitors {
		out[i] = monitorResponse(t)
	}
	return api.ListSessionMonitors200JSONResponse{Monitors: out}, nil
}

func (h *handler) CancelSessionMonitor(ctx context.Context, req api.CancelSessionMonitorRequestObject) (api.CancelSessionMonitorResponseObject, error) {
	if _, err := h.writableSession(ctx, req.SessionId); err != nil {
		return nil, err
	}
	t, err := h.Sessions.CancelMonitor(ctx, botID(ctx), req.SessionId, req.MonitorId)
	if err != nil {
		return nil, err
	}
	return api.CancelSessionMonitor200JSONResponse(monitorResponse(t)), nil
}

func monitorResponse(t session.MonitorInfo) api.Monitor {
	// This endpoint serves schedules only and keeps their existing IDs.
	return api.Monitor{Id: strings.TrimPrefix(t.ID, "schedule-"), DueAt: t.DueAt, TimeZone: t.TimeZone, Prompt: t.Prompt, State: api.MonitorState(t.State), ChainDepth: t.ChainDepth, CreatedAt: t.CreatedAt, Mode: api.MonitorMode(t.Mode), MaxRuns: t.MaxRuns, Runs: t.Runs, ExpiresAt: t.ExpiresAt, Outcome: t.Outcome}
}

func (h *handler) ListSessionOutbox(ctx context.Context, req api.ListSessionOutboxRequestObject) (api.ListSessionOutboxResponseObject, error) {
	rows, err := h.Sessions.ListOutbox(ctx, botID(ctx), req.SessionId)
	if err != nil {
		return nil, err
	}
	out := make([]api.OutboxMessage, len(rows))
	for i, r := range rows {
		out[i] = api.OutboxMessage{
			Id:          r.ID,
			Kind:        api.OutboxMessageKind(r.Kind),
			Peer:        r.Peer,
			TopicKey:    r.TopicKey,
			Body:        r.Body,
			Reason:      r.Reason,
			Status:      api.OutboxMessageStatus(r.Status),
			Error:       r.Error,
			RetryAt:     r.RetryAt,
			CreatedAt:   r.CreatedAt,
			DeliveredAt: r.DeliveredAt,
		}
	}
	return api.ListSessionOutbox200JSONResponse{Messages: out}, nil
}

func (h *handler) WatchSession(ctx context.Context, req api.WatchSessionRequestObject) (api.WatchSessionResponseObject, error) {
	body, err := h.Live.OpenWeb(ctx, botID(ctx), req.SessionId)
	if err != nil {
		return nil, err
	}
	return api.WatchSession200TexteventStreamResponse{Body: body}, nil
}

func (h *handler) GetSettings(ctx context.Context, _ api.GetSettingsRequestObject) (api.GetSettingsResponseObject, error) {
	s, err := h.Settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	out := api.GetSettings200JSONResponse{
		Providers: make([]api.Provider, len(s.Providers)),
		Builtins:  make([]api.ProviderInfo, len(s.Builtins)),
		Apis:      s.APIs,
	}
	for i, p := range s.Providers {
		out.Providers[i] = providerView(p)
	}
	for i, b := range s.Builtins {
		out.Builtins[i] = api.ProviderInfo{Id: b.ID, Name: b.Name, Source: optional(b.Source)}
	}
	return out, nil
}

func (h *handler) GetOnePassword(ctx context.Context, _ api.GetOnePasswordRequestObject) (api.GetOnePasswordResponseObject, error) {
	o, err := h.Settings.OnePassword(ctx)
	return api.GetOnePassword200JSONResponse(onePasswordView(o)), err
}

func (h *handler) SetOnePassword(ctx context.Context, req api.SetOnePasswordRequestObject) (api.SetOnePasswordResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	b := req.Body
	o, err := h.Settings.SetOnePassword(ctx, settings.OnePasswordInput{
		Mode: string(b.Mode), ConnectHost: deref(b.ConnectHost), ConnectToken: deref(b.ConnectToken), ServiceAccountToken: deref(b.ServiceAccountToken),
	})
	if err != nil {
		return nil, err
	}
	return api.SetOnePassword200JSONResponse(onePasswordView(o)), nil
}

func onePasswordView(o settings.OnePassword) api.OnePassword {
	return api.OnePassword{
		Mode: api.OnePasswordMode(o.Mode), ConnectHost: o.ConnectHost, ConnectTokenSet: o.ConnectTokenSet,
		ServiceAccountTokenSet: o.ServiceAccountTokenSet, Cli: o.CLI,
	}
}

func providerView(p settings.Provider) api.Provider {
	return api.Provider{
		Id: p.ID, Name: p.Name, Api: p.API, BaseUrl: p.BaseURL, KeySet: p.KeySet, Models: p.Models,
		ModelCount: &p.ModelCount, Error: optional(p.Error),
	}
}

// convert converts the strings of pi-ai enums to their API types.
func convert[T ~string, S ~string](values []S) []T {
	out := make([]T, len(values))
	for i, v := range values {
		out[i] = T(v)
	}
	return out
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (h *handler) ListModels(ctx context.Context, _ api.ListModelsRequestObject) (api.ListModelsResponseObject, error) {
	models, err := h.Settings.Models(ctx)
	if err != nil {
		return nil, err
	}
	out := api.ListModels200JSONResponse{Models: make([]api.Model, len(models))}
	for i, m := range models {
		out.Models[i] = api.Model{
			Provider: m.Provider, Id: m.ID, Name: m.Name, Reasoning: m.Reasoning,
			Input: convert[api.ModelInput](m.Input), ThinkingLevels: convert[api.ThinkingLevel](m.ThinkingLevels),
		}
	}
	return out, nil
}

func (h *handler) SetProvider(ctx context.Context, req api.SetProviderRequestObject) (api.SetProviderResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	b := req.Body
	p, err := h.Settings.SetProvider(ctx, settings.Provider{
		ID: req.ProviderId, API: b.Api, Name: deref(b.Name), BaseURL: strings.TrimSpace(deref(b.BaseUrl)),
		APIKey: strings.TrimSpace(deref(b.ApiKey)), Models: derefSlice(b.Models),
	})
	if err != nil {
		return nil, err
	}
	return api.SetProvider200JSONResponse(providerView(p)), nil
}

func (h *handler) ListProviderModels(ctx context.Context, req api.ListProviderModelsRequestObject) (api.ListProviderModelsResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	b := req.Body
	models, err := h.Settings.ListProviderModels(ctx, settings.Provider{
		ID: deref(b.ProviderId), API: b.Api, BaseURL: strings.TrimSpace(b.BaseUrl), APIKey: strings.TrimSpace(deref(b.ApiKey)),
	})
	if err != nil {
		return nil, err
	}
	out := api.ListProviderModels200JSONResponse{Models: make([]api.ProviderModel, len(models))}
	for i, m := range models {
		out.Models[i] = api.ProviderModel{Id: m.ID, Name: m.Name}
	}
	return out, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefSlice(s *[]string) []string {
	if s == nil {
		return nil
	}
	return *s
}

func (h *handler) DeleteProvider(ctx context.Context, req api.DeleteProviderRequestObject) (api.DeleteProviderResponseObject, error) {
	if err := h.Settings.DeleteProvider(ctx, req.ProviderId); err != nil {
		return nil, err
	}
	return api.DeleteProvider200JSONResponse(ok), nil
}

func (h *handler) SetTelegram(ctx context.Context, req api.SetTelegramRequestObject) (api.SetTelegramResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	var transport string
	if req.Body.Transport != nil {
		transport = string(*req.Body.Transport)
	}
	if err := h.Bots.SetTelegram(ctx, botID(ctx), req.Body.Token, req.Body.AllowedUsers, req.Body.Options, transport, req.Body.Webhook); err != nil {
		return nil, err
	}
	return api.SetTelegram200JSONResponse(ok), nil
}

func (h *handler) DeleteTelegram(ctx context.Context, _ api.DeleteTelegramRequestObject) (api.DeleteTelegramResponseObject, error) {
	if err := h.Bots.DeleteTelegram(ctx, botID(ctx)); err != nil {
		return nil, err
	}
	return api.DeleteTelegram200JSONResponse(ok), nil
}

func (h *handler) CreateTelegramPairing(ctx context.Context, _ api.CreateTelegramPairingRequestObject) (api.CreateTelegramPairingResponseObject, error) {
	link, expires, err := h.Bots.CreateTelegramPairing(ctx, botID(ctx))
	if err != nil {
		return nil, err
	}
	return api.CreateTelegramPairing200JSONResponse{Url: link, ExpiresAt: expires}, nil
}

func (h *handler) BeginWechatLogin(ctx context.Context, _ api.BeginWechatLoginRequestObject) (api.BeginWechatLoginResponseObject, error) {
	view, err := h.Bots.BeginWechatLogin(ctx, botID(ctx))
	if err != nil {
		return nil, err
	}
	return api.BeginWechatLogin200JSONResponse(view), nil
}

func (h *handler) PollWechatLogin(ctx context.Context, req api.PollWechatLoginRequestObject) (api.PollWechatLoginResponseObject, error) {
	code := ""
	if req.Body != nil {
		code = deref(req.Body.VerifyCode)
	}
	view, err := h.Bots.PollWechatLogin(ctx, botID(ctx), req.LoginId, code)
	if err != nil {
		return nil, err
	}
	return api.PollWechatLogin200JSONResponse(view), nil
}

func (h *handler) DeleteWechat(ctx context.Context, _ api.DeleteWechatRequestObject) (api.DeleteWechatResponseObject, error) {
	if err := h.Bots.DeleteWechat(ctx, botID(ctx)); err != nil {
		return nil, err
	}
	return api.DeleteWechat200JSONResponse(ok), nil
}

func (h *handler) GetMainSession(ctx context.Context, _ api.GetMainSessionRequestObject) (api.GetMainSessionResponseObject, error) {
	c, err := h.Sessions.Main(ctx, botID(ctx))
	if err != nil {
		return nil, err
	}
	return api.GetMainSession200JSONResponse(sessionView(c)), nil
}

func (h *handler) ClearContext(ctx context.Context, req api.ClearContextRequestObject) (api.ClearContextResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	id, err := uuid.Parse(req.Body.Id)
	if err != nil {
		return nil, errors.Join(errInvalid, err)
	}
	if _, err := h.writableSession(ctx, req.SessionId); err != nil {
		return nil, err
	}
	handoff := strings.TrimSpace(deref(req.Body.Handoff))
	if utf8.RuneCountInString(handoff) > maxMessage {
		return nil, errors.Join(errInvalid, errors.New("the handoff must have at most 32000 characters"))
	}
	if err := h.Sessions.Clear(ctx, botID(ctx), req.SessionId, id.String(), "web", handoff); err != nil {
		return nil, err
	}
	return api.ClearContext202JSONResponse{SubmissionId: id.String()}, nil
}

func (h *handler) CompactSession(ctx context.Context, req api.CompactSessionRequestObject) (api.CompactSessionResponseObject, error) {
	instructions, requestID := "", ""
	if req.Body != nil {
		instructions = strings.TrimSpace(deref(req.Body.Instructions))
		if req.Body.Id != nil {
			id, err := uuid.Parse(*req.Body.Id)
			if err != nil {
				return nil, errors.Join(errInvalid, err)
			}
			requestID = id.String()
		}
	}
	if utf8.RuneCountInString(instructions) > maxMessage {
		return nil, errors.Join(errInvalid, errors.New("the instructions must have at most 32000 characters"))
	}
	if _, err := h.writableSession(ctx, req.SessionId); err != nil {
		return nil, err
	}
	if err := h.Sessions.Compact(ctx, botID(ctx), req.SessionId, instructions, requestID); err != nil {
		return nil, err
	}
	return api.CompactSession202Response{}, nil
}

func (h *handler) ForkSession(ctx context.Context, req api.ForkSessionRequestObject) (api.ForkSessionResponseObject, error) {
	if req.Body == nil || req.Body.EntryId <= 0 {
		return nil, errInvalid
	}
	if _, err := h.writableSession(ctx, req.SessionId); err != nil {
		return nil, err
	}
	requestID := ""
	if req.Body.Id != nil {
		id, err := uuid.Parse(*req.Body.Id)
		if err != nil {
			return nil, errors.Join(errInvalid, err)
		}
		requestID = id.String()
	}
	runnerID := ""
	if req.Body.RunnerId != nil {
		runnerID = *req.Body.RunnerId
	}
	fork, err := h.Sessions.Fork(ctx, botID(ctx), req.SessionId, req.Body.EntryId, req.Body.Before != nil && *req.Body.Before, requestID, runnerID)
	if err != nil {
		return nil, err
	}
	return api.ForkSession201JSONResponse{SessionId: fork.ID}, nil
}

func (h *handler) ContinueSession(ctx context.Context, req api.ContinueSessionRequestObject) (api.ContinueSessionResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	id, err := uuid.Parse(req.Body.Id)
	if err != nil {
		return nil, errors.Join(errInvalid, err)
	}
	if _, err := h.writableSession(ctx, req.SessionId); err != nil {
		return nil, err
	}
	if err := h.Sessions.Continue(ctx, botID(ctx), req.SessionId, id.String()); err != nil {
		return nil, err
	}
	return api.ContinueSession202JSONResponse{SubmissionId: id.String()}, nil
}

func (h *handler) GetSessionItem(ctx context.Context, req api.GetSessionItemRequestObject) (api.GetSessionItemResponseObject, error) {
	item, err := h.Live.Detail(ctx, botID(ctx), req.SessionId, req.Params.Entry, req.ItemId)
	if err != nil {
		return nil, err
	}
	var out api.GetSessionItem200JSONResponse
	raw, err := json.Marshal(item)
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}

func (h *handler) GetSessionHistory(ctx context.Context, req api.GetSessionHistoryRequestObject) (api.GetSessionHistoryResponseObject, error) {
	limit := 100
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	before, beforeNotice := req.Params.Before, req.Params.BeforeNotification
	if (before == nil && beforeNotice == nil) || (before != nil && *before < 1) ||
		(beforeNotice != nil && *beforeNotice == "") || limit < 1 || limit > 200 {
		return nil, errInvalid
	}
	bot := botID(ctx)
	out := api.GetSessionHistory200JSONResponse{Items: []api.Item{}}
	if before != nil {
		page, err := h.Live.History(ctx, bot, req.SessionId, *before, limit)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(page)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, err
		}
	} else if _, err := h.Sessions.Get(ctx, bot, req.SessionId); err != nil {
		return nil, err
	}
	if beforeNotice != nil {
		page, err := h.Sessions.TimelineNotifications(ctx, bot, req.SessionId, *beforeNotice, 0, limit)
		if err != nil {
			return nil, err
		}
		notices := make([]api.TimelineNotification, len(page.Notifications))
		for i, notice := range page.Notifications {
			notices[i] = api.TimelineNotification{Id: notice.ID, Text: notice.Text, At: notice.At}
		}
		out.Notifications = &notices
		if page.NextBeforeNotification != "" {
			out.NextBeforeNotification = &page.NextBeforeNotification
		}
	}
	return out, nil
}

func (h *handler) ListDirectories(ctx context.Context, req api.ListDirectoriesRequestObject) (api.ListDirectoriesResponseObject, error) {
	id := "default"
	if req.Params.BotId != nil && strings.TrimSpace(*req.Params.BotId) != "" {
		id = strings.TrimSpace(*req.Params.BotId)
		row, err := h.Bots.Bot(ctx, id)
		if err == nil && row.ArchivedAt != 0 {
			return nil, bot.ErrNotFound
		}
		if err != nil {
			return nil, err
		}
	}
	listing, err := h.Sessions.ListDirectories(ctx, id, deref(req.Params.RunnerId), req.Params.Path)
	if err != nil {
		return nil, err
	}
	return api.ListDirectories200JSONResponse(listing), nil
}

func (h *handler) ListRecentProjects(ctx context.Context, req api.ListRecentProjectsRequestObject) (api.ListRecentProjectsResponseObject, error) {
	projects, err := h.Sessions.RecentProjects(ctx, botID(ctx), deref(req.Params.RunnerId))
	if err != nil {
		return nil, err
	}
	return api.ListRecentProjects200JSONResponse{Projects: projects}, nil
}
