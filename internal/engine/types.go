package engine

import (
	"context"
	"errors"
	"time"

	"github.com/recally-io/dex/internal/execenv"
)

var (
	ErrBusy                 = errors.New("conversation is busy")
	ErrReviewBarrier        = errors.New("a channel review holds queued inputs")
	ErrEnvironmentResetting = errors.New("environment reset in progress")
	ErrOriginBusy           = errors.New("follow-up queue holds another origin")
	ErrMaintenance          = errors.New("engine admission is paused")
	ErrClosed               = errors.New("engine is closed")
	ErrInvalidInput         = errors.New("invalid engine input")
	ErrNotFound             = errors.New("conversation not found")
	ErrItemNotFound         = errors.New("transcript item not found")
	ErrNoModel              = errors.New("the bot has no model; choose one in Settings")
)

type Ref struct {
	AgentID string
	ID      int64
}
type RunningTool struct {
	Name            string `json:"name"`
	FirstObservedAt int64  `json:"firstObservedAt"`
}

type Activity struct {
	LastActivityAt *int64
	ToolStartedAt  *int64
	RunningTools   []RunningTool
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

// InputIsolation keeps an input out of PiG's shared Inbox boundaries.
type InputIsolation string

const (
	IsolatedDelegation InputIsolation = "delegation"
	IsolatedReview     InputIsolation = "review"
)

type Input struct {
	// BeforeAdmission checks receipt state after environment preparation, before a new native commit.
	BeforeAdmission func(context.Context) error
	// ValidateAdmission runs after isolation checks, just before a new native commit.
	ValidateAdmission func(context.Context, Ref) error
	// RequireEnvironment delays admission until the selected environment is online.
	RequireEnvironment bool
	// Changed wakes content-free execution subscribers. It must not block.
	Changed                                  func()
	RequestID, Content, Kind, Origin, Source string
	Attachments                              []Attachment
	Options
	Automation bool
	// Child marks an input to a conversation that has a parent.
	Child bool
	// Main input comes from a person who waits for each reply.
	Main              bool
	TaskID, TaskTitle string
	// WorkTaskContext is model context. It is not part of the owner's message.
	WorkTaskContext string
	// ModelSuffix adds guidance to the model input without changing the saved message.
	ModelSuffix string
	// WhenBusy says what admission does while the conversation runs: "followUp" (the default)
	// waits for the run to end, "steer" joins the run after its current tool step, and "reject"
	// returns ErrBusy.
	WhenBusy  string
	Isolation InputIsolation
	// ReviewBypass permits an immediate input past a waiting review, only while native work is active.
	ReviewBypass bool
}
type inputMetadata struct {
	RequestID    string         `json:"requestId"`
	Content      string         `json:"content"`
	HasWorkTask  bool           `json:"hasWorkTask,omitzero"`
	Kind         string         `json:"kind,omitempty"`
	Origin       string         `json:"origin"`
	Source       string         `json:"source"`
	Attachments  []Attachment   `json:"attachments"`
	TaskID       string         `json:"taskId,omitempty"`
	TaskTitle    string         `json:"taskTitle,omitempty"`
	Reason       string         `json:"reason,omitempty"`
	Isolation    InputIsolation `json:"isolation,omitempty"`
	ReviewBypass bool           `json:"reviewBypass,omitzero"`
}
type Outcome struct {
	Status, Answer, Reason string
	AnswerID               int64
	// ActivationID is the native first entry of an isolated run, not a submission or task ID.
	ActivationID int64
}
type Config struct {
	MCPServers    *MCPStore
	DataDir, Home string
	// BaseEnv is the tool environment when LoginEnv is nil or fails.
	BaseEnv map[string]string
	// LoginEnv captures the login shell environment for tools, started from BaseEnv; nil keeps
	// BaseEnv.
	LoginEnv func(ctx context.Context, base map[string]string) (map[string]string, error)
	// ExecEnv configures agent workspace and execution backends. Nil defaults to a host-local provider.
	ExecEnv execenv.Provider
	// SessionEnvironment returns the explicit execution environment of a session, or
	// empty when the session follows its agent. Nil makes every session follow its agent.
	SessionEnvironment func(ctx context.Context, ref Ref) (string, error)
	// FallbackModel returns the current fallback model for an agent, or an empty ModelRef.
	FallbackModel func(ctx context.Context, agentID string) ModelRef
	FakeModel     bool
	// FakeInputSuffix is model guidance that the faux command fixtures omit from arguments.
	FakeInputSuffix string
	BashTimeout     time.Duration
	// Extensions supplies application extensions at agent open and reload.
	Extensions func(ExtensionRuntime) ExtensionSet
	// InputExtensions selects application extensions before each fresh admission.
	InputExtensions func(Input) ExtensionEdit
	// HideToolArguments omits sensitive arguments from history and live events.
	HideToolArguments func(string) bool
	// HideToolFromSearch excludes compatibility tools from the active tool catalog.
	HideToolFromSearch func(string) bool
}
