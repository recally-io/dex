package policy

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/session"
)

type menuAction struct{ text, action, object string }

func (p *Policy) liveOwnerSession(ctx context.Context, peer string, identity session.ChannelContext) (session.Session, error) {
	if err := p.RequirePairedChannel(ctx, p.q, peer, identity); err != nil {
		return session.Session{}, err
	}
	return p.sessions.Main(ctx, p.botID)
}

// RunCommand resolves the target once. A revoked actor or stale button gets no reply.
// The Session service repeats the callback check under the effect writer.
func (p *Policy) RunCommand(ctx context.Context, c sqlc.ChannelCommand, runShared func(context.Context, int64) (string, error)) (string, bool, error) {
	var identity session.ChannelContext
	if err := json.Unmarshal([]byte(c.ChannelContext), &identity); err != nil {
		return "", false, err
	}
	target, err := p.liveOwnerSession(ctx, c.Peer, identity)
	if errors.Is(err, ErrTelegramUnauthorized) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if err := validateTelegramControl(ctx, p.q, c.SourceKey, p.botID, target.ID); err != nil {
		if errors.Is(err, ErrTelegramUnauthorized) {
			return "", false, nil
		}
		return "", false, err
	}
	answer, handled, err := p.telegramCommand(ctx, c, target, identity)
	if errors.Is(err, ErrTelegramUnauthorized) {
		return "", false, nil
	}
	if err != nil || handled {
		return answer, handled, err
	}
	answer, err = runShared(ctx, target.ID)
	if errors.Is(err, ErrTelegramUnauthorized) || errors.Is(err, session.ErrChannelUnauthorized) {
		// A rejected Clear still wakes the sender, as a completed Clear does.
		return "", c.Command != "/stop", nil
	}
	return answer, true, err
}

func (p *Policy) telegramCommand(ctx context.Context, c sqlc.ChannelCommand, target session.Session, identity session.ChannelContext) (string, bool, error) {
	switch c.Command {
	case "/menu", "/help":
		return "Current conversation\n\n/help and /menu open these controls. /status shows this conversation. /settings shows Telegram preferences. /sessions lists its direct children. /workflows lists saved child workflows. /run <child ID> [message] resumes a saved child as its initiating sender. /question shows its open question. /stop stops only this conversation and its permitted child work. /clear and /new clear only this conversation. Use /feedback in private for a public GitHub issue draft.", true, nil
	case "/status":
		count, err := p.q.PendingCount(ctx, target.ID)
		pending := count > 0
		return fmt.Sprintf("Current conversation %d\nPending work: %t", target.ID, pending), true, err
	case "/settings":
		return "Change Telegram preferences on the Channels page in Dex.", true, nil
	case "/sessions", "/workflows":
		children, err := p.sessions.DirectChildren(ctx, p.botID, target)
		if err != nil {
			return "", true, err
		}
		var text strings.Builder
		text.WriteString("Saved child workflows in the current conversation. These reuse existing child sessions, not private Automations. Only the initiating Telegram sender can resume a child. Ask the bot to start a child with the workflow instructions.\n")
		if len(children) == 0 {
			text.WriteString("\nNo saved children.")
		}
		for _, child := range children {
			fmt.Fprintf(&text, "\n%s. %s (%s)", child.ID, child.Title, child.State)
		}
		return text.String(), true, nil
	case "/question":
		question, err := p.q.GetCurrentQuestion(ctx, target.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return "No open question in this conversation.", true, nil
		}
		if err != nil {
			return "", true, err
		}
		var options []string
		if err := json.Unmarshal([]byte(question.OptionsJson), &options); err != nil {
			return "", true, err
		}
		return question.Text + "\n\n" + strings.Join(options, "\n"), true, nil
	}
	if rest, ok := strings.CutPrefix(c.Command, "/run "); ok {
		id, message, _ := strings.Cut(strings.TrimSpace(rest), " ")
		// Keep the turn key of existing commands so they cannot queue a second turn after upgrade.
		err := p.sessions.ResumeChildFromSource(ctx, p.botID, target, c.Peer, c.SourceKey, "telegram-workflow:"+c.SourceKey, id, message, identity)
		if errors.Is(err, ErrTelegramUnauthorized) || errors.Is(err, session.ErrChannelUnauthorized) || errors.Is(err, session.ErrSessionNotFound) {
			return "This saved child is not available to this sender in the current conversation.", true, nil
		}
		return "Saved child workflow queued in its existing session.", true, err
	}
	if rest, ok := strings.CutPrefix(c.Command, "/answer "); ok {
		id, option, ok := strings.Cut(rest, " ")
		if !ok {
			return "Invalid question choice.", true, nil
		}
		question, err := p.q.GetQuestion(ctx, id)
		if errors.Is(err, sql.ErrNoRows) || err == nil && (question.BotID != p.botID || question.SessionID != target.ID || question.Status != "open") {
			return "This question is no longer open in this conversation.", true, nil
		}
		if err != nil {
			return "", true, err
		}
		var options []string
		if err := json.Unmarshal([]byte(question.OptionsJson), &options); err != nil {
			return "", true, err
		}
		index := -1
		for i := range options {
			if strconv.Itoa(i) == option {
				index = i
				break
			}
		}
		if index < 0 {
			return "Invalid question choice.", true, nil
		}
		err = p.sessions.SubmitMessage(ctx, p.botID, c.Peer, c.SourceKey, session.Message{Content: options[index], AnswersQuestion: id, QuestionOnly: true, ChannelContext: identity})
		if errors.Is(err, session.ErrQuestionClosed) {
			return "This question is no longer open for this sender.", true, nil
		}
		return "Answer queued in the current conversation.", true, err
	}
	return "", false, nil
}

func (p *Policy) DecorateCommandReply(ctx context.Context, c sqlc.ChannelCommand, target session.Session, part *channel.Part) error {
	var identity session.ChannelContext
	if err := json.Unmarshal([]byte(c.ChannelContext), &identity); err != nil {
		return err
	}
	actions, err := p.menuActions(ctx, c.Command, target)
	if err != nil {
		return err
	}
	return p.addButtons(ctx, part, c.SourceKey, c.Peer, identity, target, actions)
}

func (p *Policy) menuActions(ctx context.Context, command string, target session.Session) ([]menuAction, error) {
	actions := []menuAction{}
	switch command {
	case "/menu", "/help":
		for _, item := range []struct{ text, command string }{{"Status", "/status"}, {"Settings", "/settings"}, {"Sessions", "/sessions"}, {"Workflows", "/workflows"}, {"Question", "/question"}, {"Stop", "/stop"}, {"Clear context", "/clear"}} {
			actions = append(actions, menuAction{item.text, item.command, strconv.FormatInt(target.ID, 10)})
		}
	case "/sessions", "/workflows":
		children, err := p.sessions.DirectChildren(ctx, p.botID, target)
		if err != nil {
			return nil, err
		}
		for _, child := range children[:min(len(children), 10)] {
			actions = append(actions, menuAction{"Resume " + child.Title, "/run " + child.ID, child.ID})
		}
	case "/question":
		question, err := p.q.GetCurrentQuestion(ctx, target.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return questionActions(question)
	}
	return actions, nil
}

func questionActions(question sqlc.Question) ([]menuAction, error) {
	if question.Status != "open" {
		return nil, nil
	}
	var options []string
	if err := json.Unmarshal([]byte(question.OptionsJson), &options); err != nil {
		return nil, err
	}
	out := make([]menuAction, 0, len(options))
	for i, option := range options {
		out = append(out, menuAction{option, "/answer " + question.ID + " " + strconv.Itoa(i), question.ID})
	}
	return out, nil
}

func (p *Policy) addButtons(ctx context.Context, part *channel.Part, source, peer string, identity session.ChannelContext, target session.Session, actions []menuAction) error {
	if len(actions) == 0 || part.Rich == nil {
		return nil
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	revision, err := p.q.TelegramContextRevision(ctx, target.ID)
	if err != nil {
		return err
	}
	for ordinal, action := range actions {
		id := uuid.NewV7().String()
		if err := p.q.CreateTelegramCallback(ctx, sqlc.CreateTelegramCallbackParams{ID: id, BotID: p.botID, SourceID: source, Ordinal: int64(ordinal), Peer: peer, ChannelContext: string(encoded), SessionID: target.ID, ExecutionID: target.ExecutionID, ContextRevision: revision, Label: action.text, ObjectID: action.object, Action: action.action, ExpiresAt: time.Now().Add(15 * time.Minute).UnixMilli()}); err != nil {
			return err
		}
		stored, err := p.q.GetTelegramCallbackSlot(ctx, sqlc.GetTelegramCallbackSlotParams{SourceID: source, Ordinal: int64(ordinal)})
		if err != nil {
			return err
		}
		part.Buttons = append(part.Buttons, channel.Button{Text: stored.Label, Data: stored.ID})
	}
	return nil
}

// ClaimTelegramCallback consumes a button and queues its durable command in the same transaction.
// The adapter must verify the signature first. Claim calls complete only after the commit.
func (p *Policy) ClaimTelegramCallback(ctx context.Context, id, peer, remote string, identity session.ChannelContext, complete func(context.Context, string) error) (bool, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	q := p.q.WithTx(tx)
	if err := q.LockAutomationTools(ctx); err != nil {
		return false, err
	}
	row, err := q.GetTelegramCallback(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var original session.ChannelContext
	if err := json.Unmarshal([]byte(row.ChannelContext), &original); err != nil {
		return false, err
	}
	if !telegramCallbackObject(row) || row.BotID != p.botID || row.Peer != peer || row.MessageID != remote || remote == "" || row.ConsumedAt.Valid || row.ExpiresAt <= time.Now().UnixMilli() || original.AccountID != identity.AccountID || original.SenderID != identity.SenderID {
		return false, nil
	}
	allowed, err := telegramInputAllowed(ctx, q, p.botID, peer, identity)
	if err != nil {
		return false, err
	}
	if !allowed {
		if _, err := q.ConsumeTelegramCallback(ctx, sqlc.ConsumeTelegramCallbackParams{ID: id, Now: sql.NullInt64{Int64: time.Now().UnixMilli(), Valid: true}}); err != nil {
			return false, err
		}
		return false, tx.Commit()
	}
	conv, err := q.GetSession(ctx, row.SessionID)
	if err != nil {
		return false, err
	}
	revision, err := q.TelegramContextRevision(ctx, conv.ID)
	if err != nil {
		return false, err
	}
	if conv.BotID != p.botID || conv.ExecutionID.Int64 != row.ExecutionID || revision != row.ContextRevision {
		return false, nil
	}
	consumed, err := q.ConsumeTelegramCallback(ctx, sqlc.ConsumeTelegramCallbackParams{ID: id, Now: sql.NullInt64{Int64: time.Now().UnixMilli(), Valid: true}})
	if err != nil || consumed != 1 {
		return false, err
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return false, err
	}
	source := callbackSourcePrefix + id
	if err := q.CreateChannelCommand(ctx, sqlc.CreateChannelCommandParams{BotID: p.botID, SourceKey: source, Peer: peer, ChannelKey: conv.ChannelKey, Command: row.Action, ChannelContext: string(encoded)}); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, complete(ctx, source)
}
