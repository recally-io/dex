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

type Input struct {
	RequestID, Content, Kind, Origin, Source string
	Attachments                              []Attachment
	Options
	Automation bool
	// Child marks an input to a conversation that has a parent. A child has no session tools and no
	// routine tools, and it gets the child rules.
	Child bool
	// Main input comes from a person who waits for each reply.
	Main              bool
	TaskID, TaskTitle string
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
type Outcome struct {
	Status, Answer, Reason string
	// Covered is true when an earlier input of the same run started it, so the outcome of that
	// input speaks for this one. It is set only for inputs of child sessions.
	Covered bool
}
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
	Bots      BotTools
}
