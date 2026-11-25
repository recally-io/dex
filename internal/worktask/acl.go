package worktask

import (
	"context"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// Actor is the authenticated identity, separate from the run origin.
type Actor struct {
	kind           string
	botID          string
	conversationID int64
	executionID    string
}

var (
	Owner  = Actor{kind: ActorOwner}
	Source = Actor{kind: ActorExternal}
	System = Actor{kind: ActorSystem}
)

func Bot(botID string) Actor { return Actor{kind: ActorBot, botID: botID} }

func SessionBot(botID string, conversationID int64) Actor {
	return Actor{kind: ActorBot, botID: botID, conversationID: conversationID}
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
	actionLinkSession    Action = "link_session"
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
	case a.isBot():
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
	if actor.kind == ActorOwner || actor.isAssignee(task) || actor.isReviewer(task) {
		return true, nil
	}
	return actor.bound(ctx, q, task.ID)
}

// CanWrite checks role and run origin. Call it inside the transaction that applies the action.
// Source rules use AcceptCreating instead of the general creation path.
func CanWrite(ctx context.Context, q *sqlc.Queries, actor Actor, origin Origin, task sqlc.WorkTask, action Action) (bool, error) {
	owner := actor.kind == ActorOwner
	main := actor.isBot() && actor.conversationID == 0
	assigneeMain := main && actor.isAssignee(task)
	user := origin == OriginUser
	interactive := user || origin == OriginTaskWake
	switch action {
	case ActionPost:
		if owner {
			return user, nil
		}
		if !interactive && origin != OriginTaskResult {
			return false, nil
		}
		if main {
			return actor.isAssignee(task) || actor.isReviewer(task), nil
		}
		return actor.bound(ctx, q, task.ID)
	case ActionTransition, ActionSubmitReview, actionLinkSession:
		return interactive && (owner || assigneeMain), nil
	case ActionReview:
		return owner && user || interactive && main && actor.isReviewer(task), nil
	case ActionSplit:
		return interactive && (owner || assigneeMain) && task.AuthorizedByRootID.Valid, nil
	case ActionCreateTopLevel:
		return user && (owner || main), nil
	case ActionOwnerLifecycle, ActionTombstone:
		return owner && user, nil
	default:
		return false, nil
	}
}

func authorize(ctx context.Context, q *sqlc.Queries, actor Actor, origin Origin, task sqlc.WorkTask, action Action) error {
	ok, err := CanWrite(ctx, q, actor, origin, task, action)
	if err == nil && !ok {
		return ErrForbidden
	}
	return err
}
