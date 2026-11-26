package session

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/runner"
)

// Origin is the session that a fork copied its history from.
type Origin struct {
	ID    int64
	Title string
	Main  bool
}

// Fork starts a new Session from the history of a writable session, up to entryID or, with before, up
// to the entry before it. The fork takes the source's model, thinking level and project, and it
// does not run. A running source contributes only its committed entries. When nothing comes
// before entryID, the fork is an empty Session with the same settings. Files on disk stay as they are.
// A non-empty runnerID (local or an SSH environment) selects the environment of the fork;
// empty keeps the selection of the source.
func (s *Service) Fork(ctx context.Context, botID string, sessionID, entryID int64, before bool, requestID, runnerID string) (Session, error) {
	c, err := s.Get(ctx, botID, sessionID)
	if err != nil {
		return Session{}, err
	}
	if !c.Writable {
		return Session{}, ErrNotFound
	}
	conv, err := s.q.GetSession(ctx, sessionID)
	if err != nil {
		return Session{}, err
	}
	target := cmp.Or(runnerID, conv.RunnerID)
	if requestID != "" {
		if receipt, err := s.q.GetForkReceipt(ctx, requestID); err == nil {
			return s.forkReplay(ctx, botID, receipt, sessionID, entryID, before, target)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return Session{}, err
		}
	}
	// Only a new fork needs a target that still exists. A retry gets its fork after a removal.
	if runnerID != "" {
		if err := checkRunner(ctx, s.q, runnerID); err != nil {
			return Session{}, err
		}
	}
	var store func(context.Context, *sqlc.Queries, sqlc.Session) error
	if requestID != "" {
		store = func(ctx context.Context, q *sqlc.Queries, fork sqlc.Session) error {
			created, err := q.CreateForkReceipt(ctx, sqlc.CreateForkReceiptParams{ID: requestID, SourceID: sessionID, EntryID: entryID, BeforeEntry: boolInt(before), ForkID: fork.ID})
			if err == nil && created == 0 {
				return errRepeatedFork
			}
			return err
		}
	}
	fork, err := s.fork(ctx, conv, entryID, before, target, store)
	if errors.Is(err, errRepeatedFork) {
		receipt, err := s.q.GetForkReceipt(ctx, requestID)
		if err != nil {
			return Session{}, err
		}
		return s.forkReplay(ctx, botID, receipt, sessionID, entryID, before, target)
	}
	if err != nil {
		return Session{}, err
	}
	return s.Get(ctx, botID, fork.ID)
}

// ErrOperationID means an operation ID was reused for different parameters.
var ErrOperationID = errors.New("the operation ID belongs to another request")

var errRepeatedFork = errors.New("fork receipt already exists")

// forkReplay returns the fork of receipt when the retry asks for the same fork. A fork cannot
// change its environment, so its row tells the environment that the first call selected.
func (s *Service) forkReplay(ctx context.Context, botID string, receipt sqlc.ForkReceipt, sessionID, entryID int64, before bool, runnerID string) (Session, error) {
	if receipt.SourceID != sessionID || receipt.EntryID != entryID || receipt.BeforeEntry != boolInt(before) {
		return Session{}, ErrOperationID
	}
	fork, err := s.q.GetSession(ctx, receipt.ForkID)
	if err != nil {
		return Session{}, err
	}
	if fork.RunnerID != runnerID {
		return Session{}, ErrOperationID
	}
	return s.Get(ctx, botID, receipt.ForkID)
}

// fork copies the history of conv into a new Session that has the parent of conv and runs in
// runnerID (empty follows the bot). The optional store runs in the transaction that creates
// the fork. Entry zero forks at the newest finished answer. A fork that moves to another effective
// environment drops the cwd. A fork whose copied history last ran in another environment ends that
// history with a notice.
func (s *Service) fork(ctx context.Context, conv sqlc.Session, entryID int64, before bool, runnerID string, store func(context.Context, *sqlc.Queries, sqlc.Session) error) (sqlc.Session, error) {
	if s.leaseUntil.Load() != 0 {
		return sqlc.Session{}, engine.ErrMaintenance
	}
	if !conv.ExecutionID.Valid {
		return sqlc.Session{}, fmt.Errorf("%w: entry %d is not in this session", engine.ErrInvalidInput, entryID)
	}
	bot, err := s.q.GetBot(ctx, conv.BotID)
	if err != nil {
		return sqlc.Session{}, err
	}
	from, to := effectiveRunner(bot, conv.RunnerID), effectiveRunner(bot, runnerID)
	cwd := conv.Cwd
	if from != to {
		cwd = ""
	}
	// The copied history can end before an earlier move of the source. Then it last ran in the
	// environment that the move left, and the fork needs its own notice.
	move := func(ranIn string) (engine.Move, error) {
		ran := cmp.Or(ranIn, from)
		if ran == to {
			return engine.Move{}, nil
		}
		text, err := s.moveNotice(ctx, ran, to)
		return engine.Move{From: ran, Text: text}, err
	}
	ref, copied, err := s.engine.Fork(ctx, engine.Ref{BotID: conv.BotID, ID: conv.ExecutionID.Int64}, entryID, before, move)
	if err != nil {
		return sqlc.Session{}, err
	}
	title := conv.Title
	if conv.ChannelKey == MainKey {
		title = "Main"
	}
	if title != "" {
		title += " (fork)"
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return sqlc.Session{}, err
	}
	defer func() { _ = tx.Rollback() }()
	// The notice and the cwd depend on the environment of the bot, which can change meanwhile.
	if bot, err = q.GetBot(ctx, conv.BotID); err != nil {
		return sqlc.Session{}, err
	}
	if effectiveRunner(bot, conv.RunnerID) != from || effectiveRunner(bot, runnerID) != to {
		return sqlc.Session{}, fmt.Errorf("%w: the environment of the bot changed during the fork; fork again", engine.ErrBusy)
	}
	fork, err := q.CreateFork(ctx, sqlc.CreateForkParams{
		BotID: conv.BotID, ChannelKey: sessionPrefix + uuid.NewV7().String(), Title: title,
		ModelProvider: conv.ModelProvider, ModelID: conv.ModelID, ThinkingLevel: conv.ThinkingLevel, Cwd: cwd, RunnerID: runnerID,
		ExecutionID: sql.NullInt64{Int64: ref.ID, Valid: copied}, ForkedFrom: sql.NullInt64{Int64: conv.ID, Valid: true}, ParentID: conv.ParentID,
		CreatedAt: s.now().UnixMilli(),
	})
	if err != nil {
		return fork, err
	}
	if store != nil {
		if err := store(ctx, q, fork); err != nil {
			return fork, err
		}
	}
	return fork, tx.Commit()
}

// moveNotice tells the user and the bot that a fork runs in environment to instead of from. A
// deleted environment shows its ID, because a fork is the way out of a session on it.
func (s *Service) moveNotice(ctx context.Context, from, to string) (string, error) {
	name := func(id string) (string, error) {
		if id == runner.LocalID {
			return id, nil
		}
		row, err := s.q.GetRunner(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return id, nil
		}
		return row.Name, err
	}
	fromName, err := name(from)
	if err != nil {
		return "", err
	}
	toName, err := name(to)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("This session moved from `%s` to `%s`. Session history was kept. Files and background tasks were not moved. Check that the files you need exist before continuing.", fromName, toName), nil
}
