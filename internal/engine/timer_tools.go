package engine

import (
	"context"
	"errors"

	"github.com/MichaelKinsy/PiG/durable"
)

// TimerSetArgs is the input of the timer_set tool.
type TimerSetArgs struct {
	At           string `json:"at,omitempty"`
	DelaySeconds int    `json:"delaySeconds,omitempty"`
	TimeZone     string `json:"timeZone,omitempty"`
	Prompt       string `json:"prompt"`
}

// TimerInfo is one scheduled or completed follow-up timer.
type TimerInfo struct {
	ID         string `json:"id"`
	DueAt      int64  `json:"dueAt"`
	TimeZone   string `json:"timeZone"`
	Prompt     string `json:"prompt"`
	State      string `json:"state"`
	ChainDepth int    `json:"chainDepth"`
	CreatedAt  int64  `json:"createdAt"`
}

// TimerTools connects bounded session follow-up timers to the chat service.
type TimerTools struct {
	Set    func(context.Context, TaskContext, TimerSetArgs) (TimerInfo, error)
	List   func(context.Context, TaskContext) ([]TimerInfo, error)
	Cancel func(context.Context, TaskContext, string) (TimerInfo, error)
}

func timerExtension(agentID string, tools TimerTools) *durable.Extension {
	return &durable.Extension{
		Name: "timers",
		Tools: []*durable.ToolRegistration{
			sessionTool(agentID, "timer_set", "Schedule a bounded one-shot follow-up timer for this conversation (minimum 60 seconds, maximum 7 days, max chain depth 6). When due, dex wakes this conversation as a System Turn with the given prompt.",
				objectSchema([]string{"prompt"}, map[string]any{
					"at":           map[string]any{"type": "string", "description": "Optional target timestamp (RFC3339, or YYYY-MM-DDTHH:MM:SS with timeZone). Mutually exclusive with delaySeconds."},
					"delaySeconds": map[string]any{"type": "integer", "minimum": 60, "description": "Optional delay in seconds from now (minimum 60, maximum 604800). Mutually exclusive with at."},
					"timeZone":     map[string]any{"type": "string", "description": "Optional IANA time zone name (defaults to UTC)."},
					"prompt":       map[string]any{"type": "string", "minLength": 1, "description": "Instruction prompt delivered when the timer fires."},
				}), tools.Set != nil, func(ctx context.Context, source TaskContext, args TimerSetArgs) (TimerInfo, error) {
					return tools.Set(ctx, source, args)
				}),
			sessionTool(agentID, "timer_list", "List active and recent follow-up timers for this conversation.",
				objectSchema([]string{}, map[string]any{}), tools.List != nil, func(ctx context.Context, source TaskContext, _ struct{}) (struct {
					Timers []TimerInfo `json:"timers"`
				}, error) {
					list, err := tools.List(ctx, source)
					return struct {
						Timers []TimerInfo `json:"timers"`
					}{Timers: list}, err
				}),
			sessionTool(agentID, "timer_cancel", "Cancel one scheduled follow-up timer of this conversation.",
				objectSchema([]string{"id"}, map[string]any{
					"id": map[string]any{"type": "string", "minLength": 1, "description": "ID of the scheduled timer to cancel."},
				}), tools.Cancel != nil, func(ctx context.Context, source TaskContext, args struct {
					ID string `json:"id"`
				}) (TimerInfo, error) {
					if tools.Cancel == nil {
						return TimerInfo{}, errors.New("timer service is unavailable")
					}
					return tools.Cancel(ctx, source, args.ID)
				}),
		},
	}
}
