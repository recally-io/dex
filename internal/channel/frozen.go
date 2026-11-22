package channel

import "time"

// RichMessage is frozen Telegram rich HTML. Version 1 has no embedded media.
type RichMessage struct {
	Version int    `json:"version"`
	HTML    string `json:"html"`
}

// Part is one frozen platform payload. Text holds the payload for text-only
// platforms such as Feishu and WeChat. Rich, Group, and Buttons come from
// Telegram planning. Manager passes the stored variant back to the same Bot unchanged.
type Part struct {
	Rich    *RichMessage `json:"rich,omitempty"`
	Buttons []Button     `json:"buttons,omitempty"`
	Text    string       `json:"text,omitempty"`
	Literal bool         `json:"literal,omitempty"` // Deliver Text without a Markdown card.
	File    *File        `json:"file,omitempty"`
	Group   []File       `json:"group,omitempty"`
}

// File identifies a temporary outbox snapshot, not an artifact entry.
// The JSON fields describe the snapshot. Body is transient and never stored
// as JSON: Manager loads it from the outbox to plan GroupParts and for each
// Send call.
type File struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	MimeType string `json:"mimeType"`
	Size     int64  `json:"size"`
	Body     []byte `json:"-"`
}

// Receipt contains the message IDs confirmed by a platform call.
type Receipt struct {
	MessageIDs []string
	Kind       string
}

// RemoteMessage keeps the actual confirmed message kind, not its planned part index.
// Albums and rejected parts make remote and part indexes different.
type RemoteMessage struct {
	ID   string `json:"id"`
	Kind string `json:"kind,omitempty"`
}

// TextParts keeps text-only adapters on the shared part contract.
func TextParts(texts []string) []Part {
	parts := make([]Part, len(texts))
	for i, text := range texts {
		parts[i].Text = text
	}
	return parts
}

// RetryError keeps a failed part until the platform's flood-control window ends.
type RetryError struct {
	After time.Duration
	Err   error
}

func (e *RetryError) Error() string { return e.Err.Error() }
func (e *RetryError) Unwrap() error { return e.Err }

// Button is a Telegram action reference. The adapter signs it with its current bot token.
type Button struct {
	Text string `json:"text"`
	Data string `json:"data"`
}
