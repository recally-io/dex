package engine

import (
	"context"
	"errors"

	"github.com/MichaelKinsy/PiG/durable"
)

// MonitorSetArgs is the input of the monitor_set tool.
type MonitorSetArgs struct {
	At              string `json:"at,omitempty"`
	DelaySeconds    int    `json:"delaySeconds,omitempty"`
	TimeZone        string `json:"timeZone,omitempty"`
	Prompt          string `json:"prompt"`
	CheckCommand    string `json:"checkCommand,omitempty"`
	IntervalSeconds int    `json:"intervalSeconds,omitempty"`
	MaxRuns         int    `json:"maxRuns,omitzero"`
	ExpiresAt       string `json:"expiresAt,omitempty"`
}

// MonitorInfo is one scheduled or completed follow-up monitor.
type MonitorInfo struct {
	ID         string `json:"id"`
	DueAt      int64  `json:"dueAt"`
	TimeZone   string `json:"timeZone"`
	Prompt     string `json:"prompt"`
	State      string `json:"state"`
	ChainDepth int    `json:"chainDepth"`
	CreatedAt  int64  `json:"createdAt"`
	Mode       string `json:"mode"`
	MaxRuns    int    `json:"maxRuns"`
	Runs       int    `json:"runs"`
	ExpiresAt  int64  `json:"expiresAt"`
	Outcome    string `json:"outcome"`
}

// MonitorTools connects bounded session follow-up monitors to the chat service.
type MonitorTools struct {
	Set    func(context.Context, TaskContext, MonitorSetArgs) (MonitorInfo, error)
	List   func(context.Context, TaskContext) ([]MonitorInfo, error)
	Cancel func(context.Context, TaskContext, string) (MonitorInfo, error)
}

func monitorExtension(agentID string, tools MonitorTools) *durable.Extension {
	return &durable.Extension{
		Name: "monitors",
		Tools: []*durable.ToolRegistration{
			sessionTool(agentID, "monitor_set", "Schedule a bounded Monitor in this conversation. With no command it queues the prompt when due. A check command uses this session’s Bash access; schedule it or repeat a prompt only when the user authorized it in the current turn. Script exit 0 compares output (first run sets the baseline); exit 10 always queues the final prompt and stops. Other exits, timeout, and oversized output fail.",
				objectSchema([]string{"prompt"}, map[string]any{
					"at":              map[string]any{"type": "string", "description": "Optional target timestamp (RFC3339, or YYYY-MM-DDTHH:MM:SS with timeZone). Mutually exclusive with delaySeconds."},
					"delaySeconds":    map[string]any{"type": "integer", "minimum": 60, "description": "Optional delay in seconds from now (minimum 60, maximum 604800). Mutually exclusive with at."},
					"timeZone":        map[string]any{"type": "string", "description": "Optional IANA time zone name (defaults to UTC)."},
					"checkCommand":    map[string]any{"type": "string", "description": "Optional Bash check. 20 second timeout; only the hash of up to 16 KiB combined output is saved."},
					"intervalSeconds": map[string]any{"type": "integer", "minimum": 60, "maximum": 604800, "description": "Interval between checks; required for multiple runs."},
					"maxRuns":         map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "Number of attempts, including unchanged checks. Defaults to one."},
					"expiresAt":       map[string]any{"type": "string", "description": "Optional deadline (same syntax as at) after the first check, within 7 days."},
					"prompt":          map[string]any{"type": "string", "minLength": 1, "description": "Instruction prompt delivered when the monitor fires."},
				}), tools.Set != nil, func(ctx context.Context, source TaskContext, args MonitorSetArgs) (MonitorInfo, error) {
					return tools.Set(ctx, source, args)
				}),
			sessionTool(agentID, "monitor_list", "List active and recent follow-up monitors for this conversation.",
				objectSchema([]string{}, map[string]any{}), tools.List != nil, func(ctx context.Context, source TaskContext, _ struct{}) (struct {
					Monitors []MonitorInfo `json:"monitors"`
				}, error) {
					list, err := tools.List(ctx, source)
					return struct {
						Monitors []MonitorInfo `json:"monitors"`
					}{Monitors: list}, err
				}),
			sessionTool(agentID, "monitor_cancel", "Cancel one scheduled follow-up monitor of this conversation.",
				objectSchema([]string{"id"}, map[string]any{
					"id": map[string]any{"type": "string", "minLength": 1, "description": "ID of the scheduled monitor to cancel."},
				}), tools.Cancel != nil, func(ctx context.Context, source TaskContext, args struct {
					ID string `json:"id"`
				}) (MonitorInfo, error) {
					if tools.Cancel == nil {
						return MonitorInfo{}, errors.New("monitor service is unavailable")
					}
					return tools.Cancel(ctx, source, args.ID)
				}),
		},
	}
}
