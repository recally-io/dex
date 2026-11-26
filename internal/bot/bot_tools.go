package bot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/session"
)

// Patch changes the fields that are not nil.
type Patch struct {
	Name, Instructions, Provider, ModelID, ThinkingLevel, FallbackProvider, FallbackModelID, RunnerID *string
}

func applyPatch(a *Bot, p Patch) {
	for _, f := range []struct {
		dst *string
		src *string
	}{
		{&a.Name, p.Name},
		{&a.Instructions, p.Instructions},
		{&a.Provider, p.Provider},
		{&a.ModelID, p.ModelID},
		{&a.ThinkingLevel, p.ThinkingLevel},
		{&a.FallbackProvider, p.FallbackProvider},
		{&a.FallbackModelID, p.FallbackModelID},
		{&a.RunnerID, p.RunnerID},
	} {
		if f.src != nil {
			*f.dst = *f.src
		}
	}
}

// patchBot merges a patch into a saved active bot and applies the checks of SaveBot. The
// read and the write share one lock, so two patches cannot lose each other's fields.
func (s *Service) patchBot(ctx context.Context, id string, p Patch) (Bot, error) {
	var (
		oldRow sqlc.Bot
		out    Bot
	)
	err := func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		var getErr error
		oldRow, getErr = s.q.GetBot(ctx, id)
		if errors.Is(getErr, sql.ErrNoRows) || (getErr == nil && oldRow.ArchivedAt.Valid) {
			return ErrNotFound
		}
		if getErr != nil {
			return getErr
		}
		a := botView(oldRow)
		applyPatch(&a, p)
		var cleanErr error
		a, cleanErr = s.cleanBot(ctx, a)
		if cleanErr != nil {
			return cleanErr
		}
		if err := s.saveSettings(ctx, oldRow, sqlc.UpdateBotSettingsParams{
			ModelProvider: a.Provider, ModelID: a.ModelID, ThinkingLevel: a.ThinkingLevel,
			FallbackProvider: a.FallbackProvider, FallbackModelID: a.FallbackModelID,
			Instructions: a.Instructions, RunnerID: a.RunnerID, ID: id, Name: a.Name,
		}); err != nil {
			return err
		}
		a.ArchivedAt = 0
		out = a
		return nil
	}()
	if err != nil {
		return Bot{}, err
	}
	if oldRow.RunnerID != out.RunnerID && s.OnRunnerChanged != nil {
		s.OnRunnerChanged(ctx, id, oldRow.RunnerID, oldRow.RunnerSandboxID)
	}
	return out, nil
}

// resolveBot finds an active bot by ID, then by name without regard to case. A name that two bots
// share is an error, so a tool never changes the wrong bot.
func (s *Service) resolveBot(ctx context.Context, ref string) (Bot, error) {
	ref = strings.TrimSpace(ref)
	bots, err := s.ListBots(ctx)
	if err != nil {
		return Bot{}, err
	}
	var named []Bot
	for _, a := range bots {
		if a.ID == ref {
			return a, nil
		}
		if strings.EqualFold(a.Name, ref) {
			named = append(named, a)
		}
	}
	switch len(named) {
	case 0:
		return Bot{}, fmt.Errorf("%w: no active bot has the ID or name %q", ErrNotFound, ref)
	case 1:
		return named[0], nil
	default:
		return Bot{}, fmt.Errorf("%w: %d bots are named %q; use the ID", ErrInvalid, len(named), ref)
	}
}

// Tools serves the bot tools. It owns the mapping from tool input to the same
// service calls that the web uses; session.Service checks the source of every call.
type Tools struct {
	Bots       *Service
	Sessions   *session.Service
	Extensions func(ctx context.Context, botID string) (engine.ExtensionInfo, error)
}

func botInfo(a Bot) Info {
	return Info{
		ID: a.ID, Name: a.Name, Provider: a.Provider, ModelID: a.ModelID, ThinkingLevel: a.ThinkingLevel,
		FallbackProvider: a.FallbackProvider, FallbackModelID: a.FallbackModelID,
		Instructions: a.Instructions, RunnerID: a.RunnerID,
	}
}

// Handlers returns the engine callbacks.
func (t *Tools) Handlers() Handlers {
	return Handlers{List: t.list, Get: t.get, Create: t.create, Update: t.update, Archive: t.archive}
}

func (t *Tools) list(ctx context.Context, c engine.TaskContext) ([]Info, error) {
	if err := t.Sessions.ToolBotCaller(ctx, c, false); err != nil {
		return nil, err
	}
	bots, err := t.Bots.ListBots(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := t.Bots.q.BotRunningSessions(ctx)
	if err != nil {
		return nil, err
	}
	running := make(map[string]int64, len(rows))
	for _, row := range rows {
		running[row.BotID] = row.Sessions
	}
	out := make([]Info, len(bots))
	for i, a := range bots {
		out[i] = botInfo(a)
		out[i].Instructions, _, _ = strings.Cut(strings.TrimSpace(a.Instructions), "\n")
		// Not Activity.ActiveSessions: that sidebar count leaves Main out, and bot_delete refuses
		// a bot that has any pending input.
		n := running[a.ID]
		out[i].RunningSessions = &n
	}
	return out, nil
}

func (t *Tools) get(ctx context.Context, c engine.TaskContext, ref string) (Info, error) {
	if err := t.Sessions.ToolBotCaller(ctx, c, false); err != nil {
		return Info{}, err
	}
	a, err := t.Bots.resolveBot(ctx, ref)
	if err != nil {
		return Info{}, err
	}
	info, err := t.Extensions(ctx, a.ID)
	if err != nil {
		return Info{}, err
	}
	out := botInfo(a)
	for _, skill := range info.Skills {
		out.Skills = append(out.Skills, skill.Name)
	}
	for _, server := range info.Servers {
		out.McpServers = append(out.McpServers, server.Name)
	}
	return out, nil
}

func (t *Tools) create(ctx context.Context, c engine.TaskContext, p ToolPatch) (Info, error) {
	if err := t.Sessions.ToolBotCaller(ctx, c, true); err != nil {
		return Info{}, err
	}
	created, err := t.Bots.CreateBot(ctx, Patch{
		Name: p.Name, Instructions: p.Instructions, Provider: p.Provider, ModelID: p.ModelID,
		ThinkingLevel: p.ThinkingLevel, FallbackProvider: p.FallbackProvider, FallbackModelID: p.FallbackModelID,
		RunnerID: p.RunnerID,
	})
	return botInfo(created), err
}

func (t *Tools) update(ctx context.Context, c engine.TaskContext, p ToolPatch) (Info, error) {
	if err := t.Sessions.ToolBotCaller(ctx, c, true); err != nil {
		return Info{}, err
	}
	target, err := t.Bots.resolveBot(ctx, p.Bot)
	if err != nil {
		return Info{}, err
	}
	updated, err := t.Bots.patchBot(ctx, target.ID, Patch{
		Name: p.Name, Instructions: p.Instructions, Provider: p.Provider, ModelID: p.ModelID,
		ThinkingLevel: p.ThinkingLevel, FallbackProvider: p.FallbackProvider, FallbackModelID: p.FallbackModelID,
		RunnerID: p.RunnerID,
	})
	return botInfo(updated), err
}

func (t *Tools) archive(ctx context.Context, c engine.TaskContext, a DeleteArgs) (session.QuestionInfo, error) {
	if err := t.Sessions.ToolBotCaller(ctx, c, true); err != nil {
		return session.QuestionInfo{}, err
	}
	target, err := t.Bots.resolveBot(ctx, a.Bot)
	if err != nil {
		return session.QuestionInfo{}, err
	}
	return t.Sessions.ToolAskArchive(ctx, c, target.ID, target.Name, a.Reason)
}
