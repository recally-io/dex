package wechat

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/upload"
)

// Bot is one owner-only iLink account. The channel manager sends its durable replies.
type Bot struct {
	cfg      Config
	client   *client
	q        *sqlc.Queries
	channels *channel.Manager
	uploads  *upload.Store
}

// New connects a saved account to the shared channel manager.
func New(cfg Config, options Options, q *sqlc.Queries, channels *channel.Manager, uploads *upload.Store) *Bot {
	return &Bot{cfg: cfg, client: newClient(options), q: q, channels: channels, uploads: uploads}
}

func (b *Bot) state(ctx context.Context) (sqlc.WechatRuntime, error) {
	state, err := b.q.GetWechatRuntime(ctx, sqlc.GetWechatRuntimeParams{BotID: b.cfg.BotID, Owner: b.cfg.Owner})
	if errors.Is(err, sql.ErrNoRows) {
		return state, fmt.Errorf("WeChat runtime invariant: saved account has no runtime row: %w", err)
	}
	return state, err
}

func (b *Bot) report(state, reason string) {
	b.channels.Report("wechat", channel.Status{State: state, Reason: reason})
}

func (b *Bot) call(ctx context.Context, endpoint string, body map[string]any, result any, timeout time.Duration) error {
	state, err := b.state(ctx)
	if err != nil {
		return err
	}
	if state.PauseUntil > time.Now().UnixMilli() {
		b.report("needs_login", "Scan again to restore WeChat. API calls pause for one hour.")
		return ErrSessionExpired
	}
	err = b.client.call(ctx, b.cfg.BaseURL, b.cfg.Token, http.MethodPost, endpoint, body, result, timeout)
	if errors.Is(err, ErrSessionExpired) {
		saveErr := b.q.SetWechatPause(ctx, sqlc.SetWechatPauseParams{BotID: b.cfg.BotID, Owner: b.cfg.Owner, PauseUntil: time.Now().Add(time.Hour).UnixMilli()})
		b.report("needs_login", "Scan again to restore WeChat. API calls pause for one hour.")
		return errors.Join(err, saveErr)
	}
	return err
}

// Run reads updates. Cursor commits follow every durable admission in a batch.
func (b *Bot) Run(ctx context.Context) error {
	if err := b.cfg.Validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Go(func() { b.typing(ctx) })
	defer wg.Wait()
	defer cancel()
	timeout := 40 * time.Second
	for ctx.Err() == nil {
		state, err := b.state(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			b.report("error", "WeChat could not read its runtime state.")
			slog.Warn("wechat: runtime read failed", "err", err)
			if !wait(ctx, 5*time.Second) {
				return nil
			}
			continue
		}
		if remaining := time.Until(time.UnixMilli(state.PauseUntil)); remaining > 0 {
			b.report("needs_login", "Scan again to restore WeChat. API calls pause for one hour.")
			if !wait(ctx, remaining) {
				return nil
			}
			continue
		}
		var batch struct {
			Messages  []message `json:"msgs"`
			Cursor    string    `json:"get_updates_buf"`
			TimeoutMS int64     `json:"longpolling_timeout_ms"`
		}
		err = b.call(ctx, "getupdates", map[string]any{"get_updates_buf": state.Cursor}, &batch, timeout)
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			continue
		}
		if err == nil {
			for _, msg := range batch.Messages {
				if err = b.receive(ctx, msg); err != nil {
					break
				}
			}
		}
		if err == nil && batch.Cursor != "" {
			err = b.q.SetWechatCursor(ctx, sqlc.SetWechatCursorParams{BotID: b.cfg.BotID, Owner: b.cfg.Owner, Cursor: batch.Cursor})
		}
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			if !errors.Is(err, ErrSessionExpired) {
				b.report("error", "WeChat could not receive messages.")
				slog.Warn("wechat: receive failed", "err", err)
				if !wait(ctx, 5*time.Second) {
					return nil
				}
			}
			continue
		}
		b.report("running", "")
		if batch.TimeoutMS > 0 {
			timeout = time.Duration(min(batch.TimeoutMS, 180_000))*time.Millisecond + 5*time.Second
		}
	}
	return nil
}

func (b *Bot) receive(ctx context.Context, msg message) error {
	if msg.Type != 1 || msg.State != 2 || msg.Group != "" {
		return nil
	}
	if msg.From != b.cfg.Owner {
		slog.Warn("wechat: ignored a message from a sender other than the owner")
		return nil
	}
	if msg.ID == 0 {
		slog.Warn("wechat: rejected a message without its source ID")
		return nil
	}
	// Commit the reply credential before Receive can enqueue work or wake the durable sender.
	if msg.Context != "" {
		if err := b.q.SetWechatContext(ctx, sqlc.SetWechatContextParams{BotID: b.cfg.BotID, Owner: b.cfg.Owner, ContextToken: msg.Context, MessageID: fmt.Sprintf("%020d", msg.ID)}); err != nil {
			return err
		}
	}
	source := "wechat:" + b.cfg.BotID + ":" + strconv.FormatUint(msg.ID, 10)
	if _, err := b.q.GetSubmissionBySource(ctx, sql.NullString{String: source, Valid: true}); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := b.q.GetChannelCommand(ctx, source); err == nil {
		// Receive completes interrupted commands without repeating attachment side effects.
		return b.channels.Receive(ctx, "wechat:"+b.cfg.Owner, source, chat.Message{})
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	input, err := b.convert(ctx, msg.Items)
	admitted := false
	defer func() {
		if !admitted {
			for _, attachment := range input.Attachments {
				if err := b.uploads.Remove(attachment.ID); err != nil {
					slog.Warn("wechat: upload cleanup failed", "err", err)
				}
			}
		}
	}()
	if errors.Is(err, errInvalidMessage) {
		slog.Warn("wechat: rejected an invalid inbound message", "message", msg.ID)
		return nil
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(input.Content) == "" && len(input.Attachments) == 0 {
		return nil
	}
	receiveErr := b.channels.Receive(ctx, "wechat:"+b.cfg.Owner, source, input)
	// Check ownership even after an error or cancellation: admission can already be durable.
	_, err = b.q.GetSubmissionBySource(context.WithoutCancel(ctx), sql.NullString{String: source, Valid: true})
	admitted = !errors.Is(err, sql.ErrNoRows)
	if errors.Is(err, sql.ErrNoRows) {
		return receiveErr
	}
	return errors.Join(receiveErr, err)
}

// Parts filters Markdown and splits the final text into at most 2000 runes per part.
func (b *Bot) Parts(reply chat.Reply) []string {
	text := reply.Answer
	if reply.Status != "done" {
		text = "No answer. Please try again."
	}
	text = filterMarkdown(text)
	if strings.TrimSpace(text) == "" {
		text = "(empty answer)"
	}
	runes := []rune(text)
	parts := make([]string, 0, (len(runes)+1999)/2000)
	for len(runes) > 0 {
		n := min(len(runes), 2000)
		parts = append(parts, string(runes[:n]))
		runes = runes[n:]
	}
	return parts
}

// Send keeps the same client ID across retries of a durable reply part.
func (b *Bot) Send(ctx context.Context, peer, id string, part int, text string) error {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", id, part)))
	return b.send(ctx, peer, fmt.Sprintf("dex:%x", sum[:16]), text)
}

func (b *Bot) send(ctx context.Context, peer, clientID, text string) error {
	if peer != "wechat:"+b.cfg.Owner {
		return fmt.Errorf("%w: WeChat destination is not the owner", channel.ErrPermanent)
	}
	state, err := b.state(ctx)
	if err != nil {
		return err
	}
	if state.ContextToken == "" {
		return errors.New("WeChat reply waits for the owner's context token")
	}
	var result struct{}
	err = b.call(ctx, "sendmessage", map[string]any{"msg": map[string]any{
		"from_user_id": "", "to_user_id": b.cfg.Owner, "client_id": clientID, "message_type": 2, "message_state": 2,
		"context_token": state.ContextToken, "item_list": []any{map[string]any{"type": 1, "text_item": map[string]string{"text": text}}},
	}}, &result, 15*time.Second)
	if apiErr, ok := errors.AsType[*apiError](err); ok && (apiErr.InvalidUser || apiErr.HTTPStatus == 400 || apiErr.HTTPStatus == 403) {
		return fmt.Errorf("%w: %w", channel.ErrPermanent, err)
	}
	return err
}

func (b *Bot) typing(ctx context.Context) {
	ticket := ""
	for {
		if !wait(ctx, 5*time.Second) {
			return
		}
		pending, err := b.channels.Pending(ctx, "wechat:"+b.cfg.Owner)
		if err != nil || !pending {
			ticket = ""
			continue
		}
		if ticket == "" {
			state, err := b.state(ctx)
			if err != nil {
				continue
			}
			var config struct {
				Ticket string `json:"typing_ticket"`
			}
			if err = b.call(ctx, "getconfig", map[string]any{"ilink_user_id": b.cfg.Owner, "context_token": state.ContextToken}, &config, 10*time.Second); err != nil {
				continue
			}
			ticket = config.Ticket
		}
		if ticket != "" {
			var result struct{}
			if err = b.call(ctx, "sendtyping", map[string]any{"ilink_user_id": b.cfg.Owner, "typing_ticket": ticket, "status": 1}, &result, 10*time.Second); err != nil {
				ticket = ""
			}
		}
	}
}

func wait(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// Only the protocol fields used by dex enter this boundary model.
type message struct {
	ID      uint64 `json:"message_id"`
	From    string `json:"from_user_id"`
	Group   string `json:"group_id"`
	Type    int    `json:"message_type"`
	State   int    `json:"message_state"`
	Context string `json:"context_token"`
	Items   []item `json:"item_list"`
}

type item struct {
	Type int `json:"type"`
	Ref  *struct {
		Title string `json:"title"`
		Item  *item  `json:"message_item"`
	} `json:"ref_msg"`
	Text *struct {
		Text string `json:"text"`
	} `json:"text_item"`
	Voice *struct {
		Text string `json:"text"`
	} `json:"voice_item"`
	Image *struct {
		Media *media `json:"media"`
		Key   string `json:"aeskey"`
	} `json:"image_item"`
	File *struct {
		Media *media `json:"media"`
		Name  string `json:"file_name"`
	} `json:"file_item"`
}

type media struct {
	Query string `json:"encrypt_query_param"`
	Key   string `json:"aes_key"`
	URL   string `json:"full_url"`
}
