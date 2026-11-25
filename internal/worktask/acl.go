package worktask

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/recally-io/dex/internal/db/sqlc"
)

type botScope uint8

const (
	botMain botScope = iota
	botChat
	botChild
)

// Actor is the authenticated identity, separate from the run origin.
type Actor struct {
	kind           string
	botID          string
	scope          botScope
	conversationID int64
	executionID    string
}

var (
	Owner  = Actor{kind: ActorOwner}
	Source = Actor{kind: ActorExternal}
	System = Actor{kind: ActorSystem}
)

// Bot represents a Bot acting from Main.
func Bot(botID string) Actor { return Actor{kind: ActorBot, botID: botID, scope: botMain} }

func SessionBot(botID string, conversationID int64) Actor {
	return Actor{kind: ActorBot, botID: botID, scope: botChild, conversationID: conversationID}
}

// BotInConversation identifies a Bot in an authenticated, writable conversation.
func BotInConversation(conv sqlc.Conversation) Actor {
	if conv.ParentID.Valid {
		return SessionBot(conv.AgentID, conv.ID)
	}
	if conv.ChannelKey == "main" {
		return Bot(conv.AgentID)
	}
	return Actor{kind: ActorBot, botID: conv.AgentID, scope: botChat, conversationID: conv.ID}
}

func ExternalAgent(executionID string) Actor {
	return Actor{kind: ActorExternalAgent, executionID: executionID}
}

type Origin string

const (
	OriginUser       Origin = "user"
	OriginTaskWake   Origin = "task_wake"
	OriginTaskResult Origin = "task_result"
	OriginOther      Origin = "other"
)

type Action string

const (
	ActionPost           Action = "post"
	ActionTransition     Action = "transition"
	ActionSubmitReview   Action = "submit_review"
	ActionReview         Action = "review"
	ActionSplit          Action = "split"
	ActionCreateTopLevel Action = "create_top_level"
	ActionOwnerLifecycle Action = "owner_lifecycle"
	ActionTombstone      Action = "tombstone"
	ActionLinkSession    Action = "link_session"
)

func (a Actor) isBot() bool { return a.kind == ActorBot && a.botID != "" }

func (a Actor) isAssignee(task sqlc.WorkTask) bool {
	return a.isBot() && a.botID == task.AssigneeBotID
}

func (a Actor) isReviewer(task sqlc.WorkTask) bool {
	return a.isBot() && task.ReviewerKind == ActorBot && a.botID == task.ReviewerID
}

func (a Actor) bound(ctx context.Context, q *sqlc.Queries, taskID string) (bool, error) {
	switch {
	case a.isBot() && a.scope == botChild && a.conversationID != 0:
		return q.HasWorkTaskBotExecution(ctx, sqlc.HasWorkTaskBotExecutionParams{
			TaskID: taskID, BotID: a.botID, ConversationID: a.conversationID,
		})
	case a.kind == ActorExternalAgent && a.executionID != "":
		return q.HasWorkTaskExternalExecution(ctx, sqlc.HasWorkTaskExternalExecutionParams{TaskID: taskID, ID: a.executionID})
	default:
		return false, nil
	}
}

func CanRead(ctx context.Context, q *sqlc.Queries, actor Actor, task sqlc.WorkTask) (bool, error) {
	if actor.kind == ActorOwner || actor.scope != botChild && (actor.isAssignee(task) || actor.isReviewer(task)) {
		return true, nil
	}
	return actor.bound(ctx, q, task.ID)
}

// CanWrite checks role and run origin. Call it inside the transaction that applies the action.
// Source rules use AcceptCreating instead of the general creation path.
func CanWrite(ctx context.Context, q *sqlc.Queries, actor Actor, origin Origin, task sqlc.WorkTask, action Action) (bool, error) {
	owner := actor.kind == ActorOwner
	main := actor.isBot() && actor.scope == botMain
	chat := actor.isBot() && actor.scope == botChat
	assigneeMain := main && actor.isAssignee(task)
	user := origin == OriginUser
	interactive := user || origin == OriginTaskWake
	// A top-level Chat keeps the legacy task_update actions on user turns.
	assigneeUserChat := chat && user && actor.isAssignee(task)
	switch action {
	case ActionPost:
		if owner {
			return user, nil
		}
		if !interactive && origin != OriginTaskResult {
			return false, nil
		}
		if main || chat {
			return actor.isAssignee(task) || actor.isReviewer(task), nil
		}
		return actor.bound(ctx, q, task.ID)
	case ActionTransition, ActionLinkSession:
		return interactive && (owner || assigneeMain) || assigneeUserChat, nil
	case ActionSubmitReview:
		return interactive && (owner || assigneeMain), nil
	case ActionReview:
		return owner && user || interactive && main && actor.isReviewer(task), nil
	case ActionSplit:
		return interactive && (owner || assigneeMain) && task.AuthorizedByRootID.Valid, nil
	case ActionCreateTopLevel:
		return user && (owner || main || chat), nil
	case ActionOwnerLifecycle, ActionTombstone:
		return owner && user, nil
	default:
		return false, nil
	}
}

// AuthorizeSessionLink checks both the Task and the parent of the Session to link.
// The child can belong to another Bot; its parent still controls the link.
func AuthorizeSessionLink(ctx context.Context, q *sqlc.Queries, actor Actor, origin Origin, task sqlc.WorkTask, conversationID int64) error {
	if err := authorize(ctx, q, actor, origin, task, ActionLinkSession); err != nil {
		return err
	}
	if actor.kind == ActorOwner {
		return nil
	}
	notChild := fmt.Errorf("%w: session %d is not a child session of this conversation", ErrForbidden, conversationID)
	child, err := q.GetConversation(ctx, conversationID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !child.ParentID.Valid {
		return notChild
	}
	if err != nil {
		return err
	}
	parent, err := q.GetConversation(ctx, child.ParentID.Int64)
	if err != nil {
		return err
	}
	if BotInConversation(parent) != actor {
		return notChild
	}
	return nil
}

func authorize(ctx context.Context, q *sqlc.Queries, actor Actor, origin Origin, task sqlc.WorkTask, action Action) error {
	ok, err := CanWrite(ctx, q, actor, origin, task, action)
	if err == nil && !ok {
		return ErrForbidden
	}
	return err
}
