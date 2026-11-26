package chat

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"maps"
	"strings"
	"time"

	"github.com/recally-io/dex/internal/background"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

// ConfigureBackground sets the manager that runs the background commands of sessions. Call it
// before the service runs.
func (s *Service) ConfigureBackground(m *background.Manager) { s.background = m }

// WakeConversation admits the inputs that a background command stored for a session.
func (s *Service) WakeConversation(chatID int64) {
	s.signal(chatID)
	s.Wake()
}

func (s *Service) ToolProcessStart(ctx context.Context, r engine.TaskContext, a ProcessStartArgs) (ProcessInfo, error) {
	if s.background == nil {
		return ProcessInfo{}, errors.New("background commands are unavailable")
	}
	conv, err := s.toolCaller(ctx, r)
	if err != nil {
		return ProcessInfo{}, err
	}
	s.monitorMu.Lock()
	defer s.monitorMu.Unlock()
	source, err := liveSource(ctx, s.q, r)
	if err != nil {
		return ProcessInfo{}, err
	}
	if existing, err := s.q.GetBackgroundCommandByKey(ctx, r.Key); err == nil {
		return processInfo(existing), nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ProcessInfo{}, err
	}
	if _, _, err := monitorChain(ctx, s.q, source); err != nil {
		return ProcessInfo{}, err
	}
	if err := checkMonitorCapacity(ctx, s.q, conv.ID); err != nil {
		return ProcessInfo{}, err
	}
	env := make(map[string]string, len(a.BaseEnv)+len(a.Env))
	maps.Copy(env, a.BaseEnv)
	maps.Copy(env, a.Env)
	row, err := s.background.Start(ctx, background.StartParams{
		CallKey: r.Key, AgentID: conv.AgentID, ConversationID: conv.ID, SourceRequestID: r.SourceRequestID,
		Command: a.Command, Wake: a.Wake, Timeout: time.Duration(a.Timeout) * time.Second, Cwd: a.Cwd,
		DefaultCwd: cmp.Or(conv.Cwd, a.Workspace), Label: a.Label, RunnerID: a.RunnerID, Env: env,
		InheritEnv: a.InheritEnv, EnvKind: a.EnvKind, OutputDir: a.OutputDir, MaskOutput: a.MaskOutput,
	})
	if err != nil {
		return ProcessInfo{}, err
	}
	// A stop can run between the check above and the stored row: it lists no command, and the new
	// command would run on. The row exists now, so a stop that commits later lists it. If the stop
	// committed before, the source is no longer live: end the command at once.
	if _, err := liveSource(ctx, s.q, r); err != nil {
		if _, stopErr := s.background.Stop(context.WithoutCancel(ctx), conv.ID, row.ID); stopErr != nil {
			err = errors.Join(err, stopErr)
		}
		return ProcessInfo{}, err
	}
	return processInfo(row), nil
}

func (s *Service) ToolProcessList(ctx context.Context, r engine.TaskContext) ([]ProcessInfo, error) {
	conv, err := s.toolCaller(ctx, r)
	if err != nil {
		return nil, err
	}
	out := []ProcessInfo{}
	if s.background == nil {
		return out, nil
	}
	rows, err := s.background.List(ctx, conv.ID)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out = append(out, processInfo(row))
	}
	return out, nil
}

func (s *Service) ToolProcessStop(ctx context.Context, r engine.TaskContext, id string) (ProcessInfo, error) {
	conv, err := s.toolCaller(ctx, r)
	if err != nil {
		return ProcessInfo{}, err
	}
	if s.background == nil {
		return ProcessInfo{}, ErrNotFound
	}
	row, err := s.background.Stop(ctx, conv.ID, strings.TrimPrefix(id, "command-"))
	if err != nil {
		return ProcessInfo{}, err
	}
	return processInfo(row), nil
}

func processInfo(row sqlc.BackgroundCommand) ProcessInfo {
	info := ProcessInfo{
		ID: "command-" + row.ID, Label: row.Label, Command: row.Command, Wake: row.Wake, State: row.State, Reason: row.Reason,
		Cwd: row.Cwd, OutputFile: row.OutputPath, StartedAt: row.ProcessStartedAt,
	}
	if row.ExitStatus.Valid {
		info.ExitStatus = new(int(row.ExitStatus.Int64))
	}
	if row.EndedAt.Valid {
		info.EndedAt = new(row.EndedAt.Int64)
	}
	return info
}

// stopBackground ends the background commands of the sessions that a stop ended. A command that
// ends on its own at the same moment can still wake its session once: the stop already withdrew
// the inputs that existed.
func (s *Service) stopBackground(ctx context.Context, conversations []int64) {
	if s.background == nil {
		return
	}
	for _, id := range conversations {
		if err := s.background.StopConversation(ctx, id); err != nil {
			slog.Error("stop background commands", "chat", id, "err", err)
		}
	}
}
