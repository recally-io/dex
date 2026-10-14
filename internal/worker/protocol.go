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
	AgentID      string   `json:"agentId"`
	Model        ModelRef `json:"model"`
	Instructions string   `json:"instructions,omitempty"`
}

// CreateConversationResult is the result of the createConversation method.
type CreateConversationResult struct {
	ConversationID int64 `json:"conversationId"`
}

// SubmitParams are the params of the submit method.
type SubmitParams struct {
	AgentID        string `json:"agentId"`
	ConversationID int64  `json:"conversationId"`
	RequestID      string `json:"requestId"`
	Content        string `json:"content"`
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
