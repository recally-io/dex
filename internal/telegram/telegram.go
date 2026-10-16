// Package telegram connects Telegram chats to an agent. It reads messages from the Bot API with long
// polling and sends replies back. /new clears the main timeline's context.
package telegram

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/upload"
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
	// Pair consumes a code and returns the new allowlist, or nil for an invalid code.
	Pair func(context.Context, string, int64) ([]int64, error)
	// Seen checks durable acceptance before the bot downloads a repeated file.
	Seen    func(context.Context, string) (bool, error)
	Uploads *upload.Store
	// Watch returns the submission ID and its SSE reader. A settled submission has no reader.
	Watch  func(context.Context, string) (string, io.ReadCloser, error)
	Report func(state, reason string)
}

// Validate checks the token format and each allowed user ID.
func (c Config) Validate() error {
	id, secret, ok := strings.Cut(c.Token, ":")
	if _, err := strconv.ParseInt(id, 10, 64); err != nil || !ok || secret == "" {
		return errors.New("the bot token must look like 123456:ABC-DEF; get one from @BotFather")
	}
	for i, id := range c.AllowedUsers {
		if id <= 0 || slices.Contains(c.AllowedUsers[:i], id) {
			return errors.New("allowed Telegram user IDs must be positive and unique")
		}
	}
	return nil
}

// botID is the public part of the token. Two bots can deliver the same update ID.
func (c Config) botID() string {
	id, _, _ := strings.Cut(c.Token, ":")
	return id
}

// Bot is one Telegram bot that talks to one agent.
type Bot struct {
	cfg       Config
	chats     *channel.Manager
	client    *http.Client
	previewMu sync.Mutex
	previews  map[string]*preview
}

// New returns a Bot. Register it with the channel manager to start it.
func New(cfg Config, chats *channel.Manager) *Bot {
	if cfg.APIURL == "" {
		cfg.APIURL = "https://api.telegram.org"
	}
	return &Bot{
		cfg:      cfg,
		chats:    chats,
		client:   &http.Client{Timeout: 90 * time.Second}, // above the 30 s long poll
		previews: map[string]*preview{},
	}
}

// Run receives messages and sends replies until ctx is done.
func (b *Bot) Run(ctx context.Context) error {
	if err := b.call(ctx, "setMyCommands", map[string]any{"commands": []map[string]string{
		{"command": "new", "description": "Clear the main context"},
		{"command": "stop", "description": "Stop the main answer and its background work"},
		{"command": "help", "description": "List commands"},
	}}, nil); err != nil {
		slog.Warn("telegram: command menu", "err", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer func() { cancel(); b.closePreviews() }()

	// Telegram sends an update again until a getUpdates call with a higher offset confirms it. After
	// a restart the offset is 0, so the last batch comes again; Receive ignores the repeats.
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
		if err == nil && b.cfg.Report != nil {
			b.cfg.Report("running", "")
		}
		if err != nil {
			if b.cfg.Report != nil {
				b.cfg.Report("error", err.Error())
			}
			slog.Error("telegram: receive", "err", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(5 * time.Second):
			}
		}
	}
}

func (b *Bot) receive(ctx context.Context, u update) error {
	m := u.Message
	if m == nil || m.From == nil || (m.Text == "" && m.Caption == "" && len(m.Photo) == 0 && m.Document == nil) {
		return nil
	}
	if m.Chat.Type != "private" {
		return nil
	}
	if code, ok := strings.CutPrefix(m.Text, "/start "); ok && b.cfg.Pair != nil {
		if slices.Contains(b.cfg.AllowedUsers, m.From.ID) {
			return b.controlReply(ctx, m.Chat.ID, "Telegram is already paired. Send a message to talk to dex.")
		}
		users, err := b.cfg.Pair(ctx, strings.TrimSpace(code), m.From.ID)
		if err != nil {
			return err
		}
		response := "This pairing code is invalid or expired. Create a new link in dex."
		if users != nil {
			b.cfg.AllowedUsers = users
			response = "Telegram is paired. Send a message to talk to dex. /help lists commands."
		}
		return b.controlReply(ctx, m.Chat.ID, response)
	}
	if !slices.Contains(b.cfg.AllowedUsers, m.From.ID) {
		slog.Warn("telegram: ignored a message from a user who is not allowed", "user", m.From.ID)
		return nil
	}
	source := channelPrefix + b.cfg.botID() + ":" + strconv.FormatInt(u.UpdateID, 10)
	if (m.Document != nil || len(m.Photo) > 0) && b.cfg.Seen != nil {
		seen, err := b.cfg.Seen(ctx, source)
		if err != nil {
			return err
		}
		if seen {
			return nil
		}
	}
	attachments, err := b.download(ctx, m)
	if err != nil {
		permanent := errors.Is(err, upload.ErrTooLarge) || errors.Is(err, upload.ErrInvalidName)
		if e, ok := errors.AsType[*apiError](err); ok && e.Code == 400 {
			permanent = true
		}
		if !permanent {
			return err
		}
		return b.controlReply(ctx, m.Chat.ID, "Cannot read this attachment. Use a photo or document under 20 MB.")
	}
	content := m.Text
	if content == "" {
		content = m.Caption
	}
	peer := channelPrefix + strconv.FormatInt(m.Chat.ID, 10)
	if err = b.chats.Receive(ctx, peer, source, chat.Message{Content: content, Attachments: attachments}); err != nil {
		return err
	}
	if content != "/new" && content != "/stop" && content != "/help" {
		b.startPreview(ctx, source, m.Chat.ID)
	}
	return nil
}

type apiError struct {
	Code        int
	Description string
}

func (e *apiError) Error() string { return fmt.Sprintf("Telegram API %d: %s", e.Code, e.Description) }

// controlReply allows polling to pass a permanently rejected acknowledgement.
func (b *Bot) controlReply(ctx context.Context, chatID int64, text string) error {
	err := b.call(ctx, "sendMessage", map[string]any{"chat_id": chatID, "text": text}, nil)
	if e, ok := errors.AsType[*apiError](err); ok && (e.Code == 400 || e.Code == 403) {
		slog.Warn("telegram: skipped a rejected control reply", "err", err)
		return nil
	}
	return err
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
		Code        int            `json:"error_code"`
	}
	if err := json.UnmarshalRead(resp.Body, &out); err != nil {
		return fmt.Errorf("telegram %s: HTTP %d: %w", method, resp.StatusCode, err)
	}
	if !out.OK {
		return &apiError{Code: out.Code, Description: strings.ReplaceAll(out.Description, b.cfg.Token, "[redacted]")}
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
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	Text     string  `json:"text"`
	Caption  string  `json:"caption"`
	Photo    []media `json:"photo"`
	Document *media  `json:"document"`
}
