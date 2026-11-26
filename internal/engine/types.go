package engine

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/recally-io/dex/internal/runner"
)

var (
	ErrBusy                 = errors.New("conversation is busy")
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

type Input struct {
	// BeforeAdmission checks receipt state after environment preparation, before a new native commit.
	BeforeAdmission func(context.Context) error
	// ValidateAdmission checks the input before a new native commit.
	ValidateAdmission func(context.Context, Ref) error
	// RefreshContent replaces Content after the admission checks, inside the native transaction.
	// The native write can still fail. Replay of an admitted request does not call it.
	RefreshContent func(context.Context) (string, error)
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
	// ChildHasTask enables scoped Task tools on a linked child Session.
	ChildHasTask bool
	// Main input comes from a person who waits for each reply.
	Main              bool
	TaskID, TaskTitle string
	// TaskContext is model context. It is not part of the owner's message.
	TaskContext string
	// ModelSuffix adds guidance to the model input without changing the saved message.
	ModelSuffix string
	// WhenBusy says what admission does while the conversation runs: "followUp" (the default)
	// waits for the run to end, "steer" joins the run after its current tool step, and "reject"
	// returns ErrBusy.
	WhenBusy string
}
type inputMetadata struct {
	RequestID string `json:"requestId"`
	Content   string `json:"content"`
	// The field name is part of saved PiG transcripts.
	HasTask     bool         `json:"hasWorkTask,omitzero"`
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
	AnswerID               int64
	// RunInputIDs lists the request IDs of inputs answered by AnswerID, in PiG placement order.
	RunInputIDs []string
}

func (o Outcome) Equal(other Outcome) bool {
	return o.Status == other.Status &&
		o.Answer == other.Answer &&
		o.Reason == other.Reason &&
		o.AnswerID == other.AnswerID &&
		slices.Equal(o.RunInputIDs, other.RunInputIDs)
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
	ExecEnv runner.Provider
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
}
