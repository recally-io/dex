package worker

import "encoding/json/jsontext"

// Wire types of the worker protocol: one JSON object per line on the worker's stdin and stdout.
// Keep them in sync with worker/src/protocol.ts.

// ModelRef names a pi-ai model.
type ModelRef struct {
	Provider string `json:"provider"`
	ModelID  string `json:"modelId"`
}

// CreateConversationParams are the params of the createConversation method.
type CreateConversationParams struct {
	AgentID       string   `json:"agentId"`
	Model         ModelRef `json:"model"`
	ThinkingLevel string   `json:"thinkingLevel"`
	Instructions  string   `json:"instructions,omitempty"`
}

// CreateConversationResult is the result of the createConversation method.
type CreateConversationResult struct {
	ConversationID int64 `json:"conversationId"`
}

// SubmitParams are the params of the submit method. Model, ThinkingLevel and Instructions are the
// chat's current settings; the worker applies them to the conversation when they changed.
type SubmitParams struct {
	Kind           string   `json:"kind,omitempty"`
	WhenBusy       string   `json:"whenBusy,omitempty"`
	TaskStageID    string   `json:"taskStageId,omitempty"`
	AgentID        string   `json:"agentId"`
	ConversationID int64    `json:"conversationId"`
	Model          ModelRef `json:"model"`
	// ThinkingLevel is a pi-ai ModelThinkingLevel, such as "off" or "high".
	ThinkingLevel string `json:"thinkingLevel"`
	Instructions  string `json:"instructions"`
	RequestID     string `json:"requestId"`
	// Content is the user's text. The worker adds the paths of the attachments for the model.
	Content     string       `json:"content"`
	Attachments []Attachment `json:"attachments"`
	// Source is where memory entries from this conversation come from, such as dex:default/chat/12.
	Source string `json:"source"`
}

// Attachment is a file that the user attached to a message. Path is relative to the agent
// workspace: uploads/<ID>/<Name>.
type Attachment struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	MimeType string `json:"mimeType"`
	Path     string `json:"path"`
}

// SubmitResult is the result of the submit method.
type SubmitResult struct {
	SubmissionID int64 `json:"submissionId"`
}

// Settled is the event the worker sends when a submission has its final answer or has failed.
type Settled struct {
	AgentID        string `json:"agentId"`
	ConversationID int64  `json:"conversationId"`
	RequestID      string `json:"requestId"`
	Status         string `json:"status"` // "done" or "unanswered"
	Answer         string `json:"answer,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

// WatchParams are the params of the watch method. The worker sends the chat events of the
// conversation with WatchID; the first is a snapshot, sent before the result.
type WatchParams struct {
	WatchID        string `json:"watchId"`
	AgentID        string `json:"agentId"`
	ConversationID int64  `json:"conversationId"`
}

// UnwatchParams are the params of the unwatch method.
type UnwatchParams struct {
	WatchID string `json:"watchId"`
}

// AbortParams are the params of the abort method, which stops the conversation's run and
// withdraws its queued messages.
type AbortParams struct {
	AgentID        string `json:"agentId"`
	ConversationID int64  `json:"conversationId"`
}

// RecoverTasksParams registers old task conversations before a Harness can resume.
type RecoverTasksParams struct {
	Stages []RecoverTaskStage `json:"stages"`
}

type RecoverTaskStage struct {
	StageID        string `json:"stageId"`
	AgentID        string `json:"agentId"`
	ConversationID int64  `json:"conversationId"`
}

// ChatStream is the chat event of a watch: Events holds the ChatEvent objects of api/openapi.yaml
// from one commit, or End is true when the watch stopped.
// Completed spawn tools carry taskId. Control adds task IDs and titles to result marks.
type ChatStream struct {
	WatchID string           `json:"watchId"`
	Events  []jsontext.Value `json:"events"`
	End     bool             `json:"end"`
}

// ConfigureParams are the params of the configure method. They replace all providers.
type ConfigureParams struct {
	Providers []ProviderSettings `json:"providers"`
}

// ProviderSettings is one provider from the settings. API is "" for a pi-ai built-in provider,
// which uses only APIKey. Otherwise it names the API of a custom provider at BaseURL; Models lists
// its model IDs, or is empty to list them from the endpoint.
type ProviderSettings struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	API     string   `json:"api"`
	BaseURL string   `json:"baseUrl"`
	APIKey  string   `json:"apiKey"`
	Models  []string `json:"models"`
}

// ConfigureResult is the result of the configure method: provider ID → why its model list failed.
type ConfigureResult struct {
	Errors map[string]string `json:"errors"`
}

// ProviderInfo is one built-in provider in the result of the listProviders method.
type ProviderInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// "settings", another credential source, or empty when the provider has no credentials.
	Source string `json:"source,omitempty"`
}

// ListProvidersResult is the result of the listProviders method.
type ListProvidersResult struct {
	Providers []ProviderInfo `json:"providers"`
}

// ModelInfo is one model in the result of the listModels method. Reasoning and Input are pi-ai's
// model fields; ThinkingLevels is pi-ai's getSupportedThinkingLevels of the model.
type ModelInfo struct {
	Provider       string   `json:"provider"`
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Reasoning      bool     `json:"reasoning"`
	Input          []string `json:"input"`
	ThinkingLevels []string `json:"thinkingLevels"`
}

// ListModelsResult is the result of the listModels method: the models of the providers that have
// credentials.
type ListModelsResult struct {
	Models []ModelInfo `json:"models"`
}

type request struct {
	ID     int64  `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params"`
}

// TaskRequest identifies a durable tool call and its initiating submission.
type TaskRequest struct {
	AgentID         string         `json:"agentId"`
	ConversationID  int64          `json:"conversationId"`
	SourceRequestID string         `json:"sourceRequestId"`
	Key             string         `json:"key"`
	Action          string         `json:"action"`
	Args            jsontext.Value `json:"args"`
}

// incoming is a response (with ID) or an event (with Event).
type incoming struct {
	ID     int64          `json:"id"`
	Method string         `json:"method"`
	Result jsontext.Value `json:"result"`
	Error  *struct {
		Message string `json:"message"`
		Code    string `json:"code,omitempty"`
	} `json:"error"`
	Event  string         `json:"event"`
	Params jsontext.Value `json:"params"`
}

// QuiesceParams closes worker admission before checking all unfinished work.
type QuiesceParams struct {
	ExpiresAt int64 `json:"expiresAt"`
}
type QuiesceResult struct {
	Busy   bool   `json:"busy"`
	Reason string `json:"reason,omitempty"`
}

// AgentParams are the params of the extensions method.
type AgentParams struct {
	AgentID string `json:"agentId"`
	// Open opens the agent first, which connects its MCP servers.
	Open      bool `json:"open,omitzero"`
	Preflight bool `json:"preflight,omitzero"`
}

// ResetParams writes one reset through the conversation's admission chain.
type ResetParams struct {
	AgentID        string `json:"agentId"`
	ConversationID int64  `json:"conversationId"`
	RequestID      string `json:"requestId"`
}

// HistoryParams reads entries before Before. Limit extends to a display boundary.
type HistoryParams struct {
	AgentID        string `json:"agentId"`
	ConversationID int64  `json:"conversationId"`
	Before         int64  `json:"before"`
	Limit          int    `json:"limit"`
}

type HistoryResult struct {
	Items      []jsontext.Value `json:"items"`
	NextBefore *int64           `json:"nextBefore,omitempty"`
}

// InputEntriesParams finds placed inputs so control does not restore old failures after a head.
type InputEntriesParams struct {
	AgentID        string   `json:"agentId"`
	ConversationID int64    `json:"conversationId"`
	RequestIDs     []string `json:"requestIds"`
}

// EditEnvParams changes one key. An absent Value removes it.
type EditEnvParams struct {
	AgentID string  `json:"agentId"`
	Scope   string  `json:"scope"`
	Key     string  `json:"key"`
	Value   *string `json:"value,omitempty"`
}
