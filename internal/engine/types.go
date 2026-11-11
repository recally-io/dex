package engine

import (
	"context"
	"errors"
)

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
type Activity struct {
	LastAction     string
	LastActivityAt *int64
	ToolStartedAt  *int64
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

// TrustedTaskSteer is only for the task control receipt path. The caller must
// guarantee that each native request ID is uniquely bound to the same task and
// content, including retries. Engine cannot recover withdrawn input content.
// The task binding remains valid across successor runs until sealed.
type TrustedTaskSteer struct {
	OriginalRequestID string
}

type Input struct {
	RequestID, Content, Kind, Origin, Source string
	Attachments                              []Attachment
	Options
	Automation bool
	Background bool
	// Steer admits a trusted task control receipt without changing configuration.
	Steer *TrustedTaskSteer
	// Main input comes from a person who waits for each reply.
	Main              bool
	TaskID, TaskTitle string
	// TaskContinuation is a user message in the conversation of a task that has ended.
	// It is a normal chat input: no task tools and no report_task duty.
	TaskContinuation bool
	// WhenBusy says what admission does while the conversation runs: "followUp" (the default)
	// waits for the run to end, "steer" joins the run after its current tool step, and "reject"
	// returns ErrBusy.
	WhenBusy string
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
	// BaseEnv is the tool environment when LoginEnv is nil or fails.
	BaseEnv map[string]string
	// LoginEnv captures the login shell environment for tools, started from BaseEnv; nil keeps
	// BaseEnv.
	LoginEnv  func(ctx context.Context, base map[string]string) (map[string]string, error)
	FakeModel bool
	Tasks     TaskTools
	Routines  RoutineTools
}
