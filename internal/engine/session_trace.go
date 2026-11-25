package engine

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/MichaelKinsy/PiG/durable"
	durableenv "github.com/MichaelKinsy/PiG/durable/env"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

func sessionTraceExtension(agent string) *durable.Extension {
	namespace := "private"
	// Native generation hooks run again on retries and tool rounds. Keep a bounded
	// cache of the last observed input list per conversation; misses only repeat
	// a diagnostic and do not affect execution.
	type stamp struct {
		conversation durable.ConversationId
		last         durable.SubmissionId
		count        int
	}
	var seen [128]stamp
	var mu sync.Mutex
	return &durable.Extension{Name: "dex-session-trace", Hooks: []durable.HookRegistration{
		harness.Hook(harness.GenerationTask, &harness.GenerationHooks{
			BeforeRequest: func(ctx context.Context, _ harness.GenerationRequest, api harness.HookApi) (*harness.GenerationRequest, error) {
				// The hook runs in the generation task, not in the admission commit.
				// It precedes provider dispatch. It must not change execution on read failure.
				live, err := durable.Snapshot(ctx, api, harness.LiveDoc, api.ConversationId())
				if err == nil && live != nil && live.Run != nil && live.Run.TaskId == api.TaskId() && len(live.Run.Inputs) != 0 {
					inputs := live.Run.Inputs
					current := stamp{api.ConversationId(), inputs[len(inputs)-1], len(inputs)}
					slot := current.conversation & (durable.ConversationId(len(seen)) - 1)
					mu.Lock()
					alreadyLogged := seen[slot] == current
					seen[slot] = current
					mu.Unlock()
					if alreadyLogged {
						return nil, nil
					}
					slog.InfoContext(ctx, "session input", "phase", "generation_started", "agent", agent, "native_namespace", namespace,
						"native_conversation_id", api.ConversationId(), "native_task_id", api.TaskId(), "native_submission_ids", live.Run.Inputs)
				}
				return nil, nil
			},
		}),
	}}
}

// Trace a typed shell timeout before the tool converts it to display text.
// A timeout error describes the local result, not confirmed remote exit.
func (e *sessionPromptEnv) Exec(ctx context.Context, command any, opts *durableenv.ShellExecOptions) (durableenv.ShellExecResult, error) {
	started := time.Now()
	result, err := e.ExecutionEnv.Exec(ctx, command, opts)
	executionErr, typed := errors.AsType[*durableenv.ExecutionError](err)
	if errors.Is(err, context.DeadlineExceeded) || (typed && executionErr.Code == durableenv.ExecutionErrorTimeout) {
		slog.InfoContext(ctx, "session execution", "phase", "shell_timeout", "agent", e.agentID,
			"native_namespace", "private", "native_conversation_id", e.conversation, "elapsed_ms", time.Since(started).Milliseconds())
	}
	return result, err
}
