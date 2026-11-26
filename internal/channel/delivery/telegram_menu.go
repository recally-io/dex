package delivery

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

// completeTelegram resolves a Telegram command's target once for this processing entry. It
// returns done after it completes the command itself: silently for an unauthorized, stale or
// moved command, or with the answer of a Telegram-only command. Effects recheck under the writer.
func (m *Manager) completeTelegram(ctx context.Context, c sqlc.ChannelCommand) (session.Session, bool, error) {
	var identity session.ChannelContext
	if err := json.Unmarshal([]byte(c.ChannelContext), &identity); err != nil {
		return session.Session{}, false, err
	}
	silent := func() (session.Session, bool, error) {
		return session.Session{}, true, m.q.CompleteChannelCommand(ctx, sqlc.CompleteChannelCommandParams{SourceKey: c.SourceKey, Answer: ""})
	}
	target, err := m.sessions.ChannelConversation(ctx, m.botID, c.Peer, identity)
	if errors.Is(err, session.ErrTelegramUnauthorized) {
		return silent()
	}
	if err != nil {
		return session.Session{}, false, err
	}
	current, err := m.callbackCurrent(ctx, c, target)
	if err != nil {
		return session.Session{}, false, err
	}
	if !current {
		return silent()
	}
	answer, handled, err := m.telegramCommand(ctx, c, target, identity)
	if errors.Is(err, session.ErrTelegramUnauthorized) {
		return silent()
	}
	if err != nil || !handled {
		return target, false, err
	}
	if err := m.q.CompleteChannelCommand(ctx, sqlc.CompleteChannelCommandParams{SourceKey: c.SourceKey, Answer: answer}); err != nil {
		return session.Session{}, false, err
	}
	m.Notify(c.Peer)
	return target, true, nil
}

func (m *Manager) telegramCommand(ctx context.Context, c sqlc.ChannelCommand, target session.Session, identity session.ChannelContext) (string, bool, error) {
	switch c.Command {
	case "/menu", "/help":
		return "Current conversation\n\n/help and /menu open these controls. /status shows this conversation. /settings shows Telegram preferences. /sessions lists its direct children. /workflows lists saved child workflows. /run <child ID> [message] resumes a saved child as its initiating sender. /question shows its open question. /stop stops only this conversation and its permitted child work. /clear and /new clear only this conversation. Use /feedback in private for a public GitHub issue draft.", true, nil
	case "/status":
		count, err := m.q.PendingCount(ctx, target.ID)
		pending := count > 0
		return fmt.Sprintf("Current conversation %d\nPending work: %t", target.ID, pending), true, err
	case "/settings":
		return "Change Telegram preferences on the Channels page in Dex.", true, nil
	case "/sessions", "/workflows":
		children, err := m.sessions.ChannelChildren(ctx, m.botID, target)
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
		question, err := m.q.GetCurrentQuestion(ctx, target.ID)
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
		err := m.sessions.ResumeChannelWorkflow(ctx, m.botID, c.Peer, c.SourceKey, id, message, identity)
		if errors.Is(err, session.ErrTelegramUnauthorized) || errors.Is(err, session.ErrSessionNotFound) {
			return "This saved child is not available to this sender in the current conversation.", true, nil
		}
		return "Saved child workflow queued in its existing session.", true, err
	}
	if rest, ok := strings.CutPrefix(c.Command, "/answer "); ok {
		id, option, ok := strings.Cut(rest, " ")
		if !ok {
			return "Invalid question choice.", true, nil
		}
		question, err := m.q.GetQuestion(ctx, id)
		if errors.Is(err, sql.ErrNoRows) || err == nil && (question.BotID != m.botID || question.SessionID != target.ID || question.Status != "open") {
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
		err = m.sessions.SubmitMessage(ctx, m.botID, c.Peer, c.SourceKey, session.Message{Content: options[index], AnswersQuestion: id, QuestionOnly: true, ChannelContext: identity})
		if errors.Is(err, session.ErrQuestionClosed) {
			return "This question is no longer open for this sender.", true, nil
		}
		return "Answer queued in the current conversation.", true, err
	}
	return "", false, nil
}

func (m *Manager) menuActions(ctx context.Context, command string, target session.Session) ([]menuAction, error) {
	actions := []menuAction{}
	switch command {
	case "/menu", "/help":
		for _, item := range []struct{ text, command string }{{"Status", "/status"}, {"Settings", "/settings"}, {"Sessions", "/sessions"}, {"Workflows", "/workflows"}, {"Question", "/question"}, {"Stop", "/stop"}, {"Clear context", "/clear"}} {
			actions = append(actions, menuAction{item.text, item.command, strconv.FormatInt(target.ID, 10)})
		}
	case "/sessions", "/workflows":
		children, err := m.sessions.ChannelChildren(ctx, m.botID, target)
		if err != nil {
			return nil, err
		}
		for _, child := range children[:min(len(children), 10)] {
			actions = append(actions, menuAction{"Resume " + child.Title, "/run " + child.ID, child.ID})
		}
	case "/question":
		question, err := m.q.GetCurrentQuestion(ctx, target.ID)
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

func (m *Manager) addButtons(ctx context.Context, part *channel.Part, source, peer string, identity session.ChannelContext, target session.Session, actions []menuAction) error {
	if len(actions) == 0 || part.Rich == nil {
		return nil
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	revision, err := m.q.TelegramContextRevision(ctx, target.ID)
	if err != nil {
		return err
	}
	for ordinal, action := range actions {
		id := uuid.NewV7().String()
		if err := m.q.CreateTelegramCallback(ctx, sqlc.CreateTelegramCallbackParams{ID: id, BotID: m.botID, SourceID: source, Ordinal: int64(ordinal), Peer: peer, ChannelContext: string(encoded), SessionID: target.ID, ExecutionID: target.ExecutionID, ContextRevision: revision, Label: action.text, ObjectID: action.object, Action: action.action, ExpiresAt: time.Now().Add(15 * time.Minute).UnixMilli()}); err != nil {
			return err
		}
		stored, err := m.q.GetTelegramCallbackSlot(ctx, sqlc.GetTelegramCallbackSlotParams{SourceID: source, Ordinal: int64(ordinal)})
		if err != nil {
			return err
		}
		part.Buttons = append(part.Buttons, channel.Button{Text: stored.Label, Data: stored.ID})
	}
	return nil
}

// ClaimTelegramCallback consumes a button and queues its durable command in the same transaction.
// The adapter must verify the signature before it calls this method.
func (m *Manager) ClaimTelegramCallback(ctx context.Context, id, peer, remote string, identity session.ChannelContext) (bool, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	q := m.q.WithTx(tx)
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
	if !session.TelegramCallbackObject(row) || row.BotID != m.botID || row.Peer != peer || row.MessageID != remote || remote == "" || row.ConsumedAt.Valid || row.ExpiresAt <= time.Now().UnixMilli() || original.AccountID != identity.AccountID || original.SenderID != identity.SenderID {
		return false, nil
	}
	allowed, err := session.TelegramInputAllowed(ctx, q, m.botID, peer, identity)
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
	if conv.BotID != m.botID || conv.ExecutionID.Int64 != row.ExecutionID || revision != row.ContextRevision {
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
	source := "telegram-callback:" + id
	if err := session.RequirePairedChannel(ctx, q, m.botID, peer, identity); err != nil {
		return false, err
	}
	if err := q.CreateChannelCommand(ctx, sqlc.CreateChannelCommandParams{BotID: m.botID, SourceKey: source, Peer: peer, ChannelKey: conv.ChannelKey, Command: row.Action, ChannelContext: string(encoded)}); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, m.complete(ctx, source)
}

// callbackCurrent applies the same post-claim validity check that effects repeat under the writer.
func (m *Manager) callbackCurrent(ctx context.Context, c sqlc.ChannelCommand, target session.Session) (bool, error) {
	err := session.ValidateTelegramControl(ctx, m.q, c.SourceKey, m.botID, target.ID)
	if errors.Is(err, session.ErrTelegramUnauthorized) {
		return false, nil
	}
	return err == nil, err
}

func (m *Manager) TelegramInputAllowed(ctx context.Context, peer string, source session.ChannelContext) (bool, error) {
	return m.sessions.TelegramInputAllowed(ctx, m.botID, peer, source)
}
