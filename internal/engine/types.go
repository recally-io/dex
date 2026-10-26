package engine

import "errors"

var (
	ErrBusy         = errors.New("conversation is busy")
	ErrMaintenance  = errors.New("engine admission is paused")
	ErrClosed       = errors.New("engine is closed")
	ErrInvalidInput = errors.New("invalid engine input")
	ErrNotFound     = errors.New("conversation not found")
	ErrNoModel      = errors.New("the bot has no model; choose one in Settings")
)

type Ref struct {
	AgentID string
	ID      int64
}
type ModelRef struct {
	Provider string `json:"provider"`
	ModelID  string `json:"modelId"`
}
type Options struct {
	Cwd           string
	Model         ModelRef
	ThinkingLevel string
	Instructions  string
}
type Attachment struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	MimeType string `json:"mimeType"`
	Path     string `json:"path"`
}
type Input struct {
	RequestID, Content, Kind, Origin, Source string
	Attachments                              []Attachment
	Options
	Automation        bool
	Background        bool
	TaskID, TaskTitle string
}
type InputMetadata struct {
	RequestID   string       `json:"requestId"`
	Content     string       `json:"content"`
	Kind        string       `json:"kind,omitempty"`
	Origin      string       `json:"origin"`
	Source      string       `json:"source"`
	Attachments []Attachment `json:"attachments"`
	TaskID      string       `json:"taskId,omitempty"`
	TaskTitle   string       `json:"taskTitle,omitempty"`
	Reason      string       `json:"reason,omitempty"`
}
type Outcome struct{ Status, Answer, Reason string }
type Config struct {
	DataDir, Home string
	BaseEnv       map[string]string
	FakeModel     bool
	Tasks         TaskTools
}
