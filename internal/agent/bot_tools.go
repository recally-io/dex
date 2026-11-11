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

// AgentPatch changes the fields that are not nil.
type AgentPatch struct {
	Name, Instructions, Provider, ModelID, ThinkingLevel *string
}

// PatchAgent merges a patch into a saved active agent and applies the checks of SaveAgent. The
// read and the write share one lock, so two patches cannot lose each other's fields.
func (s *Service) PatchAgent(ctx context.Context, id string, p AgentPatch) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, err := s.q.GetAgent(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && row.ArchivedAt.Valid) {
		return Agent{}, ErrNotFound
	}
	if err != nil {
		return Agent{}, err
	}
	a := agentView(row)
	for _, f := range []struct {
		dst *string
		src *string
	}{{&a.Name, p.Name}, {&a.Instructions, p.Instructions}, {&a.Provider, p.Provider}, {&a.ModelID, p.ModelID}, {&a.ThinkingLevel, p.ThinkingLevel}} {
		if f.src != nil {
			*f.dst = *f.src
		}
	}
	a, err = s.cleanAgent(ctx, a)
	if err != nil {
		return Agent{}, err
	}
	err = s.q.UpdateAgentSettings(ctx, sqlc.UpdateAgentSettingsParams{
		ModelProvider: a.Provider, ModelID: a.ModelID, ThinkingLevel: a.ThinkingLevel, Instructions: a.Instructions,
		ID: id, Name: a.Name,
	})
	a.ArchivedAt = 0
	return a, err
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

// BotTools serves the bot tools (ADR 0016). It owns the mapping from tool input to the same
// service calls that the web uses; chat.Service checks the source of every call.
type BotTools struct {
	Agents     *Service
	Chats      *chat.Service
	Extensions func(ctx context.Context, agentID string) (engine.ExtensionInfo, error)
}

func botInfo(a Agent) engine.BotInfo {
	return engine.BotInfo{ID: a.ID, Name: a.Name, Provider: a.Provider, ModelID: a.ModelID, ThinkingLevel: a.ThinkingLevel, Instructions: a.Instructions}
}

// Handlers returns the engine callbacks.
func (t BotTools) Handlers() engine.BotTools {
	return engine.BotTools{List: t.list, Get: t.get, Create: t.create, Update: t.update, Archive: t.archive}
}

func (t BotTools) list(ctx context.Context, c engine.TaskContext) ([]engine.BotInfo, error) {
	if err := t.Chats.ToolBotCaller(ctx, c, false); err != nil {
		return nil, err
	}
	agents, err := t.Agents.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]engine.BotInfo, len(agents))
	for i, a := range agents {
		out[i] = botInfo(a)
		out[i].Instructions, _, _ = strings.Cut(strings.TrimSpace(a.Instructions), "\n")
		out[i].RunningSessions = &a.Activity.ActiveSessions
	}
	return out, nil
}

func (t BotTools) get(ctx context.Context, c engine.TaskContext, ref string) (engine.BotInfo, error) {
	if err := t.Chats.ToolBotCaller(ctx, c, false); err != nil {
		return engine.BotInfo{}, err
	}
	a, err := t.Agents.resolveBot(ctx, ref)
	if err != nil {
		return engine.BotInfo{}, err
	}
	info, err := t.Extensions(ctx, a.ID)
	if err != nil {
		return engine.BotInfo{}, err
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

func (t BotTools) create(ctx context.Context, c engine.TaskContext, p engine.BotPatch) (engine.BotInfo, error) {
	if err := t.Chats.ToolBotCaller(ctx, c, true); err != nil {
		return engine.BotInfo{}, err
	}
	a := Agent{ThinkingLevel: "off"}
	for _, f := range []struct {
		dst *string
		src *string
	}{{&a.Name, p.Name}, {&a.Instructions, p.Instructions}, {&a.Provider, p.Provider}, {&a.ModelID, p.ModelID}, {&a.ThinkingLevel, p.ThinkingLevel}} {
		if f.src != nil {
			*f.dst = *f.src
		}
	}
	created, err := t.Agents.CreateAgent(ctx, a)
	return botInfo(created), err
}

func (t BotTools) update(ctx context.Context, c engine.TaskContext, p engine.BotPatch) (engine.BotInfo, error) {
	if err := t.Chats.ToolBotCaller(ctx, c, true); err != nil {
		return engine.BotInfo{}, err
	}
	target, err := t.Agents.resolveBot(ctx, p.Bot)
	if err != nil {
		return engine.BotInfo{}, err
	}
	updated, err := t.Agents.PatchAgent(ctx, target.ID, AgentPatch{Name: p.Name, Instructions: p.Instructions, Provider: p.Provider, ModelID: p.ModelID, ThinkingLevel: p.ThinkingLevel})
	return botInfo(updated), err
}

func (t BotTools) archive(ctx context.Context, c engine.TaskContext, a engine.BotDeleteArgs) (engine.QuestionInfo, error) {
	if err := t.Chats.ToolBotCaller(ctx, c, true); err != nil {
		return engine.QuestionInfo{}, err
	}
	target, err := t.Agents.resolveBot(ctx, a.Bot)
	if err != nil {
		return engine.QuestionInfo{}, err
	}
	return t.Chats.ToolAskArchive(ctx, c, target.ID, target.Name, a.Reason)
}
