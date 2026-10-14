// Package server serves the HTTP API of api/openapi.yaml and the web UI.
package server

import (
	"context"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"
	"uuid"

	"github.com/vaayne/dex/internal/api"
	"github.com/vaayne/dex/internal/automation"
	"github.com/vaayne/dex/internal/chat"
	"github.com/vaayne/dex/internal/live"
	"github.com/vaayne/dex/internal/settings"
	"github.com/vaayne/dex/web"
)

// Config holds what the handler serves.
type Config struct {
	Chats       *chat.Service
	Automations *automation.Service
	Settings    *settings.Service
	Live        *live.Hub
	AgentID     string
	DataDir     string
	Version     string
}

// The longest message, in characters, as in the contract.
const maxMessage = 32000

// errInvalid is a request that the handler refuses with 400.
var errInvalid = errors.New("invalid request")

// errReadOnly is a message to a chat that the web UI cannot write to.
var errReadOnly = errors.New("this chat is read-only here; reply in Telegram")

// New returns the handler for the API under /api/ and the web UI at /. The API has no
// authentication; it refuses only cross-site writes, so a web page in the user's browser cannot
// send messages or change settings.
func New(cfg Config) http.Handler {
	mux := http.NewServeMux()
	strict := api.NewStrictHandlerWithOptions(&handler{cfg}, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeError(w, http.StatusBadRequest, "the request is not valid: "+err.Error())
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			status := http.StatusInternalServerError
			switch {
			case errors.Is(err, settings.ErrInvalid), errors.Is(err, errInvalid), errors.Is(err, automation.ErrInvalid):
				status = http.StatusBadRequest
			case errors.Is(err, errReadOnly):
				status = http.StatusForbidden
			case errors.Is(err, chat.ErrNotFound), errors.Is(err, automation.ErrNotFound):
				status = http.StatusNotFound
			case errors.Is(err, chat.ErrNoModel):
				status = http.StatusConflict
			default:
				slog.Error("api", "err", err)
			}
			writeError(w, status, err.Error())
		},
	})
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{BaseRouter: mux})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	mux.Handle("/", web.Handler())
	// The longest message is 32000 characters of up to 4 bytes.
	return http.NewCrossOriginProtection().Handler(http.MaxBytesHandler(mux, 256<<10))
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.MarshalWrite(w, api.Error{Error: message}); err != nil {
		slog.Error("write response", "err", err)
	}
}

type handler struct{ Config }

var ok = api.Ok{Status: api.OkStatusOk}

func (h *handler) GetHealth(context.Context, api.GetHealthRequestObject) (api.GetHealthResponseObject, error) {
	return api.GetHealth200JSONResponse{Status: api.HealthStatusOk, Version: h.Version, DataDir: h.DataDir}, nil
}

func chatView(c chat.Chat) api.Chat {
	out := api.Chat{
		Id: c.ID, Channel: api.ChatChannelWeb, Title: c.Title, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, Busy: c.Busy,
	}
	if strings.HasPrefix(c.ChannelKey, "telegram:") {
		out.Channel = api.ChatChannelTelegram
	}
	if id, ok := automation.ChatAutomation(c.ChannelKey); ok {
		out.Channel, out.AutomationId = api.ChatChannelAutomation, &id
	}
	return out
}

func (h *handler) ListChats(ctx context.Context, _ api.ListChatsRequestObject) (api.ListChatsResponseObject, error) {
	chats, err := h.Chats.List(ctx, h.AgentID)
	if err != nil {
		return nil, err
	}
	out := api.ListChats200JSONResponse{Chats: make([]api.Chat, len(chats))}
	for i, c := range chats {
		out.Chats[i] = chatView(c)
	}
	return out, nil
}

func (h *handler) GetChat(ctx context.Context, req api.GetChatRequestObject) (api.GetChatResponseObject, error) {
	c, err := h.Chats.Get(ctx, h.AgentID, req.ChatId)
	if err != nil {
		return nil, err
	}
	return api.GetChat200JSONResponse(chatView(c)), nil
}

// message validates a message from the client and returns its ID and text.
func message(in *api.MessageInput) (string, string, error) {
	if in == nil {
		return "", "", errInvalid
	}
	id, err := uuid.Parse(in.Id)
	if err != nil {
		return "", "", errors.Join(errInvalid, errors.New("the message ID must be a UUID"))
	}
	text := strings.TrimSpace(in.Text)
	if text == "" || utf8.RuneCountInString(text) > maxMessage {
		return "", "", errors.Join(errInvalid, errors.New("the message must have 1 to 32000 characters"))
	}
	return id.String(), text, nil
}

func (h *handler) CreateChat(ctx context.Context, req api.CreateChatRequestObject) (api.CreateChatResponseObject, error) {
	id, text, err := message(req.Body)
	if err != nil {
		return nil, err
	}
	// The channel key comes from the message ID, so a retried request finds the same chat.
	chatID, err := h.Chats.Post(ctx, h.AgentID, "web:"+id, "", id, text)
	if err != nil {
		return nil, err
	}
	c, err := h.Chats.Get(ctx, h.AgentID, chatID)
	if err != nil {
		return nil, err
	}
	return api.CreateChat202JSONResponse{Chat: chatView(c), SubmissionId: id}, nil
}

func (h *handler) SendMessage(ctx context.Context, req api.SendMessageRequestObject) (api.SendMessageResponseObject, error) {
	id, text, err := message(req.Body)
	if err != nil {
		return nil, err
	}
	c, err := h.Chats.Get(ctx, h.AgentID, req.ChatId)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(c.ChannelKey, "telegram:") {
		return nil, errReadOnly
	}
	if _, err := h.Chats.Post(ctx, h.AgentID, c.ChannelKey, "", id, text); err != nil {
		return nil, err
	}
	return api.SendMessage202JSONResponse{SubmissionId: id}, nil
}

func (h *handler) AbortChat(ctx context.Context, req api.AbortChatRequestObject) (api.AbortChatResponseObject, error) {
	if err := h.Chats.Abort(ctx, h.AgentID, req.ChatId); err != nil {
		return nil, err
	}
	return api.AbortChat200JSONResponse(ok), nil
}

func (h *handler) WatchChat(ctx context.Context, req api.WatchChatRequestObject) (api.WatchChatResponseObject, error) {
	body, err := h.Live.Open(ctx, req.ChatId)
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
		Agent:     s.Agent,
		Providers: make([]api.Provider, len(s.Providers)),
		Builtins:  make([]api.ProviderInfo, len(s.Builtins)),
		Apis:      s.APIs,
		Telegram:  api.Telegram{Enabled: s.Telegram.Enabled, AllowedUsers: s.Telegram.AllowedUsers},
	}
	for i, p := range s.Providers {
		out.Providers[i] = providerView(p)
	}
	for i, b := range s.Builtins {
		out.Builtins[i] = api.ProviderInfo{Id: b.ID, Name: b.Name, Source: optional(b.Source)}
	}
	out.Telegram.BotId = optional(s.Telegram.BotID)
	return out, nil
}

func providerView(p settings.Provider) api.Provider {
	return api.Provider{
		Id: p.ID, Name: p.Name, Api: p.API, BaseUrl: p.BaseURL, KeySet: p.KeySet, Models: p.Models,
		ModelCount: &p.ModelCount, Error: optional(p.Error),
	}
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
		out.Models[i] = api.Model{Provider: m.Provider, Id: m.ID, Name: m.Name}
	}
	return out, nil
}

func (h *handler) SetAgent(ctx context.Context, req api.SetAgentRequestObject) (api.SetAgentResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	err := h.Settings.SetAgent(ctx, *req.Body)
	if err != nil {
		return nil, err
	}
	return api.SetAgent200JSONResponse(ok), nil
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
	if err := h.Settings.SetTelegram(ctx, req.Body.Token, req.Body.AllowedUsers); err != nil {
		return nil, err
	}
	return api.SetTelegram200JSONResponse(ok), nil
}

func (h *handler) DeleteTelegram(ctx context.Context, _ api.DeleteTelegramRequestObject) (api.DeleteTelegramResponseObject, error) {
	if err := h.Settings.DeleteTelegram(ctx); err != nil {
		return nil, err
	}
	return api.DeleteTelegram200JSONResponse(ok), nil
}
