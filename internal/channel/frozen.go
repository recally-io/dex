package channel

import "time"

// RichMessage is frozen Telegram rich HTML. Version 1 has no embedded media.
type RichMessage struct {
	Version int    `json:"version"`
	HTML    string `json:"html"`
}

// Part is one frozen platform payload. Text holds the payload for text-only
// platforms such as Feishu and WeChat. The sender passes a frozen payload to
// the same platform adapter on each attempt.
type Part struct {
	Rich    *RichMessage `json:"rich,omitempty"`
	Buttons []Button     `json:"buttons,omitempty"`
	Text    string       `json:"text,omitempty"`
	File    *File        `json:"file,omitempty"`
}

// File identifies a temporary outbox snapshot, not an artifact entry.
// The JSON fields describe the snapshot. Body is transient and never stored
// as JSON: Manager loads it from the outbox before sending.
type File struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	MimeType string `json:"mimeType"`
	Size     int64  `json:"size"`
	Body     []byte `json:"-"`
}

// Receipt describes one confirmed platform message. WeChat does not return a message ID.
type Receipt struct {
	MessageID string
	Kind      string
}

// RemoteMessage is one recorded platform receipt.
type RemoteMessage struct {
	ID   string `json:"id"`
	Kind string `json:"kind,omitempty"`
}

// RetryError reports the platform's retry deadline relative to the current time.
type RetryError struct {
	After time.Duration
	Err   error
}

func (e *RetryError) Error() string { return e.Err.Error() }
func (e *RetryError) Unwrap() error { return e.Err }

// Button is a Telegram action reference. The adapter signs it with the current bot token.
type Button struct {
	Text string `json:"text"`
	Data string `json:"data"`
}
