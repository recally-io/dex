package chat

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"log/slog"
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

func (s *Service) ToolMonitorCommand(ctx context.Context, r engine.TaskContext, a engine.MonitorCommandArgs) (engine.MonitorInfo, error) {
	if s.background == nil {
		return engine.MonitorInfo{}, errors.New("background commands are unavailable")
	}
	conv, err := s.toolCaller(ctx, r)
	if err != nil {
		return engine.MonitorInfo{}, err
	}
	if a.Env != nil {
		return engine.MonitorInfo{}, errors.New("env requires vault support")
	}
	s.monitorMu.Lock()
	defer s.monitorMu.Unlock()
	source, err := liveSource(ctx, s.q, r)
	if err != nil {
		return engine.MonitorInfo{}, err
	}
	if existing, err := s.q.GetBackgroundCommandByKey(ctx, r.Key); err == nil {
		return backgroundInfo(existing), nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return engine.MonitorInfo{}, err
	}
	if _, _, err := monitorChain(ctx, s.q, source); err != nil {
		return engine.MonitorInfo{}, err
	}
	if err := checkMonitorCapacity(ctx, s.q, conv.ID); err != nil {
		return engine.MonitorInfo{}, err
	}
	row, err := s.background.Start(ctx, background.StartParams{
		CallKey: r.Key, AgentID: conv.AgentID, ConversationID: conv.ID, SourceRequestID: r.SourceRequestID,
		Command: a.Command, Wake: a.Wake, Timeout: time.Duration(a.Timeout) * time.Second, Cwd: a.Cwd,
		DefaultCwd: cmp.Or(conv.Cwd, a.Workspace), Label: a.Label, EnvironmentID: a.EnvironmentID, Env: a.BaseEnv,
		InheritEnv: a.InheritEnv, EnvKind: a.EnvKind, OutputDir: a.OutputDir,
	})
	if err != nil {
		return engine.MonitorInfo{}, err
	}
	// A stop can run between the check above and the stored row: it lists no command, and the new
	// command would run on. The row exists now, so a stop that commits later lists it. If the stop
	// committed before, the source is no longer live: end the command at once.
	if _, err := liveSource(ctx, s.q, r); err != nil {
		if _, stopErr := s.background.Stop(context.WithoutCancel(ctx), conv.ID, row.ID); stopErr != nil {
			err = errors.Join(err, stopErr)
		}
		return engine.MonitorInfo{}, err
	}
	return backgroundInfo(row), nil
}

func (s *Service) cancelCommandMonitor(ctx context.Context, conversationID int64, id string) (engine.MonitorInfo, error) {
	if s.background == nil {
		return engine.MonitorInfo{}, ErrNotFound
	}
	// Older commands have a bare UUID in storage.
	if _, err := s.q.GetBackgroundCommand(ctx, id); errors.Is(err, sql.ErrNoRows) {
		id = strings.TrimPrefix(id, "command-")
	} else if err != nil {
		return engine.MonitorInfo{}, err
	}
	row, err := s.background.Stop(ctx, conversationID, id)
	if err != nil {
		return engine.MonitorInfo{}, err
	}
	return backgroundInfo(row), nil
}

func backgroundInfo(row sqlc.BackgroundCommand) engine.MonitorInfo {
	info := engine.MonitorInfo{
		ID: commandMonitorID(row.ID), Kind: "command", Label: row.Label, Command: row.Command, Wake: row.Wake, State: row.State, Reason: row.Reason,
		Cwd: row.Cwd, OutputFile: row.OutputPath, StartedAt: row.ProcessStartedAt,
	}
	if row.ExitStatus.Valid {
		info.ExitStatus = new(int(row.ExitStatus.Int64))
	}
	if row.EndedAt.Valid {
		info.EndedAt = &row.EndedAt.Int64
	}
	return info
}

func commandMonitorID(id string) string {
	if strings.HasPrefix(id, "command-") {
		return id
	}
	return "command-" + id
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
