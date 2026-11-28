package tools

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/recally-io/dex/internal/background"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/runner"
	"github.com/recally-io/dex/internal/session"
	tasksvc "github.com/recally-io/dex/internal/task"
)

type DispatchArgs struct {
	ID        string `json:"id"`
	Harness   string `json:"harness"`
	Workspace string `json:"workspace"`
	Command   string `json:"command"`
	Server    string `json:"server"`
	Resume    string `json:"resume,omitempty"`
	SessionID string `json:"externalSessionId,omitempty"`
}

type DispatchResult struct {
	ExecutionID string `json:"executionId"`
	ProcessID   string `json:"processId"`
}

func (s *Service) DispatchExternal(ctx context.Context, c engine.TaskContext, a DispatchArgs, rt engine.ExtensionRuntime, start func(context.Context, engine.TaskContext, session.ProcessStartArgs) (session.ProcessInfo, error)) (DispatchResult, error) {
	if c.Key == "" || !strings.HasPrefix(c.Key, c.BotID+"/") {
		return DispatchResult{}, invalid("a durable tool key is required")
	}
	u, err := url.Parse(a.Server)
	if err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !runner.IsAbsPath(a.Workspace) || strings.TrimSpace(a.Command) == "" || a.ID == "" {
		return DispatchResult{}, invalid("provide a dex server URL, an absolute workspace and a command")
	}
	conv, source, err := caller(ctx, s.q, c)
	if err != nil {
		return DispatchResult{}, err
	}
	settings, err := rt.Environment(ctx, c.ConversationID)
	if err != nil {
		return DispatchResult{}, err
	}
	runnerID := runner.EnrolledRunnerID(settings.Handle)
	if runnerID == "" {
		return DispatchResult{}, invalid("select an Outbound Runner for this Session")
	}
	issue, err := s.tasks.DispatchExternalExecution(ctx, tasksvc.BotInConversation(conv), s.taskOrigin(source), a.ID, a.Resume, tasksvc.ExternalParams{
		Harness: a.Harness, RunnerID: runnerID, WorkspacePath: a.Workspace, ExternalSessionID: a.SessionID, DispatchID: c.Key,
	})
	if err != nil {
		return DispatchResult{}, err
	}
	e := issue.Execution
	// Do not persist harness output. The command can print environment variables or traces.
	cmd := `/bin/bash -c '` + strings.ReplaceAll(a.Command, `'`, `'\''`) + `' >/dev/null 2>&1`
	p, err := start(runner.RequireEnrollment(ctx, e.RunnerEnrollment), c, session.ProcessStartArgs{
		Command: cmd, Cwd: a.Workspace, Label: "External Task run", Wake: background.WakeNone,
		RunnerID: runnerID, EnvKind: runner.KindRunner, Workspace: settings.Handle.Workspace(), InheritEnv: settings.Inherit, BaseEnv: settings.Vars,
		Env:        map[string]string{"DEX_TASK_TOKEN": issue.TaskToken, "DEX_TASK_SERVER": a.Server, "DEX_TOKEN": "", "DEX_TASK_EXTERNAL_SESSION_ID": e.ExternalSessionID},
		MaskOutput: func(output string) string { return strings.ReplaceAll(output, issue.TaskToken, "[redacted]") },
	})
	if err != nil {
		_, finishErr := s.tasks.ExternalFinish(context.WithoutCancel(ctx), issue.TaskToken, "failed", "Could not start the Process")
		// Executor errors may include command or environment data.
		return DispatchResult{ExecutionID: e.ID}, errors.Join(fmt.Errorf("could not start Process for external run %s", e.ID), finishErr)
	}
	return DispatchResult{ExecutionID: e.ID, ProcessID: p.ID}, nil
}
