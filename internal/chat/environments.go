package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/execenv"
)

var (
	// ErrEnvironmentInvalid is a session environment that does not exist or that a session cannot
	// select.
	ErrEnvironmentInvalid = errors.New("invalid session environment")
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

// checkEnvironment fails with ErrEnvironmentInvalid unless environmentID is LocalID or a saved
// pi-env environment. E2B and runner environments serve only the bot that has them as its own
// environment: an E2B sandbox and a runner belong to one bot.
func checkEnvironment(ctx context.Context, q *sqlc.Queries, environmentID string) error {
	if environmentID == execenv.LocalID {
		return nil
	}
	row, err := q.GetEnvironment(ctx, environmentID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s does not exist; call environment_list", ErrEnvironmentInvalid, environmentID)
	}
	if err != nil {
		return err
	}
	if execenv.Kind(row.Kind) != execenv.KindPiEnv {
		return fmt.Errorf("%w: %s is a %s environment; select local or an SSH environment", ErrEnvironmentInvalid, row.Name, row.Kind)
	}
	return nil
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
		if err := checkEnvironment(ctx, s.q, environmentID); err != nil {
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
