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

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/automation"
	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/live"
	"github.com/recally-io/dex/internal/settings"
	"github.com/recally-io/dex/internal/upload"
	"github.com/recally-io/dex/web"
)

// Config holds what the handler serves.
type Config struct {
	Chats       *chat.Service
	Automations *automation.Service
	Settings    *settings.Service
	Live        *live.Hub
	Uploads     *upload.Store
	AgentID     string
	DataDir     string
	Version     string
	WorkerReady func(context.Context) bool
}

// The longest message, in characters, and the most attachments of one message, as in the contract.
const (
	maxMessage     = 32000
	maxAttachments = 10
)

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
			case errors.Is(err, settings.ErrInvalid), errors.Is(err, errInvalid), errors.Is(err, automation.ErrInvalid),
				errors.Is(err, upload.ErrInvalidName):
				status = http.StatusBadRequest
			case errors.Is(err, upload.ErrTooLarge):
				status = http.StatusRequestEntityTooLarge
			case errors.Is(err, errReadOnly):
				status = http.StatusForbidden
			case errors.Is(err, chat.ErrNotFound), errors.Is(err, automation.ErrNotFound), errors.Is(err, upload.ErrNotFound):
				status = http.StatusNotFound
			case errors.Is(err, chat.ErrNoModel), errors.Is(err, chat.ErrMaintenanceBusy):
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
	// The longest message is 32000 characters of up to 4 bytes. An upload has room for its
	// multipart headers.
	small, uploads := http.MaxBytesHandler(mux, 256<<10), http.MaxBytesHandler(mux, upload.MaxSize+64<<10)
	limited := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/uploads" {
			uploads.ServeHTTP(w, r)
		} else {
			small.ServeHTTP(w, r)
		}
	})
	return http.NewCrossOriginProtection().Handler(limited)
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

func chatView(c chat.Chat) api.Chat {
	out := api.Chat{
		Id: c.ID, Channel: api.ChatChannelWeb, Title: c.Title, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, Busy: c.Busy,
		Settings: c.Settings,
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

// message validates a message from the client. The text can be empty when the message has an
// attachment.
func (h *handler) message(in api.MessageInput) (chat.Message, error) {
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
		a, err := h.Uploads.Get(uploadID)
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

func (h *handler) CreateChat(ctx context.Context, req api.CreateChatRequestObject) (api.CreateChatResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	b := req.Body
	m, err := h.message(api.MessageInput{Id: b.Id, Text: b.Text, Attachments: b.Attachments})
	if err != nil {
		return nil, err
	}
	if b.Settings != nil {
		if err := h.Settings.CheckChat(ctx, *b.Settings); err != nil {
			return nil, err
		}
		m.Settings = *b.Settings
	}
	// The channel key comes from the message ID, so a retried request finds the same chat.
	chatID, err := h.Chats.Send(ctx, h.AgentID, "web:"+m.ID, m)
	if err != nil {
		return nil, err
	}
	c, err := h.Chats.Get(ctx, h.AgentID, chatID)
	if err != nil {
		return nil, err
	}
	return api.CreateChat202JSONResponse{Chat: chatView(c), SubmissionId: m.ID}, nil
}

func (h *handler) SendMessage(ctx context.Context, req api.SendMessageRequestObject) (api.SendMessageResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	m, err := h.message(*req.Body)
	if err != nil {
		return nil, err
	}
	c, err := h.writableChat(ctx, req.ChatId)
	if err != nil {
		return nil, err
	}
	if _, err := h.Chats.Send(ctx, h.AgentID, c.ChannelKey, m); err != nil {
		return nil, err
	}
	return api.SendMessage202JSONResponse{SubmissionId: m.ID}, nil
}

// writableChat returns chat id, or errReadOnly for a chat that the web UI cannot write to.
func (h *handler) writableChat(ctx context.Context, id int64) (chat.Chat, error) {
	c, err := h.Chats.Get(ctx, h.AgentID, id)
	if err != nil {
		return chat.Chat{}, err
	}
	if strings.HasPrefix(c.ChannelKey, "telegram:") {
		return chat.Chat{}, errReadOnly
	}
	return c, nil
}

func (h *handler) SetChatSettings(ctx context.Context, req api.SetChatSettingsRequestObject) (api.SetChatSettingsResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	if _, err := h.writableChat(ctx, req.ChatId); err != nil {
		return nil, err
	}
	if err := h.Settings.SetChat(ctx, req.ChatId, *req.Body); err != nil {
		return nil, err
	}
	c, err := h.Chats.Get(ctx, h.AgentID, req.ChatId)
	if err != nil {
		return nil, err
	}
	return api.SetChatSettings200JSONResponse(chatView(c)), nil
}

// CreateUpload stores the first part named file. The browser sends the file's base name.
func (h *handler) CreateUpload(_ context.Context, req api.CreateUploadRequestObject) (api.CreateUploadResponseObject, error) {
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
		a, err := h.Uploads.Save(params["filename"], part)
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

func (h *handler) GetUpload(_ context.Context, req api.GetUploadRequestObject) (api.GetUploadResponseObject, error) {
	f, a, err := h.Uploads.Open(req.UploadId)
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
