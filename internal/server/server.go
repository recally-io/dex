// Package server serves the HTTP API of api/openapi.yaml and the web UI.
package server

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/recally-io/dex/internal/agent"
	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/automation"
	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/live"
	"github.com/recally-io/dex/internal/settings"
	"github.com/recally-io/dex/internal/upload"
	"github.com/recally-io/dex/internal/worker"
	"github.com/recally-io/dex/web"
)

// Config holds what the handler serves.
type Config struct {
	Chats       *chat.Service
	Automations *automation.Service
	Settings    *settings.Service
	Agents      *agent.Service
	Live        *live.Hub
	DataDir     string
	Version     string
	WorkerReady func(context.Context) bool
	// Worker calls the worker for the agent's extensions.
	Worker worker.CallFunc
	// RestartWorker restarts the worker, which connects the MCP servers again.
	RestartWorker func(context.Context) error
	// Token is the access token that the API requires, or empty for no authentication.
	Token string
}

// The longest message, in characters, and the most attachments of one message, as in the contract.
const (
	maxMessage     = 32000
	maxAttachments = 10
)

// errInvalid is a request that the handler refuses with 400.
var errInvalid = errors.New("invalid request")

// errReadOnly is a message to a chat that the web UI cannot write to.
var errReadOnly = errors.New("this session is read-only; send messages in main or a Chat session")

// New returns the handler for the API under /api/ and the web UI at /. The API requires
// cfg.Token when it is set. It refuses cross-site writes, so a web page in the user's browser
// cannot send messages or change settings, and cannot sign in.
func New(cfg Config) http.Handler {
	mux := http.NewServeMux()
	h := &handler{cfg}
	strict := api.NewStrictHandlerWithOptions(h, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeError(w, http.StatusBadRequest, "the request is not valid: "+err.Error())
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			status := http.StatusInternalServerError
			switch {
			case errors.Is(err, settings.ErrInvalid), errors.Is(err, agent.ErrInvalid), errors.Is(err, errInvalid), errors.Is(err, automation.ErrInvalid),
				errors.Is(err, upload.ErrInvalidName), errors.Is(err, chat.ErrResetID):
				status = http.StatusBadRequest
			case errors.Is(err, upload.ErrTooLarge):
				status = http.StatusRequestEntityTooLarge
			case errors.Is(err, errReadOnly):
				status = http.StatusForbidden
			case errors.Is(err, agent.ErrNotFound), errors.Is(err, chat.ErrNotFound), errors.Is(err, automation.ErrNotFound), errors.Is(err, upload.ErrNotFound), errors.Is(err, chat.ErrTaskNotFound):
				status = http.StatusNotFound
			case errors.Is(err, agent.ErrCredentialInUse), errors.Is(err, chat.ErrNoModel), errors.Is(err, chat.ErrMaintenanceBusy), errors.Is(err, chat.ErrConversationBusy):
				status = http.StatusConflict
			default:
				slog.Error("api", "err", err)
			}
			writeError(w, status, err.Error())
		},
	})
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{BaseRouter: mux, Middlewares: []api.MiddlewareFunc{h.withAgent}})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	mux.Handle("/", web.Handler())
	// The longest message is 32000 characters of up to 4 bytes. An upload has room for its
	// multipart headers.
	small, uploads := http.MaxBytesHandler(mux, 256<<10), http.MaxBytesHandler(mux, upload.MaxSize+64<<10)
	limited := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) == 5 && parts[1] == "api" && parts[2] == "agents" && parts[4] == "uploads" {
			uploads.ServeHTTP(w, r)
		} else {
			small.ServeHTTP(w, r)
		}
	})
	return http.NewCrossOriginProtection().Handler(requireToken(cfg.Token, limited))
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.MarshalWrite(w, api.Error{Error: message}); err != nil {
		slog.Error("write response", "err", err)
	}
}

type handler struct{ Config }

type agentKey struct{}

func agentID(ctx context.Context) string {
	return ctx.Value(agentKey{}).(string) //nolint:forcetypeassert // withAgent sets this value on every agent route.
}

// withAgent checks the path before the strict handler decodes the body or opens storage.
func (h *handler) withAgent(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := r.PathValue("agentId"); id != "" {
			if _, err := h.Agents.Agent(r.Context(), id); err != nil {
				status := http.StatusInternalServerError
				if errors.Is(err, agent.ErrNotFound) {
					status = http.StatusNotFound
				} else {
					slog.Error("read request agent", "err", err)
				}
				writeError(w, status, err.Error())
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), agentKey{}, id))
		}
		next.ServeHTTP(w, r)
	})
}

func (h *handler) uploads(ctx context.Context) *upload.Store {
	return upload.New(agent.Home(h.DataDir, agentID(ctx)))
}

var ok = api.Ok{Status: api.OkStatusOk}

func (h *handler) GetHealth(ctx context.Context, _ api.GetHealthRequestObject) (api.GetHealthResponseObject, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	ready := h.WorkerReady != nil && h.WorkerReady(ctx)
	return api.GetHealth200JSONResponse{Status: api.HealthStatusOk, Version: h.Version, DataDir: h.DataDir, WorkerReady: ready}, nil
}
func (h *handler) AcquireMaintenance(ctx context.Context, _ api.AcquireMaintenanceRequestObject) (api.AcquireMaintenanceResponseObject, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := h.Chats.AcquireMaintenance(ctx); err != nil {
		return nil, err
	}
	return api.AcquireMaintenance200JSONResponse(ok), nil
}
func (h *handler) ReleaseMaintenance(ctx context.Context, _ api.ReleaseMaintenanceRequestObject) (api.ReleaseMaintenanceResponseObject, error) {
	if err := h.Chats.ReleaseMaintenance(ctx); err != nil {
		return nil, err
	}
	return api.ReleaseMaintenance200JSONResponse(ok), nil
}

func (h *handler) GetExtensions(ctx context.Context, _ api.GetExtensionsRequestObject) (api.GetExtensionsResponseObject, error) {
	var out api.GetExtensions200JSONResponse
	if err := h.Worker(ctx, "extensions", worker.AgentParams{AgentID: agentID(ctx)}, &out); err != nil {
		return nil, err
	}
	return out, nil
}
func (h *handler) ReloadExtensions(ctx context.Context, _ api.ReloadExtensionsRequestObject) (api.ReloadExtensionsResponseObject, error) {
	// The maintenance lease refuses a reload while the agent has work, and holds new work until the
	// new worker runs. The new worker gets the lease again before it accepts calls.
	if err := h.Chats.AcquireReload(ctx); err != nil {
		return nil, err
	}
	defer func() {
		release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := h.Chats.ReleaseReload(release); err != nil {
			slog.Error("release maintenance after an extension reload", "err", err)
		}
	}()
	// A worker that starts with an mcp.json that is not valid has no MCP servers. Keep the loaded ones.
	var files api.Extensions
	if err := h.Worker(ctx, "extensions", worker.AgentParams{AgentID: agentID(ctx), Preflight: true}, &files); err != nil {
		return nil, err
	}
	if len(files.Errors) > 0 {
		agents, err := h.Agents.ListAgents(ctx)
		if err != nil {
			return nil, err
		}
		for i, message := range files.Errors {
			for _, a := range agents {
				if tail, found := strings.CutPrefix(message, a.ID+": "); found {
					files.Errors[i] = a.Name + ": " + tail
					break
				}
			}
		}
		return nil, fmt.Errorf("%w: %s", errInvalid, strings.Join(files.Errors, "\n"))
	}
	if err := h.RestartWorker(ctx); err != nil {
		return nil, err
	}
	// The agent opens now, under the lease, so the response shows the state of each server.
	var out api.ReloadExtensions200JSONResponse
	if err := h.Worker(ctx, "extensions", worker.AgentParams{AgentID: agentID(ctx), Open: true}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func chatView(c chat.Chat) api.Chat {
	out := api.Chat{
		Id: c.ID, Channel: api.ChatChannelWeb, Title: c.Title, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, Busy: c.Busy,
		Settings: c.Settings, Main: c.ChannelKey == chat.MainKey, ReadOnly: !chat.Writable(c.ChannelKey),
	}
	if strings.HasPrefix(c.ChannelKey, "telegram:") {
		out.Channel = api.ChatChannelTelegram
	}
	if strings.HasPrefix(c.ChannelKey, "wechat:") {
		out.Channel = api.ChatChannelWechat
	}
	if strings.HasPrefix(c.ChannelKey, "feishu:") {
		out.Channel = api.ChatChannelFeishu
	}
	if strings.HasPrefix(c.ChannelKey, "task:") {
		out.Channel = api.ChatChannelTask
	}
	if id, ok := automation.ChatAutomation(c.ChannelKey); ok {
		out.Channel, out.AutomationId = api.ChatChannelAutomation, &id
	}
	return out
}

func (h *handler) ListChats(ctx context.Context, _ api.ListChatsRequestObject) (api.ListChatsResponseObject, error) {
	chats, err := h.Chats.List(ctx, agentID(ctx))
	if err != nil {
		return nil, err
	}
	tasks, err := h.Chats.TaskRecords(ctx, agentID(ctx))
	if err != nil {
		return nil, err
	}
	byConversation := make(map[int64]api.TaskSummary, len(tasks))
	for _, t := range tasks {
		byConversation[t.ConversationID] = taskSummary(t)
	}
	out := api.ListChats200JSONResponse{Chats: make([]api.Chat, len(chats))}
	for i, c := range chats {
		out.Chats[i] = chatView(c)
		if t, ok := byConversation[c.ID]; ok {
			out.Chats[i].Task = &t
		}
	}
	return out, nil
}

func (h *handler) GetChat(ctx context.Context, req api.GetChatRequestObject) (api.GetChatResponseObject, error) {
	c, err := h.Chats.Get(ctx, agentID(ctx), req.ChatId)
	if err != nil {
		return nil, err
	}
	return api.GetChat200JSONResponse(chatView(c)), nil
}

// message validates a message from the client. The text can be empty when the message has an
// attachment.
func (h *handler) message(ctx context.Context, in api.MessageInput) (chat.Message, error) {
	id, err := uuid.Parse(in.Id)
	if err != nil {
		return chat.Message{}, errors.Join(errInvalid, errors.New("the message ID must be a UUID"))
	}
	var ids []string
	if in.Attachments != nil {
		ids = *in.Attachments
	}
	if len(ids) > maxAttachments {
		return chat.Message{}, errors.Join(errInvalid, errors.New("a message can have at most 10 attachments"))
	}
	m := chat.Message{ID: id.String(), Content: strings.TrimSpace(in.Text)}
	if (m.Content == "" && len(ids) == 0) || utf8.RuneCountInString(m.Content) > maxMessage {
		return chat.Message{}, errors.Join(errInvalid, errors.New("the message must have 1 to 32000 characters"))
	}
	for i, uploadID := range ids {
		if slices.Contains(ids[:i], uploadID) {
			return chat.Message{}, errors.Join(errInvalid, fmt.Errorf("the attachment %q is in the message twice", uploadID))
		}
		a, err := h.uploads(ctx).Get(uploadID)
		if errors.Is(err, upload.ErrNotFound) {
			return chat.Message{}, errors.Join(errInvalid, fmt.Errorf("no upload %q; upload the file again", uploadID))
		}
		if err != nil {
			return chat.Message{}, err
		}
		m.Attachments = append(m.Attachments, a)
	}
	return m, nil
}

func (h *handler) CreateChat(ctx context.Context, _ api.CreateChatRequestObject) (api.CreateChatResponseObject, error) {
	c, err := h.Chats.Create(ctx, agentID(ctx))
	if err != nil {
		return nil, err
	}
	return api.CreateChat201JSONResponse(chatView(c)), nil
}

func (h *handler) SendMessage(ctx context.Context, req api.SendMessageRequestObject) (api.SendMessageResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	m, err := h.message(ctx, *req.Body)
	if err != nil {
		return nil, err
	}
	c, err := h.writableChat(ctx, req.ChatId)
	if err != nil {
		return nil, err
	}
	if _, err := h.Chats.Send(ctx, agentID(ctx), c.ChannelKey, m); err != nil {
		return nil, err
	}
	return api.SendMessage202JSONResponse{SubmissionId: m.ID}, nil
}

// writableChat returns chat id, or errReadOnly for a chat that the web UI cannot write to.
func (h *handler) writableChat(ctx context.Context, id int64) (chat.Chat, error) {
	c, err := h.Chats.Get(ctx, agentID(ctx), id)
	if err != nil {
		return chat.Chat{}, err
	}
	if !chat.Writable(c.ChannelKey) {
		return chat.Chat{}, errReadOnly
	}
	return c, nil
}

func (h *handler) SetChatSettings(ctx context.Context, req api.SetChatSettingsRequestObject) (api.SetChatSettingsResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	c, err := h.Chats.Get(ctx, agentID(ctx), req.ChatId)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(c.ChannelKey, "automation:") {
		return nil, errReadOnly
	}
	if err := h.Chats.SetChat(ctx, agentID(ctx), req.ChatId, *req.Body, h.Settings.CheckChat); err != nil {
		return nil, err
	}
	c, err = h.Chats.Get(ctx, agentID(ctx), req.ChatId)
	if err != nil {
		return nil, err
	}
	return api.SetChatSettings200JSONResponse(chatView(c)), nil
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

func (h *handler) AbortChat(ctx context.Context, req api.AbortChatRequestObject) (api.AbortChatResponseObject, error) {
	if _, err := h.writableChat(ctx, req.ChatId); err != nil {
		return nil, err
	}
	if err := h.Chats.Stop(ctx, agentID(ctx), req.ChatId); err != nil {
		return nil, err
	}
	return api.AbortChat200JSONResponse{Status: api.OkStatusOk}, nil
}

func (h *handler) WatchChat(ctx context.Context, req api.WatchChatRequestObject) (api.WatchChatResponseObject, error) {
	body, err := h.Live.Open(ctx, agentID(ctx), req.ChatId)
	if err != nil {
		return nil, err
	}
	return api.WatchChat200TexteventStreamResponse{Body: body}, nil
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

func providerView(p settings.Provider) api.Provider {
	return api.Provider{
		Id: p.ID, Name: p.Name, Api: p.API, BaseUrl: p.BaseURL, KeySet: p.KeySet, Models: p.Models,
		ModelCount: &p.ModelCount, Error: optional(p.Error),
	}
}

// convert converts the strings of pi-ai enums to their API types.
func convert[T ~string](values []string) []T {
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
	if err := h.Agents.SetTelegram(ctx, agentID(ctx), req.Body.Token, req.Body.AllowedUsers); err != nil {
		return nil, err
	}
	return api.SetTelegram200JSONResponse(ok), nil
}

func (h *handler) DeleteTelegram(ctx context.Context, _ api.DeleteTelegramRequestObject) (api.DeleteTelegramResponseObject, error) {
	if err := h.Agents.DeleteTelegram(ctx, agentID(ctx)); err != nil {
		return nil, err
	}
	return api.DeleteTelegram200JSONResponse(ok), nil
}

func (h *handler) CreateTelegramPairing(ctx context.Context, _ api.CreateTelegramPairingRequestObject) (api.CreateTelegramPairingResponseObject, error) {
	link, expires, err := h.Agents.CreateTelegramPairing(ctx, agentID(ctx))
	if err != nil {
		return nil, err
	}
	return api.CreateTelegramPairing200JSONResponse{Url: link, ExpiresAt: expires}, nil
}

func (h *handler) BeginWechatLogin(ctx context.Context, _ api.BeginWechatLoginRequestObject) (api.BeginWechatLoginResponseObject, error) {
	view, err := h.Agents.BeginWechatLogin(ctx, agentID(ctx))
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
	view, err := h.Agents.PollWechatLogin(ctx, agentID(ctx), req.LoginId, code)
	if err != nil {
		return nil, err
	}
	return api.PollWechatLogin200JSONResponse(view), nil
}

func (h *handler) DeleteWechat(ctx context.Context, _ api.DeleteWechatRequestObject) (api.DeleteWechatResponseObject, error) {
	if err := h.Agents.DeleteWechat(ctx, agentID(ctx)); err != nil {
		return nil, err
	}
	return api.DeleteWechat200JSONResponse(ok), nil
}

func (h *handler) GetMainChat(ctx context.Context, _ api.GetMainChatRequestObject) (api.GetMainChatResponseObject, error) {
	c, err := h.Chats.Main(ctx, agentID(ctx))
	if err != nil {
		return nil, err
	}
	return api.GetMainChat200JSONResponse(chatView(c)), nil
}

func (h *handler) ClearContext(ctx context.Context, req api.ClearContextRequestObject) (api.ClearContextResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	id, err := uuid.Parse(req.Body.Id)
	if err != nil {
		return nil, errors.Join(errInvalid, err)
	}
	if _, err := h.writableChat(ctx, req.ChatId); err != nil {
		return nil, err
	}
	if err := h.Chats.Clear(ctx, agentID(ctx), req.ChatId, id.String(), "web"); err != nil {
		return nil, err
	}
	return api.ClearContext202JSONResponse{SubmissionId: id.String()}, nil
}

func (h *handler) GetChatHistory(ctx context.Context, req api.GetChatHistoryRequestObject) (api.GetChatHistoryResponseObject, error) {
	limit := 100
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	if req.Params.Before < 1 || limit < 1 || limit > 200 {
		return nil, errInvalid
	}
	page, err := h.Live.History(ctx, agentID(ctx), req.ChatId, req.Params.Before, limit)
	if err != nil {
		return nil, err
	}
	var out api.GetChatHistory200JSONResponse
	// The worker renders the same Item contract for history and live events.
	raw, err := json.Marshal(page)
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}
