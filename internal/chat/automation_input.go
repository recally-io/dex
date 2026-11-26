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
	RoutineSkipNoModel       = "the bot has no model"
	RoutineSkipMissed        = "dex did not run at the scheduled time"
	RoutineSkipBusy          = "the chat of the automation is busy"
	routineSkipTargetSession = "the target session is missing or cannot accept Automation inputs"
)

const (
	routineRunnerPrefix      = "the Runner "
	routineUnavailableSuffix = " is unavailable"
	routineRemovedSuffix     = " was removed"
)

// RoutineUnavailableRunner describes a failed probe without including its diagnostics.
func RoutineUnavailableRunner(name string) string {
	return routineRunnerPrefix + name + routineUnavailableSuffix
}

// RoutineRemovedRunner describes a deleted Runner by its saved ID.
func RoutineRemovedRunner(id string) string {
	return routineRunnerPrefix + id + routineRemovedSuffix
}

// AutomationInput binds one run record to its durable message intent.
type AutomationInput struct {
	Expected                                 *sqlc.Automation
	Run                                      sqlc.CreateAutomationRunParams
	AgentID, ChannelKey, Title, Content, Cwd string
	// RunnerID is the Runner of the Automation: empty follows the bot.
	RunnerID string
	// Target is the effective Runner that the preflight checked, and Skip is its reason to
	// skip the run. The input is stored only when the Runner is still Target.
	Target, Skip string
}

// AutomationTargetEligible applies the same rule to saves, admission, and the session picker.
func AutomationTargetEligible(conv sqlc.Conversation, agentID string) bool {
	return conv.AgentID == agentID && !conv.ArchivedAt.Valid && acceptsInput(conv.ChannelKey, conv.ParentID)
}

// ErrAutomationTarget identifies a binding that cannot accept an Automation input.
var ErrAutomationTarget = errors.New(routineSkipTargetSession)

func AutomationTarget(ctx context.Context, q *sqlc.Queries, agentID string, id sql.NullInt64) (sqlc.Conversation, error) {
	if !id.Valid {
		return sqlc.Conversation{}, ErrAutomationTarget
	}
	conv, err := q.GetConversation(ctx, id.Int64)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !AutomationTargetEligible(conv, agentID)) {
		return sqlc.Conversation{}, ErrAutomationTarget
	}
	return conv, err
}

// AutomationInputResult describes a committed run or a replay that made no change.
type AutomationInputResult struct {
	ConversationID int64
	Skip           string
	Created        bool
}

// EnqueueAutomation commits both records before it wakes native delivery.
func (s *Service) EnqueueAutomation(ctx context.Context, in AutomationInput) (AutomationInputResult, error) {
	s.admissionMu.RLock()
	defer s.admissionMu.RUnlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AutomationInputResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	out, err := StoreAutomationInput(ctx, q, in)
	if err != nil || !out.Created {
		return AutomationInputResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return AutomationInputResult{}, err
	}
	if out.ConversationID != 0 {
		s.WakeConversation(out.ConversationID)
	} else {
		s.WakeAutomationOutbox(ctx, in.Run.ID)
	}
	return out, nil
}

// bindAutomationRunner selects the Runner of the Automation for its chat. A reused chat
// continues only in the effective Runner (ranIn) of its last run, because its history, paths,
// and tool results belong to that machine. A chat cannot change its Runner after its first
// message, so a reused chat from another Runner moves to the key of this run, where it stays
// readable, and the run starts a new chat. This also covers an Automation that follows the bot after
// the bot's Runner changed.
func bindAutomationRunner(ctx context.Context, q *sqlc.Queries, conv sqlc.Conversation, in AutomationInput, ranIn string) (sqlc.Conversation, error) {
	// A chat with no run stays. A last run with an empty binding is from before runs stored their
	// Runner, so where it ran is not known, and the chat starts again.
	last, err := q.LastAutomationRunRunner(ctx, conv.ID)
	moved := err == nil && last != ranIn
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return conv, err
	}
	if !moved {
		if conv.RunnerID == in.RunnerID {
			return conv, nil
		}
		n, err := q.SetChatRunner(ctx, sqlc.SetChatRunnerParams{RunnerID: in.RunnerID, ID: conv.ID, AgentID: in.AgentID})
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
	n, err := q.SetChatRunner(ctx, sqlc.SetChatRunnerParams{RunnerID: in.RunnerID, ID: conv.ID, AgentID: in.AgentID})
	if err == nil && n != 1 {
		err = fmt.Errorf("could not set Runner %q for Automation chat %d", in.RunnerID, conv.ID)
	}
	return conv, err
}

// StoreAutomationInput stores a run and input in the caller's control transaction.
// The caller must wake native delivery after commit only when the run has an input.
// A skipped run has no input and its chat ID is 0.
func StoreAutomationInput(ctx context.Context, q *sqlc.Queries, in AutomationInput) (AutomationInputResult, error) {
	// Take the SQLite writer before reading, so an edit or an archive cannot pass a check before the insert.
	if err := q.LockAutomationTools(ctx); err != nil {
		return AutomationInputResult{}, err
	}
	if err := db.RequireActive(ctx, q, in.AgentID); err != nil {
		return AutomationInputResult{}, err
	}
	var current sqlc.Automation
	if in.Expected != nil {
		var err error
		current, err = q.GetAutomation(ctx, sqlc.GetAutomationParams{ID: in.Expected.ID, AgentID: in.Expected.AgentID})
		expected := *in.Expected
		if current.Session == "session" && !current.TargetConversationID.Valid {
			// ON DELETE SET NULL invalidates the binding, not the scheduled snapshot.
			expected.TargetConversationID = sql.NullInt64{}
		}
		if errors.Is(err, sql.ErrNoRows) || (err == nil && current != expected) {
			return AutomationInputResult{}, ErrAutomationChanged
		}
		if err != nil {
			return AutomationInputResult{}, err
		}
	}
	// Resolve the target and its settings under the writer, without changing the conversation.
	bot, err := q.GetAgent(ctx, in.AgentID)
	if err != nil {
		return AutomationInputResult{}, err
	}
	bound := current.Session == "session"
	var conv sqlc.Conversation
	ranIn, cwd := effectiveRunner(bot, in.RunnerID), in.Cwd
	if bound && in.Skip != RoutineSkipMissed {
		conv, err = AutomationTarget(ctx, q, in.AgentID, current.TargetConversationID)
		switch {
		case errors.Is(err, ErrAutomationTarget):
			in.Skip, in.Target = routineSkipTargetSession, ""
		case err != nil:
			return AutomationInputResult{}, err
		default:
			ranIn, cwd = effectiveRunner(bot, conv.RunnerID), conv.Cwd
			busy, err := q.CountBoundAutomationPending(ctx, current.ID)
			if err != nil {
				return AutomationInputResult{}, err
			}
			if busy > 0 {
				in.Skip, in.Target = RoutineSkipBusy, ""
			} else if in.Skip == "" && bot.ModelID == "" && conv.ModelID == "" {
				in.Skip = RoutineSkipNoModel
			}
		}
	}
	if in.Target != "" && ranIn != in.Target {
		return AutomationInputResult{}, fmt.Errorf("%w: the Automation Runner changed during its check; run it again", engine.ErrBusy)
	}
	if in.Skip != "" {
		in.Run.Status, in.Run.Reason = "skipped", sql.NullString{String: in.Skip, Valid: true}
		created, err := q.CreateAutomationRun(ctx, in.Run)
		if err != nil || created == 0 {
			return AutomationInputResult{}, err
		}
		if err := queueAutomationOutbox(ctx, q, in.Run.ID, 0, in.Run.CreatedAt); err != nil {
			return AutomationInputResult{}, err
		}
		return AutomationInputResult{Skip: in.Skip, Created: true}, nil
	}
	in.Run.RunnerID, in.Run.Cwd = ranIn, cwd
	created, err := q.CreateAutomationRun(ctx, in.Run)
	if err != nil || created == 0 {
		return AutomationInputResult{}, err
	}
	if !bound {
		conv, err = q.EnsureConversation(ctx, sqlc.EnsureConversationParams{AgentID: in.AgentID, ChannelKey: in.ChannelKey, Title: in.Title, CreatedAt: in.Run.CreatedAt})
		if err != nil {
			return AutomationInputResult{}, err
		}
		if conv, err = bindAutomationRunner(ctx, q, conv, in, ranIn); err != nil {
			return AutomationInputResult{}, err
		}
		if err = q.SetConversationCwd(ctx, sqlc.SetConversationCwdParams{ID: conv.ID, AgentID: in.AgentID, Cwd: in.Cwd}); err != nil {
			return AutomationInputResult{}, err
		}
	}
	inserted, err := q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{ID: in.Run.ID, ConversationID: conv.ID, Content: in.Content, Attachments: "[]", Origin: "automation", Producer: ProducerScheduler, Kind: "message", CreatedAt: in.Run.CreatedAt})
	if err != nil {
		return AutomationInputResult{}, err
	}
	if inserted != 1 {
		return AutomationInputResult{}, errors.New("automation run ID already has an input intent")
	}
	return AutomationInputResult{ConversationID: conv.ID, Created: true}, nil
}
