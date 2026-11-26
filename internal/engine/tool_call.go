package engine

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

// TaskContext binds a tool invocation to its persisted initiating input.
type TaskContext struct {
	AgentID         string
	ConversationID  int64
	SourceRequestID string
	RunInputIDs     []string
	Key             string
}

// RunnerInfo is an execution environment that a session can select. It has no secrets.
type RunnerInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"desc,omitempty"`
	Kind        string `json:"kind"`
	// Default marks the environment of the bot. A session without a selection runs in it.
	Default bool `json:"default"`
	// Status is "available", "offline", or "unavailable: <reason>".
	Status string `json:"status"`
}

// ToolCall provides the execution context of one durable tool invocation.
// Source lookup is explicit so each tool can keep its argument validation order.
type ToolCall struct {
	api     durable.ToolExecutionApi
	agentID string
}

func BindTool(agentID string, execute func(context.Context, any, ToolCall) (durable.ToolExecutionResult, error)) func(context.Context, any, durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
	return func(ctx context.Context, raw any, api durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
		return execute(ctx, raw, ToolCall{api: api, agentID: agentID})
	}
}

func (c ToolCall) ConversationID() int64                      { return int64(c.api.ConversationId()) }
func (c ToolCall) Key() string                                { return fmt.Sprintf("%s/%d", c.agentID, c.api.TaskId()) }
func (c ToolCall) Source(ctx context.Context) (string, error) { return taskSource(ctx, c.api) }
func (c ToolCall) Context(ctx context.Context) (TaskContext, error) {
	return toolCall(ctx, c.api, c.agentID)
}
func (c ToolCall) TurnEnding(ctx context.Context, name string) (TaskContext, error) {
	return turnEndingCall(ctx, c.api, c.agentID, name)
}

func (c ToolCall) SetCwd(ctx context.Context, path string) error {
	change := harness.AgentChange{Cwd: harness.Cleared[string]()}
	if path != "" {
		change.Cwd = harness.SetTo(path)
	}
	_, err := c.api.Commit(ctx, func(tx durable.Tx) (any, error) {
		return nil, harness.Configure(tx, c.api.ConversationId(), change)
	})
	return err
}

// JSONTool binds a typed operation to its source input and encodes the result.
func JSONTool[A, R any](agentID, name, description string, schema map[string]any, present bool, invoke func(context.Context, TaskContext, A) (R, error)) *durable.ToolRegistration {
	return &durable.ToolRegistration{
		Name: name, Description: description, Parameters: schema, Replay: durable.ReplaySafe,
		Execute: func(ctx context.Context, raw any, api durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
			if !present {
				return durable.ToolExecutionResult{}, errors.New("session service is unavailable")
			}
			args, err := durable.FromJsonValue[A](raw)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			requestID, err := taskSource(ctx, api)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			source := TaskContext{AgentID: agentID, ConversationID: int64(api.ConversationId()), SourceRequestID: requestID, Key: fmt.Sprintf("%s/%d", agentID, api.TaskId())}
			result, err := invoke(ctx, source, args)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			data, err := json.Marshal(result)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			return durable.ToolExecutionResult{Output: []ai.ToolResultMessageContent{ai.TextContent{Text: string(data)}}}, nil
		},
	}
}

func ObjectSchema(required []string, properties map[string]any) map[string]any {
	// A nil slice encodes as null, and providers such as OpenAI reject "required": null.
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": properties}
}

// turnEndingCall returns the call context of a tool that ends the turn with Terminate. Native
// Terminate ends the turn only when every call of the round asks for it. The round's tool slots are
// saved before any tool starts, so a mixed round is visible here.
func turnEndingCall(ctx context.Context, api durable.ToolExecutionApi, agentID, tool string) (TaskContext, error) {
	live, err := durable.Snapshot[harness.LiveState](ctx, api, harness.LiveDoc, api.ConversationId())
	if err != nil {
		return TaskContext{}, err
	}
	if live == nil || len(live.Tools) != 1 {
		return TaskContext{}, fmt.Errorf("%s must be the only tool call", tool)
	}
	return toolCall(ctx, api, agentID)
}

// toolCall names the agent, the conversation, the source input, live run inputs, and the tool operation key of a call.
func toolCall(ctx context.Context, api durable.ToolExecutionApi, agentID string) (TaskContext, error) {
	requestID, err := taskSource(ctx, api)
	if err != nil {
		return TaskContext{}, err
	}
	inputIDs, err := runInputRequestIDs(ctx, api)
	if err != nil {
		return TaskContext{}, err
	}
	if len(inputIDs) == 0 {
		inputIDs = []string{requestID}
	}
	return TaskContext{AgentID: agentID, ConversationID: int64(api.ConversationId()), SourceRequestID: requestID, RunInputIDs: inputIDs, Key: fmt.Sprintf("%s/%d", agentID, api.TaskId())}, nil
}

func runInputRequestIDs(ctx context.Context, api durable.ToolExecutionApi) ([]string, error) {
	val, err := api.Commit(ctx, func(tx durable.Tx) (any, error) {
		liveDoc, err := tx.Doc(harness.LiveDoc, api.ConversationId())
		if err != nil {
			return nil, err
		}
		live, err := durable.FromJsonValue[harness.LiveState](liveDoc.Snapshot())
		if err != nil || live.Run == nil || len(live.Run.Inputs) == 0 {
			return []string(nil), err
		}
		need := make(map[durable.SubmissionId]struct{}, len(live.Run.Inputs))
		for _, subID := range live.Run.Inputs {
			need[subID] = struct{}{}
		}
		found := make(map[durable.SubmissionId]string, len(need))
		var cursor durable.Cursor
		for len(found) < len(need) {
			page, err := tx.ScanEntries(durable.EntryQuery{
				ConversationId: api.ConversationId(),
				Order:          new(durable.ScanDescending),
			}, 100, cursor)
			if err != nil {
				return nil, err
			}
			for _, entry := range page.Items {
				if !isDexInputEntry(entry.Kind) {
					continue
				}
				data, err := durable.FromJsonValue[struct {
					RequestID string `json:"requestId"`
				}](entry.Data)
				if err != nil {
					return nil, err
				}
				if data.RequestID == "" {
					continue
				}
				sub, err := tx.SubmissionByRequest(api.ConversationId(), data.RequestID)
				if err != nil {
					return nil, err
				}
				if sub != nil {
					if _, ok := need[sub.Id]; ok {
						found[sub.Id] = data.RequestID
						if len(found) == len(need) {
							break
						}
					}
				}
			}
			if page.Next == nil {
				break
			}
			cursor = *page.Next
		}
		ids := make([]string, 0, len(found))
		for _, subID := range live.Run.Inputs {
			if reqID, ok := found[subID]; ok {
				ids = append(ids, reqID)
			}
		}
		return ids, nil
	})
	if err != nil {
		return nil, err
	}
	ids, _ := val.([]string)
	return ids, nil
}

// taskSource returns the request ID of the input that started the run of this tool call. Steered
// inputs join the run after its first input, so the first input is the source.
func taskSource(ctx context.Context, api durable.ToolExecutionApi) (string, error) {
	memo, present, err := api.Memo(ctx, "dex.sourceRequestId")
	if err != nil {
		return "", err
	}
	if present {
		if id, ok := memo.(string); ok {
			return id, nil
		}
		return "", errors.New("task source memo is invalid")
	}
	value, err := api.Commit(ctx, func(tx durable.Tx) (any, error) {
		liveDoc, err := tx.Doc(harness.LiveDoc, api.ConversationId())
		if err != nil {
			return nil, err
		}
		live, err := durable.FromJsonValue[harness.LiveState](liveDoc.Snapshot())
		if err != nil {
			return nil, err
		}
		if live.Run == nil || len(live.Run.Inputs) == 0 {
			return nil, errors.New("task tool has no source run")
		}
		original := live.Run.Inputs[0]
		task, err := tx.Task(api.TaskId())
		if err != nil {
			return nil, err
		}
		for task != nil && task.Id != live.Run.TaskId && task.Owner != nil {
			task, err = tx.Task(*task.Owner)
			if err != nil {
				return nil, err
			}
		}
		if task == nil || task.Id != live.Run.TaskId {
			return nil, errors.New("task tool does not belong to the source run")
		}
		var cursor durable.Cursor
		for {
			page, err := tx.ScanEntries(durable.EntryQuery{ConversationId: api.ConversationId(), Order: new(durable.ScanDescending)}, 100, cursor)
			if err != nil {
				return nil, err
			}
			for _, entry := range page.Items {
				if !isDexInputEntry(entry.Kind) {
					continue
				}
				data, err := durable.FromJsonValue[struct {
					RequestID string `json:"requestId"`
				}](entry.Data)
				if err != nil {
					return nil, err
				}
				if data.RequestID == "" {
					continue
				}
				submission, err := tx.SubmissionByRequest(api.ConversationId(), data.RequestID)
				if err != nil {
					return nil, err
				}
				if submission != nil && submission.Id == original {
					return data.RequestID, nil
				}
			}
			if page.Next == nil {
				return nil, errors.New("source run has no dex input")
			}
			cursor = *page.Next
		}
	})
	if err != nil {
		return "", err
	}
	winner, err := api.MemoCandidate(ctx, "dex.sourceRequestId", value)
	if err != nil {
		return "", err
	}
	id, ok := winner.(string)
	if !ok {
		return "", errors.New("task source memo is invalid")
	}
	return id, nil
}
func CommandEnvSchema(description string) map[string]any {
	return map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": description}
}
