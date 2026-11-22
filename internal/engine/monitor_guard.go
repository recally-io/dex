package engine

import (
	"context"
	"errors"
	"strings"

	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

// monitorGuard protects a follow-up that waited in the native queue. A grant valid
// at admission is not permission to run later in a changed target or after revocation.
func monitorGuard(agentID string, store durable.Storage, authorize func(context.Context, TaskContext) error, abort func(context.Context, durable.TaskId) error) *durable.Extension {
	check := func(ctx context.Context, api harness.HookApi) error {
		live, err := durable.Snapshot(ctx, api, harness.LiveDoc, api.ConversationId())
		if err != nil || live == nil || live.Run == nil {
			return err
		}
		for _, id := range live.Run.Inputs {
			sub, err := store.Submission(ctx, id)
			if err != nil {
				return err
			}
			if sub == nil || sub.RequestId == nil {
				continue
			}
			request := *sub.RequestId
			if !IsMonitorEvent(request) {
				continue
			}
			if err := authorize(ctx, TaskContext{AgentID: agentID, ConversationID: int64(api.ConversationId()), SourceRequestID: request}); err != nil {
				return err
			}
		}
		return nil
	}
	return &durable.Extension{Name: "monitor-guard", Hooks: []durable.HookRegistration{
		harness.Hook(harness.GenerationTask, &harness.GenerationHooks{BeforeRequest: func(ctx context.Context, _ harness.GenerationRequest, api harness.HookApi) (*harness.GenerationRequest, error) {
			if err := check(ctx, api); err != nil {
				// A hook error alone would report and continue in PiG.
				return nil, errors.Join(err, abort(ctx, api.TaskId()))
			}
			return nil, nil
		}}),
		harness.Hook(harness.ToolTask, &harness.ToolHooks{BeforeTool: func(ctx context.Context, _ harness.ToolHookCall, api harness.HookApi) (*harness.BeforeToolResult, error) {
			if err := check(ctx, api); err != nil {
				return &harness.BeforeToolResult{Block: new("monitor target or authority is no longer valid")}, nil //nolint:nilerr // PiG blocks on a result, not a hook error.
			}
			return nil, nil
		}}),
	}}
}

// IsMonitorEvent recognizes current request IDs and frozen pre-upgrade receipts.
func IsMonitorEvent(request string) bool {
	return strings.HasPrefix(request, "monitor:") || strings.HasPrefix(request, "timer:")
}
