package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/execenv"
)

var (
	// ErrEnvironmentNotAllowed is a session environment that is not on the allowlist of its bot.
	ErrEnvironmentNotAllowed = errors.New("execution environment is not allowed for this bot")
	// ErrEnvironmentLocked is an environment change of a chat that has started. Use Fork.
	ErrEnvironmentLocked = errors.New("the environment of a chat is set before its first message")
)

// SessionEnvironment returns the explicit execution environment of the session ref, or empty when
// it follows its bot. The engine calls it for each tool call. A native conversation without a
// stored session follows its bot: chat stores the execution ID before the first input, so no
// tool of a session runs before its row exists.
func (s *Service) SessionEnvironment(ctx context.Context, ref engine.Ref) (string, error) {
	conv, err := s.q.GetConversationByExecution(ctx, sqlc.GetConversationByExecutionParams{AgentID: ref.AgentID, ExecutionScope: ref.Scope, ExecutionID: sql.NullInt64{Int64: ref.ID, Valid: true}})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return conv.EnvironmentID, nil
}

// checkEnvironment fails with ErrEnvironmentNotAllowed when environmentID is neither the
// environment of agentID nor on its allowlist. The error names the allowed environments.
func checkEnvironment(ctx context.Context, q *sqlc.Queries, agentID, environmentID string) error {
	allowed, err := q.ListAllowedEnvironments(ctx, agentID)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(allowed))
	for _, e := range allowed {
		if e.ID == environmentID {
			return nil
		}
		names = append(names, fmt.Sprintf("%s (%s)", e.Name, e.ID))
	}
	return fmt.Errorf("%w: %s; allowed: %s", ErrEnvironmentNotAllowed, environmentID, strings.Join(names, ", "))
}

// effectiveEnvironment is the environment that a session with selection id runs in: id, or the
// environment of the bot for an empty id.
func effectiveEnvironment(agent sqlc.Agent, id string) string {
	if id != "" {
		return id
	}
	if agent.EnvironmentID == "" {
		return execenv.LocalID
	}
	return agent.EnvironmentID
}

// SetEnvironment selects the execution environment of a chat before its first message. An empty
// environmentID makes the chat follow its bot. The selection clears the project of the chat,
// because a path of one environment means nothing in another.
func (s *Service) SetEnvironment(ctx context.Context, agentID string, id int64, environmentID string) (Chat, error) {
	conv, err := s.q.GetConversation(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && conv.AgentID != agentID) {
		return Chat{}, ErrNotFound
	}
	if err != nil {
		return Chat{}, err
	}
	if conv.ExecutionScope != "" || !acceptsInput(conv.ChannelKey, conv.ParentID) {
		return Chat{}, ErrProjectForbidden
	}
	if environmentID != "" {
		if err := checkEnvironment(ctx, s.q, agentID, environmentID); err != nil {
			return Chat{}, err
		}
	}
	n, err := s.q.SetChatEnvironment(ctx, sqlc.SetChatEnvironmentParams{EnvironmentID: environmentID, ID: id, AgentID: agentID})
	if err != nil {
		return Chat{}, err
	}
	if n == 0 && conv.EnvironmentID != environmentID {
		return Chat{}, ErrEnvironmentLocked
	}
	return s.Get(ctx, agentID, id)
}
