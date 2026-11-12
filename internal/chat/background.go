package chat

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
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

func (s *Service) ToolBackgroundStart(ctx context.Context, r engine.TaskContext, a engine.BackgroundStartArgs) (engine.BackgroundInfo, error) {
	if s.background == nil {
		return engine.BackgroundInfo{}, errors.New("background commands are unavailable")
	}
	conv, err := s.toolCaller(ctx, r)
	if err != nil {
		return engine.BackgroundInfo{}, err
	}
	if _, err := liveSource(ctx, s.q, r); err != nil {
		return engine.BackgroundInfo{}, err
	}
	row, err := s.background.Start(ctx, background.StartParams{
		CallKey: r.Key, AgentID: conv.AgentID, ConversationID: conv.ID, SourceRequestID: r.SourceRequestID,
		Command: a.Command, Wake: a.Wake, Timeout: time.Duration(a.Timeout) * time.Second, Cwd: a.Cwd,
		DefaultCwd: cmp.Or(conv.Cwd, a.Workspace), Label: a.Label, Env: a.Env, OutputDir: a.OutputDir,
	})
	if err != nil {
		return engine.BackgroundInfo{}, err
	}
	// A stop can run between the check above and the stored row: it lists no command, and the new
	// command would run on. The row exists now, so a stop that commits later lists it. If the stop
	// committed before, the source is no longer live: end the command at once.
	if _, err := liveSource(ctx, s.q, r); err != nil {
		if _, stopErr := s.background.Stop(context.WithoutCancel(ctx), conv.ID, row.ID); stopErr != nil {
			err = errors.Join(err, stopErr)
		}
		return engine.BackgroundInfo{}, err
	}
	return backgroundInfo(row), nil
}

func (s *Service) ToolBackgroundList(ctx context.Context, r engine.TaskContext) ([]engine.BackgroundInfo, error) {
	if s.background == nil {
		return nil, errors.New("background commands are unavailable")
	}
	conv, err := s.toolCaller(ctx, r)
	if err != nil {
		return nil, err
	}
	rows, err := s.background.List(ctx, conv.ID)
	if err != nil {
		return nil, err
	}
	list := make([]engine.BackgroundInfo, 0, len(rows))
	for _, row := range rows {
		list = append(list, backgroundInfo(row))
	}
	return list, nil
}

func (s *Service) ToolBackgroundStop(ctx context.Context, r engine.TaskContext, id string) (engine.BackgroundInfo, error) {
	if s.background == nil {
		return engine.BackgroundInfo{}, errors.New("background commands are unavailable")
	}
	conv, err := s.toolCaller(ctx, r)
	if err != nil {
		return engine.BackgroundInfo{}, err
	}
	row, err := s.background.Stop(ctx, conv.ID, id)
	if err != nil {
		return engine.BackgroundInfo{}, fmt.Errorf("stop background command: %w", err)
	}
	return backgroundInfo(row), nil
}

func backgroundInfo(row sqlc.BackgroundCommand) engine.BackgroundInfo {
	info := engine.BackgroundInfo{
		ID: row.ID, Label: row.Label, Command: row.Command, Wake: row.Wake, State: row.State, Reason: row.Reason,
		Cwd: row.Cwd, OutputFile: row.OutputPath, StartedAt: row.ProcessStartedAt,
	}
	if row.ExitStatus.Valid {
		status := int(row.ExitStatus.Int64)
		info.ExitStatus = &status
	}
	if row.EndedAt.Valid {
		info.EndedAt = &row.EndedAt.Int64
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
