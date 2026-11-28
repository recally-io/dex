package task

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
	botSession
	botChild
)

// Actor is the authenticated identity, separate from the run origin.
type Actor struct {
	kind        string
	botID       string
	scope       botScope
	sessionID   int64
	executionID string
}

var (
	Owner  = Actor{kind: ActorOwner}
	Source = Actor{kind: ActorExternal}
	System = Actor{kind: ActorSystem}
)

// Bot represents a Bot acting from Main.
func Bot(botID string) Actor { return Actor{kind: ActorBot, botID: botID, scope: botMain} }

func SessionBot(botID string, sessionID int64) Actor {
	return Actor{kind: ActorBot, botID: botID, scope: botChild, sessionID: sessionID}
}

// BotInConversation identifies a Bot in an authenticated, writable conversation.
func BotInConversation(conv sqlc.Session) Actor {
	if conv.ParentID.Valid {
		return SessionBot(conv.BotID, conv.ID)
	}
	if conv.ChannelKey == "main" {
		return Bot(conv.BotID)
	}
	return Actor{kind: ActorBot, botID: conv.BotID, scope: botSession, sessionID: conv.ID}
}

func ExternalAgent(executionID string) Actor {
	return Actor{kind: ActorExternalAgent, executionID: executionID}
}

type Origin string

const (
	OriginUser          Origin = "user"
	OriginTaskWake      Origin = "task_wake"
	OriginSessionResult Origin = "session_result"
	OriginWake          Origin = "wake"
	OriginOther         Origin = "other"
	// OriginNativeResult is the result of a child turn with a native answer. Only Dex writes it, with
	// the child in result_child_id. It authorizes what OriginSessionResult does and the Assignee Main's closeout.
	OriginNativeResult Origin = "native_result"
)

type Action string

const (
	ActionPost             Action = "post"
	ActionDispatchExternal Action = "dispatch_external"
	ActionLinkObject       Action = "link_object"
	ActionTransition       Action = "transition"
	ActionComplete         Action = "complete"
	ActionSubmitReview     Action = "submit_review"
	ActionReview           Action = "review"
	ActionSplit            Action = "split"
	ActionAssign           Action = "assign"
	ActionCreateTopLevel   Action = "create_top_level"
	ActionOwnerLifecycle   Action = "owner_lifecycle"
	ActionLinkSession      Action = "link_session"
	ActionRename           Action = "rename"
)

func (a Actor) isBot() bool { return a.kind == ActorBot && a.botID != "" }

func (a Actor) isAssignee(task sqlc.Task) bool {
	return a.isBot() && a.botID == task.AssigneeBotID
}

func (a Actor) isReviewer(task sqlc.Task) bool {
	return a.isBot() && task.ReviewerKind == ActorBot && a.botID == task.ReviewerID
}

func (a Actor) bound(ctx context.Context, q *sqlc.Queries, taskID string) (bool, error) {
	switch {
	case a.isBot() && a.scope == botChild && a.sessionID != 0:
		return q.HasTaskBotExecution(ctx, sqlc.HasTaskBotExecutionParams{
			TaskID: taskID, BotID: a.botID, SessionID: a.sessionID,
		})
	case a.kind == ActorExternalAgent && a.executionID != "":
		return q.HasTaskExternalExecution(ctx, sqlc.HasTaskExternalExecutionParams{TaskID: taskID, ID: a.executionID})
	default:
		return false, nil
	}
}

func CanRead(ctx context.Context, q *sqlc.Queries, actor Actor, task sqlc.Task) (bool, error) {
	if actor.kind == ActorOwner || actor.scope != botChild && (actor.isAssignee(task) || actor.isReviewer(task)) {
		return true, nil
	}
	return actor.bound(ctx, q, task.ID)
}

// CanWrite checks role and run origin. Call it inside the transaction that applies the action.
// Source rules use AcceptCreating instead of the general creation path.
func CanWrite(ctx context.Context, q *sqlc.Queries, actor Actor, origin Origin, task sqlc.Task, action Action) (bool, error) {
	owner := actor.kind == ActorOwner
	main := actor.isBot() && actor.scope == botMain
	session := actor.isBot() && actor.scope == botSession
	assigneeMain := main && actor.isAssignee(task)
	user := origin == OriginUser
	interactive := user || origin == OriginTaskWake
	nativeResult := origin == OriginNativeResult
	// A top-level Session can rename a Task or link its own child on user turns.
	assigneeUserSession := session && user && actor.isAssignee(task)
	switch action {
	case ActionPost, ActionLinkObject:
		if action == ActionLinkObject && !main && !session {
			return owner && user, nil
		}
		if owner {
			return user, nil
		}
		if !interactive && origin != OriginSessionResult && !nativeResult {
			return false, nil
		}
		if main || session {
			return actor.isAssignee(task) || actor.isReviewer(task), nil
		}
		return actor.bound(ctx, q, task.ID)
	case ActionRename, ActionLinkSession:
		return interactive && (owner || assigneeMain) || assigneeUserSession, nil
	case ActionDispatchExternal, ActionTransition:
		return interactive && (owner || assigneeMain), nil
	case ActionComplete, ActionSubmitReview:
		// On a native child result, the Assignee's Main can close out the work it delegated.
		// Lifecycle rules still apply: a Task with a Reviewer must go through review.
		return interactive && owner || (interactive || nativeResult) && assigneeMain, nil
	case ActionReview:
		return owner && user || interactive && main && actor.isReviewer(task), nil
	case ActionSplit:
		return interactive && (owner || assigneeMain) && task.AuthorizedByRootID.Valid, nil
	case ActionAssign:
		if owner && user || interactive && assigneeMain {
			return true, nil
		}
		if !main || !interactive || !task.ParentTaskID.Valid || !task.AuthorizedByRootID.Valid {
			return false, nil
		}
		parent, err := q.GetRelatedTask(ctx, task.ParentTaskID.String)
		return actor.isAssignee(parent), err
	case ActionCreateTopLevel:
		return user && (owner || main || session), nil
	case ActionOwnerLifecycle:
		return owner && user, nil
	default:
		return false, nil
	}
}

// AuthorizeSessionLink checks both the Task and the parent of the Session to link.
// The child can belong to another Bot; its parent still controls the link.
func AuthorizeSessionLink(ctx context.Context, q *sqlc.Queries, actor Actor, origin Origin, task sqlc.Task, sessionID int64) error {
	if err := authorize(ctx, q, actor, origin, task, ActionLinkSession); err != nil {
		return err
	}
	if actor.kind == ActorOwner {
		return nil
	}
	notChild := fmt.Errorf("%w: session %d is not a child session of this conversation", ErrForbidden, sessionID)
	child, err := q.GetSession(ctx, sessionID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !child.ParentID.Valid {
		return notChild
	}
	if err != nil {
		return err
	}
	parent, err := q.GetSession(ctx, child.ParentID.Int64)
	if err != nil {
		return err
	}
	if BotInConversation(parent) != actor {
		return notChild
	}
	return nil
}

func authorize(ctx context.Context, q *sqlc.Queries, actor Actor, origin Origin, task sqlc.Task, action Action) error {
	ok, err := CanWrite(ctx, q, actor, origin, task, action)
	if err == nil && !ok {
		return ErrForbidden
	}
	return err
}
