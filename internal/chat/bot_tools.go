package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

// outcomeSeparator joins the answer and the decision that control writes for an action question.
const outcomeSeparator = "\n\n"

// ErrBotCaller means that the conversation or the input of a bot tool call may not use the tool.
var ErrBotCaller = errors.New("bot tool not allowed here")

const maxArchiveReason = 500

// archiveAction is the one action that a question can carry (ADR 0016).
const archiveAction = "archive"

// UserOrigin reports whether an input origin is a person: the web UI or a paired channel peer.
// It is an allow-list. Task, automation, dream, and unknown inputs are not people.
func UserOrigin(origin string) bool {
	return origin == "web" || strings.HasPrefix(origin, "telegram:") || strings.HasPrefix(origin, "feishu:") || strings.HasPrefix(origin, "wechat:")
}

// questionAction is the action that control runs when the owner approves a question.
type questionAction struct{ kind, agentID, approve string }

func (a *questionAction) store(p *sqlc.CreateQuestionParams) {
	if a == nil {
		return
	}
	p.Action = sql.NullString{String: a.kind, Valid: true}
	p.ActionAgentID = sql.NullString{String: a.agentID, Valid: true}
	p.ApproveText = sql.NullString{String: a.approve, Valid: true}
}

// allowed refuses an action question that must not be asked: the source must be a person's input,
// and the bot must be archivable now. The answer is checked again when it arrives.
func (a *questionAction) allowed(ctx context.Context, q *sqlc.Queries, r engine.TaskContext, source sqlc.Submission) error {
	if source.Kind != "message" || !UserOrigin(source.Origin) {
		return fmt.Errorf("%w: this action needs a user input in this conversation", ErrQuestionInvalid)
	}
	if a.agentID == r.AgentID {
		return fmt.Errorf("%w: a bot cannot archive itself", ErrQuestionInvalid)
	}
	return db.CheckArchive(ctx, q, a.agentID)
}

// ToolAskArchive asks the owner to archive a bot. It saves the question with the action and does
// not archive. The caller resolves the bot. The model cannot archive a bot: control runs the action
// when the owner approves the question.
func (s *Service) ToolAskArchive(ctx context.Context, r engine.TaskContext, botID, botName, reason string) (engine.QuestionInfo, error) {
	reason = strings.Join(strings.Fields(reason), " ")
	if reason == "" || utf8.RuneCountInString(reason) > maxArchiveReason {
		return engine.QuestionInfo{}, fmt.Errorf("%w: a reason of 1 to %d characters", ErrQuestionInvalid, maxArchiveReason)
	}
	name := strings.Join(strings.Fields(botName), " ")
	approve := "Archive " + name
	text := fmt.Sprintf("Archive bot %q? It leaves the sidebar, its channels stop, and its automations pause. You can restore it. Reason: %s", name, reason)
	return s.askQuestion(ctx, r, engine.AskUserArgs{Question: text, Options: []string{approve, "Cancel"}}, &questionAction{kind: archiveAction, agentID: botID, approve: approve})
}

// answerDecision is the content of an answer and what control decided for it.
type answerDecision struct {
	Content string
	// Outcome is set for an action question, approved or not.
	Outcome sql.NullString
	// archive is the bot to archive when the answer is stored.
	archive string
}

// decide settles an action question inside the receipt transaction, before the model sees the
// answer. Only a person's answer that equals the stored approve text approves. The rules for the bot
// run again now, because the bot can be busy after the owner was asked.
func (d *answerDecision) decide(ctx context.Context, q *sqlc.Queries, row sqlc.Question, answer, origin string) error {
	if row.Action.String != archiveAction {
		return fmt.Errorf("unknown question action %q", row.Action.String)
	}
	bot, err := q.GetAgent(ctx, row.ActionAgentID.String)
	if err != nil {
		return err
	}
	var outcome string
	if !UserOrigin(origin) || answer != row.ApproveText.String {
		outcome = fmt.Sprintf("dex: Not approved. Bot %q is not archived.", bot.Name)
	} else if err := db.CheckArchive(ctx, q, bot.ID); err == nil {
		d.archive = bot.ID
		outcome = fmt.Sprintf("dex: Approved. Bot %q is archived.", bot.Name)
	} else if errors.Is(err, db.ErrAgentBusy) || errors.Is(err, db.ErrDefaultAgent) || errors.Is(err, db.ErrAgentArchived) || errors.Is(err, db.ErrAgentMissing) {
		outcome = fmt.Sprintf("dex: Approved, but bot %q is not archived: %s.", bot.Name, err)
	} else {
		return err
	}
	d.Outcome = sql.NullString{String: outcome, Valid: true}
	d.Content += outcomeSeparator + outcome
	return nil
}

// apply archives the bot in the transaction that stores the answer.
func (d *answerDecision) apply(ctx context.Context, q *sqlc.Queries, now time.Time) error {
	if d.archive == "" {
		return nil
	}
	return db.ArchiveAgent(ctx, q, d.archive, now)
}

// botArchived tells the runtime that a bot is archived. Call it after the commit.
func (s *Service) botArchived(agentID string) {
	if agentID != "" && s.OnBotArchived != nil {
		s.OnBotArchived(agentID)
	}
}

// ToolBotCaller checks the source of a bot tool call. Reading needs Main, a Chat, or an automation
// session. Changing a bot also needs a person's input in the conversation: a task result, an
// automation run, or a dream cannot change a bot, as with routine tools.
func (s *Service) ToolBotCaller(ctx context.Context, r engine.TaskContext, change bool) error {
	conv, err := s.q.GetConversationByExecution(ctx, sqlc.GetConversationByExecutionParams{AgentID: r.AgentID, ExecutionID: sql.NullInt64{Int64: r.ConversationID, Valid: true}})
	if err != nil {
		return err
	}
	if !change {
		if !Writable(conv.ChannelKey) && !strings.HasPrefix(conv.ChannelKey, "automation:") {
			return fmt.Errorf("%w: bot tools need Main, a Chat, or an automation session", ErrBotCaller)
		}
		return nil
	}
	if !Writable(conv.ChannelKey) {
		return fmt.Errorf("%w: changing a bot needs Main or a Chat", ErrBotCaller)
	}
	source, err := s.q.GetSubmission(ctx, r.SourceRequestID)
	if err != nil {
		return err
	}
	if source.ConversationID != conv.ID || source.Kind != "message" || !UserOrigin(source.Origin) {
		return fmt.Errorf("%w: changing a bot needs a user input in this conversation", ErrBotCaller)
	}
	return nil
}
