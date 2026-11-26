package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/runner"
)

var (
	// ErrRunnerInvalid is a session environment that does not exist or that a session cannot
	// select.
	ErrRunnerInvalid = errors.New("invalid session Runner")
	// ErrRunnerLocked is an environment change of a session that has started. Use Fork.
	ErrRunnerLocked = errors.New("the Runner of a session is set before its first message")
)

// SessionRunner returns the explicit execution environment of the session ref, or empty when
// it follows its bot. The engine calls it for each tool call. A native conversation without a
// stored session follows its bot: session stores the execution ID before the first input, so no
// tool of a session runs before its row exists.
func (s *Service) SessionRunner(ctx context.Context, ref engine.Ref) (string, error) {
	conv, err := s.q.GetSessionByExecution(ctx, sqlc.GetSessionByExecutionParams{AgentID: ref.AgentID, ExecutionID: sql.NullInt64{Int64: ref.ID, Valid: true}})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return conv.RunnerID, nil
}

// checkRunner validates an explicit selection without opening the environment.
func checkRunner(ctx context.Context, q *sqlc.Queries, runnerID string) error {
	if runnerID == runner.LocalID {
		return nil
	}
	row, err := q.GetRunner(ctx, runnerID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s does not exist; call runner_list", ErrRunnerInvalid, runnerID)
	}
	if err != nil {
		return err
	}
	if !runner.IsRegisteredKind(runner.Kind(row.Kind)) {
		return fmt.Errorf("%w: %s has unsupported Runner kind %s", ErrRunnerInvalid, row.Name, row.Kind)
	}
	return nil
}

// effectiveRunner is the environment that a session with selection id runs in: id, or the
// environment of the bot for an empty id.
func effectiveRunner(agent sqlc.Agent, id string) string {
	if id != "" {
		return id
	}
	if agent.RunnerID == "" {
		return runner.LocalID
	}
	return agent.RunnerID
}

// SetRunner selects the execution environment of a session before its first message. An empty
// runnerID makes the session follow its bot. The selection clears the project of the session,
// because a path of one environment means nothing in another.
func (s *Service) SetRunner(ctx context.Context, agentID string, id int64, runnerID string) (Session, error) {
	conv, err := s.q.GetSession(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && conv.AgentID != agentID) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	if !acceptsInput(conv.ChannelKey, conv.ParentID) {
		return Session{}, ErrProjectForbidden
	}
	if runnerID != "" {
		if err := checkRunner(ctx, s.q, runnerID); err != nil {
			return Session{}, err
		}
	}
	n, err := s.q.SetSessionRunner(ctx, sqlc.SetSessionRunnerParams{RunnerID: runnerID, ID: id, AgentID: agentID})
	if err != nil {
		return Session{}, err
	}
	if n == 0 && conv.RunnerID != runnerID {
		return Session{}, ErrRunnerLocked
	}
	return s.Get(ctx, agentID, id)
}
