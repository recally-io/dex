package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

// Patch changes the fields that are not nil.
type Patch struct {
	Name, Instructions, Provider, ModelID, ThinkingLevel, FallbackProvider, FallbackModelID, RunnerID *string
}

func applyPatch(a *Agent, p Patch) {
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

// patchAgent merges a patch into a saved active agent and applies the checks of SaveAgent. The
// read and the write share one lock, so two patches cannot lose each other's fields.
func (s *Service) patchAgent(ctx context.Context, id string, p Patch) (Agent, error) {
	var (
		oldRow sqlc.Agent
		out    Agent
	)
	err := func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		var getErr error
		oldRow, getErr = s.q.GetAgent(ctx, id)
		if errors.Is(getErr, sql.ErrNoRows) || (getErr == nil && oldRow.ArchivedAt.Valid) {
			return ErrNotFound
		}
		if getErr != nil {
			return getErr
		}
		a := agentView(oldRow)
		applyPatch(&a, p)
		var cleanErr error
		a, cleanErr = s.cleanAgent(ctx, a)
		if cleanErr != nil {
			return cleanErr
		}
		if err := s.saveSettings(ctx, oldRow, sqlc.UpdateAgentSettingsParams{
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
		return Agent{}, err
	}
	if oldRow.RunnerID != out.RunnerID && s.OnRunnerChanged != nil {
		s.OnRunnerChanged(ctx, id, oldRow.RunnerID, oldRow.RunnerSandboxID)
	}
	return out, nil
}

// resolveBot finds an active bot by ID, then by name without regard to case. A name that two bots
// share is an error, so a tool never changes the wrong bot.
func (s *Service) resolveBot(ctx context.Context, ref string) (Agent, error) {
	ref = strings.TrimSpace(ref)
	agents, err := s.ListAgents(ctx)
	if err != nil {
		return Agent{}, err
	}
	var named []Agent
	for _, a := range agents {
		if a.ID == ref {
			return a, nil
		}
		if strings.EqualFold(a.Name, ref) {
			named = append(named, a)
		}
	}
	switch len(named) {
	case 0:
		return Agent{}, fmt.Errorf("%w: no active bot has the ID or name %q", ErrNotFound, ref)
	case 1:
		return named[0], nil
	default:
		return Agent{}, fmt.Errorf("%w: %d bots are named %q; use the ID", ErrInvalid, len(named), ref)
	}
}

// BotTools serves the bot tools. It owns the mapping from tool input to the same
// service calls that the web uses; chat.Service checks the source of every call.
type BotTools struct {
	Agents     *Service
	Chats      *chat.Service
	Extensions func(ctx context.Context, agentID string) (engine.ExtensionInfo, error)
}

func botInfo(a Agent) BotInfo {
	return BotInfo{
		ID: a.ID, Name: a.Name, Provider: a.Provider, ModelID: a.ModelID, ThinkingLevel: a.ThinkingLevel,
		FallbackProvider: a.FallbackProvider, FallbackModelID: a.FallbackModelID,
		Instructions: a.Instructions, RunnerID: a.RunnerID,
	}
}

// Handlers returns the engine callbacks.
func (t *BotTools) Handlers() BotHandlers {
	return BotHandlers{List: t.list, Get: t.get, Create: t.create, Update: t.update, Archive: t.archive}
}

func (t *BotTools) list(ctx context.Context, c engine.TaskContext) ([]BotInfo, error) {
	if err := t.Chats.ToolBotCaller(ctx, c, false); err != nil {
		return nil, err
	}
	agents, err := t.Agents.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := t.Agents.q.AgentRunningSessions(ctx)
	if err != nil {
		return nil, err
	}
	running := make(map[string]int64, len(rows))
	for _, row := range rows {
		running[row.AgentID] = row.Sessions
	}
	out := make([]BotInfo, len(agents))
	for i, a := range agents {
		out[i] = botInfo(a)
		out[i].Instructions, _, _ = strings.Cut(strings.TrimSpace(a.Instructions), "\n")
		// Not Activity.ActiveSessions: that sidebar count leaves Main out, and bot_delete refuses
		// a bot that has any pending input.
		n := running[a.ID]
		out[i].RunningSessions = &n
	}
	return out, nil
}

func (t *BotTools) get(ctx context.Context, c engine.TaskContext, ref string) (BotInfo, error) {
	if err := t.Chats.ToolBotCaller(ctx, c, false); err != nil {
		return BotInfo{}, err
	}
	a, err := t.Agents.resolveBot(ctx, ref)
	if err != nil {
		return BotInfo{}, err
	}
	info, err := t.Extensions(ctx, a.ID)
	if err != nil {
		return BotInfo{}, err
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

func (t *BotTools) create(ctx context.Context, c engine.TaskContext, p BotPatch) (BotInfo, error) {
	if err := t.Chats.ToolBotCaller(ctx, c, true); err != nil {
		return BotInfo{}, err
	}
	created, err := t.Agents.CreateAgent(ctx, Patch{
		Name: p.Name, Instructions: p.Instructions, Provider: p.Provider, ModelID: p.ModelID,
		ThinkingLevel: p.ThinkingLevel, FallbackProvider: p.FallbackProvider, FallbackModelID: p.FallbackModelID,
		RunnerID: p.RunnerID,
	})
	return botInfo(created), err
}

func (t *BotTools) update(ctx context.Context, c engine.TaskContext, p BotPatch) (BotInfo, error) {
	if err := t.Chats.ToolBotCaller(ctx, c, true); err != nil {
		return BotInfo{}, err
	}
	target, err := t.Agents.resolveBot(ctx, p.Bot)
	if err != nil {
		return BotInfo{}, err
	}
	updated, err := t.Agents.patchAgent(ctx, target.ID, Patch{
		Name: p.Name, Instructions: p.Instructions, Provider: p.Provider, ModelID: p.ModelID,
		ThinkingLevel: p.ThinkingLevel, FallbackProvider: p.FallbackProvider, FallbackModelID: p.FallbackModelID,
		RunnerID: p.RunnerID,
	})
	return botInfo(updated), err
}

func (t *BotTools) archive(ctx context.Context, c engine.TaskContext, a BotDeleteArgs) (chat.QuestionInfo, error) {
	if err := t.Chats.ToolBotCaller(ctx, c, true); err != nil {
		return chat.QuestionInfo{}, err
	}
	target, err := t.Agents.resolveBot(ctx, a.Bot)
	if err != nil {
		return chat.QuestionInfo{}, err
	}
	return t.Chats.ToolAskArchive(ctx, c, target.ID, target.Name, a.Reason)
}
