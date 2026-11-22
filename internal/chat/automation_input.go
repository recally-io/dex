package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/recally-io/dex/internal/db"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

// ErrAutomationChanged means a scheduled snapshot is no longer current.
var ErrAutomationChanged = errors.New("automation changed before scheduled input")

// These pre-start reasons are safe to store and put on a channel. Busy overlap is not a failure.
const (
	RoutineSkipNoModel = "the bot has no model"
	RoutineSkipMissed  = "dex did not run at the scheduled time"
	RoutineSkipBusy    = "the chat of the automation is busy"
)

const (
	routineEnvironmentPrefix = "the environment "
	routineUnavailableSuffix = " is unavailable"
	routineRemovedSuffix     = " was removed"
)

// RoutineUnavailableEnvironment describes a failed probe without including its diagnostics.
func RoutineUnavailableEnvironment(name string) string {
	return routineEnvironmentPrefix + name + routineUnavailableSuffix
}

// RoutineRemovedEnvironment describes a deleted environment by its saved ID.
func RoutineRemovedEnvironment(id string) string {
	return routineEnvironmentPrefix + id + routineRemovedSuffix
}

// AutomationInput binds one run record to its durable message intent.
type AutomationInput struct {
	Expected                                 *sqlc.Automation
	Run                                      sqlc.CreateAutomationRunParams
	AgentID, ChannelKey, Title, Content, Cwd string
	// EnvironmentID is the environment of the routine: empty follows the bot.
	EnvironmentID string
	// Target is the effective environment that the preflight checked, and Skip is its reason to
	// skip the run. The input is stored only when the environment is still Target.
	Target, Skip string
}

// EnqueueAutomation commits both records before it wakes native delivery.
func (s *Service) EnqueueAutomation(ctx context.Context, in AutomationInput) (int64, bool, error) {
	s.admissionMu.RLock()
	defer s.admissionMu.RUnlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	convID, created, err := StoreAutomationInput(ctx, q, in)
	if err != nil || !created {
		return 0, false, err
	}
	if err = tx.Commit(); err != nil {
		return 0, false, err
	}
	if convID != 0 {
		s.WakeConversation(convID)
	} else {
		s.WakeRoutineOutbox(ctx, in.Run.ID)
	}
	return convID, true, nil
}

// bindAutomationEnvironment selects the environment of the routine for its chat. A reused chat
// continues only in the effective environment (ranIn) of its last run, because its history, paths,
// and tool results belong to that machine. A chat cannot change its environment after its first
// message, so a reused chat from another environment moves to the key of this run, where it stays
// readable, and the run starts a new chat. This also covers a routine that follows the bot after
// the bot's environment changed.
func bindAutomationEnvironment(ctx context.Context, q *sqlc.Queries, conv sqlc.Conversation, in AutomationInput, ranIn string) (sqlc.Conversation, error) {
	// A chat with no run stays. A last run with an empty binding is from before runs stored their
	// environment, so where it ran is not known, and the chat starts again.
	last, err := q.LastAutomationRunEnvironment(ctx, conv.ID)
	moved := err == nil && last != ranIn
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return conv, err
	}
	if !moved {
		if conv.EnvironmentID == in.EnvironmentID {
			return conv, nil
		}
		n, err := q.SetChatEnvironment(ctx, sqlc.SetChatEnvironmentParams{EnvironmentID: in.EnvironmentID, ID: conv.ID, AgentID: in.AgentID})
		if err != nil || n == 1 {
			return conv, err
		}
	}
	if err := q.RetireAutomationChat(ctx, sqlc.RetireAutomationChatParams{ChannelKey: in.ChannelKey + ":" + in.Run.ID, ID: conv.ID}); err != nil {
		return conv, err
	}
	conv, err = q.EnsureConversation(ctx, sqlc.EnsureConversationParams{AgentID: in.AgentID, ChannelKey: in.ChannelKey, Title: in.Title, CreatedAt: in.Run.CreatedAt})
	if err != nil {
		return conv, err
	}
	n, err := q.SetChatEnvironment(ctx, sqlc.SetChatEnvironmentParams{EnvironmentID: in.EnvironmentID, ID: conv.ID, AgentID: in.AgentID})
	if err == nil && n != 1 {
		err = fmt.Errorf("routine chat %d did not take environment %q", conv.ID, in.EnvironmentID)
	}
	return conv, err
}

// StoreAutomationInput stores a run and input in the caller's control transaction.
// The caller must wake native delivery after commit only when the run has an input.
// A skipped run has no input and its chat ID is 0.
func StoreAutomationInput(ctx context.Context, q *sqlc.Queries, in AutomationInput) (int64, bool, error) {
	// Take the SQLite writer before reading, so an edit or an archive cannot pass a check before the insert.
	if err := q.LockRoutineTools(ctx); err != nil {
		return 0, false, err
	}
	if err := db.RequireActive(ctx, q, in.AgentID); err != nil {
		return 0, false, err
	}
	if in.Expected != nil {
		current, err := q.GetAutomation(ctx, sqlc.GetAutomationParams{ID: in.Expected.ID, AgentID: in.Expected.AgentID})
		if errors.Is(err, sql.ErrNoRows) || (err == nil && current != *in.Expected) {
			return 0, false, ErrAutomationChanged
		}
		if err != nil {
			return 0, false, err
		}
	}
	// The bot is read under the writer lock, so a change of its environment cannot come between
	// this read and the input.
	bot, err := q.GetAgent(ctx, in.AgentID)
	if err != nil {
		return 0, false, err
	}
	ranIn := effectiveEnvironment(bot, in.EnvironmentID)
	if in.Target != "" && ranIn != in.Target {
		return 0, false, fmt.Errorf("%w: the environment of the routine changed during its check; run it again", engine.ErrBusy)
	}
	if in.Skip != "" {
		in.Run.Status, in.Run.Reason = "skipped", sql.NullString{String: in.Skip, Valid: true}
		created, err := q.CreateAutomationRun(ctx, in.Run)
		if err != nil || created == 0 {
			return 0, false, err
		}
		if err := queueRoutineOutbox(ctx, q, in.Run.ID, 0, in.Run.CreatedAt); err != nil {
			return 0, false, err
		}
		return 0, true, nil
	}
	in.Run.EnvironmentID, in.Run.Cwd = ranIn, in.Cwd
	created, err := q.CreateAutomationRun(ctx, in.Run)
	if err != nil || created == 0 {
		return 0, false, err
	}
	conv, err := q.EnsureConversation(ctx, sqlc.EnsureConversationParams{AgentID: in.AgentID, ChannelKey: in.ChannelKey, Title: in.Title, CreatedAt: in.Run.CreatedAt})
	if err != nil {
		return 0, false, err
	}
	if conv, err = bindAutomationEnvironment(ctx, q, conv, in, ranIn); err != nil {
		return 0, false, err
	}
	if err = q.SetConversationCwd(ctx, sqlc.SetConversationCwdParams{ID: conv.ID, AgentID: in.AgentID, Cwd: in.Cwd}); err != nil {
		return 0, false, err
	}
	inserted, err := q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{ID: in.Run.ID, ConversationID: conv.ID, Content: in.Content, Attachments: "[]", Origin: "automation", Kind: "message", CreatedAt: in.Run.CreatedAt})
	if err != nil {
		return 0, false, err
	}
	if inserted != 1 {
		return 0, false, errors.New("automation run ID already has an input intent")
	}
	return conv.ID, true, nil
}
