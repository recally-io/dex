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
	// Source is where memory entries from this conversation come from, such as dex:chat/12.
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

// ChatStream is the chat event of a watch: Events holds the ChatEvent objects of api/openapi.yaml
// from one commit, or End is true when the watch stopped.
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

// incoming is a response (with ID) or an event (with Event).
type incoming struct {
	ID     int64          `json:"id"`
	Result jsontext.Value `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
	Event  string         `json:"event"`
	Params jsontext.Value `json:"params"`
}
