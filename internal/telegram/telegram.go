// Package telegram connects Telegram chats to an agent. It reads messages from the Bot API with long
// polling and sends the agent's replies back. Each Telegram chat is one conversation.
package telegram

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/recally-io/dex/internal/chat"
)

const channelPrefix = "telegram:"

// Telegram rejects a message longer than 4096 UTF-16 code units.
const maxMessageLen = 4096

// Config configures a Bot.
type Config struct {
	Token string
	// AllowedUsers are the Telegram user IDs that can talk to the agent. The bot ignores all other
	// users, because agent tools run on the host.
	AllowedUsers []int64
	APIURL       string // default https://api.telegram.org
}

// Validate reports a token that is not "<bot ID>:<secret>", and an empty allowlist.
func (c Config) Validate() error {
	id, secret, ok := strings.Cut(c.Token, ":")
	if _, err := strconv.ParseInt(id, 10, 64); err != nil || !ok || secret == "" {
		return errors.New("the bot token must look like 123456:ABC-DEF; get one from @BotFather")
	}
	if len(c.AllowedUsers) == 0 {
		return errors.New("add at least one allowed Telegram user ID")
	}
	return nil
}

// botID is the public part of the token. Two bots can deliver the same update ID.
func (c Config) botID() string {
	id, _, _ := strings.Cut(c.Token, ":")
	return id
}

// Submitter stores a message for an agent. *chat.Service implements it.
type Submitter interface {
	Submit(ctx context.Context, agentID, channelKey, sourceKey, content string) error
}

// Bot is one Telegram bot that talks to one agent.
type Bot struct {
	cfg     Config
	chats   Submitter
	agentID string
	client  *http.Client
	out     chan outgoing
	done    chan struct{} // closed when Run returns
}

type outgoing struct {
	chatID int64
	text   string
}

// New returns a Bot. Run starts it. Connect the chat service's notify to Deliver.
func New(cfg Config, chats Submitter, agentID string) *Bot {
	if cfg.APIURL == "" {
		cfg.APIURL = "https://api.telegram.org"
	}
	return &Bot{
		cfg:     cfg,
		chats:   chats,
		agentID: agentID,
		client:  &http.Client{Timeout: 90 * time.Second}, // above the 30 s long poll
		out:     make(chan outgoing, 64),
		done:    make(chan struct{}),
	}
}

// Run receives messages and sends replies until ctx is done.
func (b *Bot) Run(ctx context.Context) error {
	defer close(b.done)
	var wg sync.WaitGroup
	wg.Go(func() { b.send(ctx) })
	defer wg.Wait()

	// Telegram sends an update again until a getUpdates call with a higher offset confirms it. After
	// a restart the offset is 0, so the last batch comes again; Submit ignores the repeats.
	var offset int64
	for {
		var updates []update
		err := b.call(ctx, "getUpdates", map[string]any{
			"offset": offset, "timeout": 30, "allowed_updates": []string{"message"},
		}, &updates)
		for _, u := range updates {
			if err = b.receive(ctx, u); err != nil {
				break // retry this update on the next poll
			}
			offset = u.UpdateID + 1
		}
		if ctx.Err() != nil {
			return nil //nolint:nilerr // an error after cancel is the requested stop
		}
		if err != nil {
			slog.Error("telegram: receive", "err", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(5 * time.Second):
			}
		}
	}
}

// Deliver queues a reply for a Telegram channel and ignores other channels. It blocks only when
// 64 replies wait to be sent. After Run returns, it drops the reply.
func (b *Bot) Deliver(ctx context.Context, channelKey string, reply chat.Reply) {
	id, ok := strings.CutPrefix(channelKey, channelPrefix)
	if !ok {
		return
	}
	chatID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		slog.Error("telegram: bad channel key", "channel", channelKey)
		return
	}
	text := reply.Answer
	if reply.Status != "done" {
		text = "No answer: " + reply.Reason
	}
	if strings.TrimSpace(text) == "" {
		text = "(empty answer)"
	}
	select {
	case b.out <- outgoing{chatID: chatID, text: text}:
	case <-b.done:
		slog.Warn("telegram: the bot stopped; dropped a reply", "chat", chatID)
	case <-ctx.Done():
	}
}

func (b *Bot) receive(ctx context.Context, u update) error {
	m := u.Message
	if m == nil || m.From == nil || m.Text == "" {
		return nil
	}
	if !slices.Contains(b.cfg.AllowedUsers, m.From.ID) {
		slog.Warn("telegram: ignored a message from a user who is not allowed", "user", m.From.ID)
		return nil
	}
	// Best effort: the indicator shows for 5 s or until the reply arrives.
	_ = b.call(ctx, "sendChatAction", map[string]any{"chat_id": m.Chat.ID, "action": "typing"}, nil)
	return b.chats.Submit(ctx, b.agentID,
		channelPrefix+strconv.FormatInt(m.Chat.ID, 10),
		channelPrefix+b.cfg.botID()+":"+strconv.FormatInt(u.UpdateID, 10),
		m.Text)
}

// send sends queued replies in order. A reply that fails is logged and dropped.
func (b *Bot) send(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-b.out:
			for _, part := range split(msg.text, maxMessageLen) {
				err := b.call(ctx, "sendMessage", map[string]any{"chat_id": msg.chatID, "text": part}, nil)
				if err != nil {
					slog.Error("telegram: send reply", "chat", msg.chatID, "err", err)
					break
				}
			}
		}
	}
}

func (b *Bot) call(ctx context.Context, method string, params, result any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		b.cfg.APIURL+"/bot"+b.cfg.Token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram %s: bad request", method) // the error text would hold the token
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		// A *url.Error holds the URL, and the URL holds the token.
		if uerr, ok := errors.AsType[*url.Error](err); ok {
			err = uerr.Err
		}
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body
	var out struct {
		OK          bool           `json:"ok"`
		Result      jsontext.Value `json:"result"`
		Description string         `json:"description"`
	}
	if err := json.UnmarshalRead(resp.Body, &out); err != nil {
		return fmt.Errorf("telegram %s: HTTP %d: %w", method, resp.StatusCode, err)
	}
	if !out.OK {
		return fmt.Errorf("telegram %s: %s", method, out.Description)
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(out.Result, result)
}

// split cuts text into parts of at most limit UTF-16 code units, between runes.
func split(text string, limit int) []string {
	var parts []string
	start, n := 0, 0
	for i, r := range text {
		size := utf16.RuneLen(r)
		if size < 0 {
			size = 1 // invalid rune; Go encodes it as U+FFFD
		}
		if n+size > limit {
			parts = append(parts, text[start:i])
			start, n = i, 0
		}
		n += size
	}
	return append(parts, text[start:])
}

// Bot API types, with only the fields that dex reads.

type update struct {
	UpdateID int64    `json:"update_id"`
	Message  *message `json:"message"`
}

type message struct {
	From *struct {
		ID int64 `json:"id"`
	} `json:"from"`
	Chat struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	Text string `json:"text"`
}
