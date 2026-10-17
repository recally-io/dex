package server

import (
	"context"
	"net/http"

	"github.com/recally-io/dex/internal/api"
)

// Agent routes share the legacy handlers. withAgent binds their validated path ID.
// Remove these adapters with the default-agent aliases after the web UI migrates.

func (h *handler) AgentGetTask(ctx context.Context, req api.AgentGetTaskRequestObject) (api.AgentGetTaskResponseObject, error) {
	response, err := h.GetTask(ctx, api.GetTaskRequestObject{TaskId: req.TaskId})
	return agentGetTaskResponse{response}, err
}

type agentGetTaskResponse struct{ api.GetTaskResponseObject }

func (r agentGetTaskResponse) VisitAgentGetTaskResponse(w http.ResponseWriter) error {
	return r.VisitGetTaskResponse(w)
}

func (h *handler) AgentGetMainChat(ctx context.Context, _ api.AgentGetMainChatRequestObject) (api.AgentGetMainChatResponseObject, error) {
	response, err := h.GetMainChat(ctx, api.GetMainChatRequestObject{})
	return agentGetMainChatResponse{response}, err
}

type agentGetMainChatResponse struct{ api.GetMainChatResponseObject }

func (r agentGetMainChatResponse) VisitAgentGetMainChatResponse(w http.ResponseWriter) error {
	return r.VisitGetMainChatResponse(w)
}

func (h *handler) AgentListChats(ctx context.Context, _ api.AgentListChatsRequestObject) (api.AgentListChatsResponseObject, error) {
	response, err := h.ListChats(ctx, api.ListChatsRequestObject{})
	return agentListChatsResponse{response}, err
}

type agentListChatsResponse struct{ api.ListChatsResponseObject }

func (r agentListChatsResponse) VisitAgentListChatsResponse(w http.ResponseWriter) error {
	return r.VisitListChatsResponse(w)
}

func (h *handler) AgentCreateChat(ctx context.Context, _ api.AgentCreateChatRequestObject) (api.AgentCreateChatResponseObject, error) {
	response, err := h.CreateChat(ctx, api.CreateChatRequestObject{})
	return agentCreateChatResponse{response}, err
}

type agentCreateChatResponse struct{ api.CreateChatResponseObject }

func (r agentCreateChatResponse) VisitAgentCreateChatResponse(w http.ResponseWriter) error {
	return r.VisitCreateChatResponse(w)
}

func (h *handler) AgentGetChat(ctx context.Context, req api.AgentGetChatRequestObject) (api.AgentGetChatResponseObject, error) {
	response, err := h.GetChat(ctx, api.GetChatRequestObject{ChatId: req.ChatId})
	return agentGetChatResponse{response}, err
}

type agentGetChatResponse struct{ api.GetChatResponseObject }

func (r agentGetChatResponse) VisitAgentGetChatResponse(w http.ResponseWriter) error {
	return r.VisitGetChatResponse(w)
}

func (h *handler) AgentSetChatSettings(ctx context.Context, req api.AgentSetChatSettingsRequestObject) (api.AgentSetChatSettingsResponseObject, error) {
	response, err := h.SetChatSettings(ctx, api.SetChatSettingsRequestObject{ChatId: req.ChatId, Body: req.Body})
	return agentSetChatSettingsResponse{response}, err
}

type agentSetChatSettingsResponse struct {
	api.SetChatSettingsResponseObject
}

func (r agentSetChatSettingsResponse) VisitAgentSetChatSettingsResponse(w http.ResponseWriter) error {
	return r.VisitSetChatSettingsResponse(w)
}

func (h *handler) AgentSendMessage(ctx context.Context, req api.AgentSendMessageRequestObject) (api.AgentSendMessageResponseObject, error) {
	response, err := h.SendMessage(ctx, api.SendMessageRequestObject{ChatId: req.ChatId, Body: req.Body})
	return agentSendMessageResponse{response}, err
}

type agentSendMessageResponse struct{ api.SendMessageResponseObject }

func (r agentSendMessageResponse) VisitAgentSendMessageResponse(w http.ResponseWriter) error {
	return r.VisitSendMessageResponse(w)
}

func (h *handler) AgentClearContext(ctx context.Context, req api.AgentClearContextRequestObject) (api.AgentClearContextResponseObject, error) {
	response, err := h.ClearContext(ctx, api.ClearContextRequestObject{ChatId: req.ChatId, Body: (*api.ClearContextJSONRequestBody)(req.Body)})
	return agentClearContextResponse{response}, err
}

type agentClearContextResponse struct{ api.ClearContextResponseObject }

func (r agentClearContextResponse) VisitAgentClearContextResponse(w http.ResponseWriter) error {
	return r.VisitClearContextResponse(w)
}

func (h *handler) AgentGetChatHistory(ctx context.Context, req api.AgentGetChatHistoryRequestObject) (api.AgentGetChatHistoryResponseObject, error) {
	response, err := h.GetChatHistory(ctx, api.GetChatHistoryRequestObject{ChatId: req.ChatId, Params: api.GetChatHistoryParams(req.Params)})
	return agentGetChatHistoryResponse{response}, err
}

type agentGetChatHistoryResponse struct {
	api.GetChatHistoryResponseObject
}

func (r agentGetChatHistoryResponse) VisitAgentGetChatHistoryResponse(w http.ResponseWriter) error {
	return r.VisitGetChatHistoryResponse(w)
}

func (h *handler) AgentAbortChat(ctx context.Context, req api.AgentAbortChatRequestObject) (api.AgentAbortChatResponseObject, error) {
	response, err := h.AbortChat(ctx, api.AbortChatRequestObject{ChatId: req.ChatId})
	return agentAbortChatResponse{response}, err
}

type agentAbortChatResponse struct{ api.AbortChatResponseObject }

func (r agentAbortChatResponse) VisitAgentAbortChatResponse(w http.ResponseWriter) error {
	return r.VisitAbortChatResponse(w)
}

func (h *handler) AgentWatchChat(ctx context.Context, req api.AgentWatchChatRequestObject) (api.AgentWatchChatResponseObject, error) {
	response, err := h.WatchChat(ctx, api.WatchChatRequestObject{ChatId: req.ChatId})
	return agentWatchChatResponse{response}, err
}

type agentWatchChatResponse struct{ api.WatchChatResponseObject }

func (r agentWatchChatResponse) VisitAgentWatchChatResponse(w http.ResponseWriter) error {
	return r.VisitWatchChatResponse(w)
}

func (h *handler) AgentCreateUpload(ctx context.Context, req api.AgentCreateUploadRequestObject) (api.AgentCreateUploadResponseObject, error) {
	response, err := h.CreateUpload(ctx, api.CreateUploadRequestObject{Body: req.Body})
	return agentCreateUploadResponse{response}, err
}

type agentCreateUploadResponse struct{ api.CreateUploadResponseObject }

func (r agentCreateUploadResponse) VisitAgentCreateUploadResponse(w http.ResponseWriter) error {
	return r.VisitCreateUploadResponse(w)
}

func (h *handler) AgentGetUpload(ctx context.Context, req api.AgentGetUploadRequestObject) (api.AgentGetUploadResponseObject, error) {
	response, err := h.GetUpload(ctx, api.GetUploadRequestObject{UploadId: req.UploadId})
	return agentGetUploadResponse{response}, err
}

type agentGetUploadResponse struct{ api.GetUploadResponseObject }

func (r agentGetUploadResponse) VisitAgentGetUploadResponse(w http.ResponseWriter) error {
	return r.VisitGetUploadResponse(w)
}

func (h *handler) AgentGetExtensions(ctx context.Context, _ api.AgentGetExtensionsRequestObject) (api.AgentGetExtensionsResponseObject, error) {
	response, err := h.GetExtensions(ctx, api.GetExtensionsRequestObject{})
	return agentGetExtensionsResponse{response}, err
}

type agentGetExtensionsResponse struct {
	api.GetExtensionsResponseObject
}

func (r agentGetExtensionsResponse) VisitAgentGetExtensionsResponse(w http.ResponseWriter) error {
	return r.VisitGetExtensionsResponse(w)
}

func (h *handler) AgentReloadExtensions(ctx context.Context, _ api.AgentReloadExtensionsRequestObject) (api.AgentReloadExtensionsResponseObject, error) {
	response, err := h.ReloadExtensions(ctx, api.ReloadExtensionsRequestObject{})
	return agentReloadExtensionsResponse{response}, err
}

type agentReloadExtensionsResponse struct {
	api.ReloadExtensionsResponseObject
}

func (r agentReloadExtensionsResponse) VisitAgentReloadExtensionsResponse(w http.ResponseWriter) error {
	return r.VisitReloadExtensionsResponse(w)
}

func (h *handler) AgentListAutomations(ctx context.Context, _ api.AgentListAutomationsRequestObject) (api.AgentListAutomationsResponseObject, error) {
	response, err := h.ListAutomations(ctx, api.ListAutomationsRequestObject{})
	return agentListAutomationsResponse{response}, err
}

type agentListAutomationsResponse struct {
	api.ListAutomationsResponseObject
}

func (r agentListAutomationsResponse) VisitAgentListAutomationsResponse(w http.ResponseWriter) error {
	return r.VisitListAutomationsResponse(w)
}

func (h *handler) AgentCreateAutomation(ctx context.Context, req api.AgentCreateAutomationRequestObject) (api.AgentCreateAutomationResponseObject, error) {
	response, err := h.CreateAutomation(ctx, api.CreateAutomationRequestObject{Body: req.Body})
	return agentCreateAutomationResponse{response}, err
}

type agentCreateAutomationResponse struct {
	api.CreateAutomationResponseObject
}

func (r agentCreateAutomationResponse) VisitAgentCreateAutomationResponse(w http.ResponseWriter) error {
	return r.VisitCreateAutomationResponse(w)
}

func (h *handler) AgentGetAutomation(ctx context.Context, req api.AgentGetAutomationRequestObject) (api.AgentGetAutomationResponseObject, error) {
	response, err := h.GetAutomation(ctx, api.GetAutomationRequestObject{AutomationId: req.AutomationId})
	return agentGetAutomationResponse{response}, err
}

type agentGetAutomationResponse struct {
	api.GetAutomationResponseObject
}

func (r agentGetAutomationResponse) VisitAgentGetAutomationResponse(w http.ResponseWriter) error {
	return r.VisitGetAutomationResponse(w)
}

func (h *handler) AgentUpdateAutomation(ctx context.Context, req api.AgentUpdateAutomationRequestObject) (api.AgentUpdateAutomationResponseObject, error) {
	response, err := h.UpdateAutomation(ctx, api.UpdateAutomationRequestObject{AutomationId: req.AutomationId, Body: req.Body})
	return agentUpdateAutomationResponse{response}, err
}

type agentUpdateAutomationResponse struct {
	api.UpdateAutomationResponseObject
}

func (r agentUpdateAutomationResponse) VisitAgentUpdateAutomationResponse(w http.ResponseWriter) error {
	return r.VisitUpdateAutomationResponse(w)
}

func (h *handler) AgentDeleteAutomation(ctx context.Context, req api.AgentDeleteAutomationRequestObject) (api.AgentDeleteAutomationResponseObject, error) {
	response, err := h.DeleteAutomation(ctx, api.DeleteAutomationRequestObject{AutomationId: req.AutomationId})
	return agentDeleteAutomationResponse{response}, err
}

type agentDeleteAutomationResponse struct {
	api.DeleteAutomationResponseObject
}

func (r agentDeleteAutomationResponse) VisitAgentDeleteAutomationResponse(w http.ResponseWriter) error {
	return r.VisitDeleteAutomationResponse(w)
}

func (h *handler) AgentRunAutomation(ctx context.Context, req api.AgentRunAutomationRequestObject) (api.AgentRunAutomationResponseObject, error) {
	response, err := h.RunAutomation(ctx, api.RunAutomationRequestObject{AutomationId: req.AutomationId})
	return agentRunAutomationResponse{response}, err
}

type agentRunAutomationResponse struct {
	api.RunAutomationResponseObject
}

func (r agentRunAutomationResponse) VisitAgentRunAutomationResponse(w http.ResponseWriter) error {
	return r.VisitRunAutomationResponse(w)
}

func (h *handler) AgentListAutomationRuns(ctx context.Context, req api.AgentListAutomationRunsRequestObject) (api.AgentListAutomationRunsResponseObject, error) {
	response, err := h.ListAutomationRuns(ctx, api.ListAutomationRunsRequestObject{AutomationId: req.AutomationId})
	return agentListAutomationRunsResponse{response}, err
}

type agentListAutomationRunsResponse struct {
	api.ListAutomationRunsResponseObject
}

func (r agentListAutomationRunsResponse) VisitAgentListAutomationRunsResponse(w http.ResponseWriter) error {
	return r.VisitListAutomationRunsResponse(w)
}

func (h *handler) ListAgents(ctx context.Context, _ api.ListAgentsRequestObject) (api.ListAgentsResponseObject, error) {
	agents, err := h.Settings.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	return api.ListAgents200JSONResponse{Agents: agents}, nil
}

func (h *handler) CreateAgent(ctx context.Context, req api.CreateAgentRequestObject) (api.CreateAgentResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	agent, err := h.Settings.CreateAgent(ctx, *req.Body)
	if err != nil {
		return nil, err
	}
	return api.CreateAgent201JSONResponse(agent), nil
}

func (h *handler) GetAgent(ctx context.Context, req api.GetAgentRequestObject) (api.GetAgentResponseObject, error) {
	agent, err := h.Settings.Agent(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	return api.GetAgent200JSONResponse(agent), nil
}

func (h *handler) UpdateAgent(ctx context.Context, req api.UpdateAgentRequestObject) (api.UpdateAgentResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	if err := h.Settings.SaveAgent(ctx, req.AgentId, *req.Body); err != nil {
		return nil, err
	}
	agent, err := h.Settings.Agent(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	return api.UpdateAgent200JSONResponse(agent), nil
}
