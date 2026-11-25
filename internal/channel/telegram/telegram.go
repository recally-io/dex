// Package telegram connects Telegram chats to an agent through the Bot API.
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

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/speech"
	"github.com/recally-io/dex/internal/upload"
)

const channelPrefix = "telegram:"

// Config configures a Bot.
type Config struct {
	Options Options
	Webhook WebhookConfig
	Speech  speech.Resolve
	Token   string
	// CredentialRef is the stored key for a vault-backed token.
	CredentialRef string
	// AllowedUsers are the Telegram user IDs that can talk to the agent. The bot ignores all other
	// users, because agent tools run on the host.
	AllowedUsers []int64
	APIURL       string // default https://api.telegram.org
	// Pair consumes a code and returns the new allowlist, or nil for an invalid code.
	Pair    func(context.Context, string, int64) ([]int64, error)
	Uploads *upload.Store
	RetryAt int64
}

// Validate checks the token format and each allowed user ID.
func (c Config) Validate() error {
	if err := c.Options.Validate(); err != nil {
		return err
	}
	if err := c.Webhook.validate(); err != nil {
		return err
	}
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

type inbound interface {
	channel.WebhookInbox
	Receive(ctx context.Context, peer, source string, msg chat.Message) error
	QueueChannelNotice(ctx context.Context, peer, source string, identity chat.ChannelContext, body string) error
	ReceivedSource(ctx context.Context, source string) (bool, error)
	Source(ctx context.Context, source string) (chat.SourceReceipt, error)
	Report(name string, status channel.Status)
	TelegramInputAllowed(ctx context.Context, peer string, source chat.ChannelContext) (bool, error)
	ClaimTelegramCallback(ctx context.Context, id, peer, remote string, identity chat.ChannelContext) (bool, error)
	SetTelegramRetryAt(ctx context.Context, token string, until int64) error
	SpeechInput(ctx context.Context, source string) (*chat.Message, bool, error)
	Transcribe(ctx context.Context, peer, source string, msg chat.Message, duration int, options speech.Options, resolve speech.Resolve, store *upload.Store) (chat.Message, error)
	FinishSpeechInput(ctx context.Context, source string, album bool) error
	KnownTelegramAlbum(ctx context.Context, peer string, identity chat.ChannelContext) (bool, error)
	StoreTelegramAlbum(ctx context.Context, peer, source string, msg chat.Message) error
	ReadyTelegramAlbums(ctx context.Context) ([]chat.TelegramAlbum, time.Duration, error)
	FinishTelegramAlbum(ctx context.Context, source string) error
	CleanupTelegramAlbums(ctx context.Context, store *upload.Store) error
}

// Bot is one Telegram bot that talks to one agent.
type Bot struct {
	cfg       Config
	chats     inbound
	client    *http.Client
	typing    sync.WaitGroup
	rateMu    sync.Mutex
	retryAt   time.Time
	albumWake chan struct{}
	username  string
}

// New returns a Bot. Register it with the channel manager to start it.
func New(cfg Config, chats inbound) *Bot {
	if cfg.APIURL == "" {
		cfg.APIURL = "https://api.telegram.org"
	}
	client := &http.Client{Timeout: 90 * time.Second} // above the 30 s long poll
	if cfg.CredentialRef != "" {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return &Bot{
		cfg:       cfg,
		chats:     chats,
		client:    client,
		retryAt:   time.UnixMilli(cfg.RetryAt),
		albumWake: make(chan struct{}, 1),
	}
}

func (b *Bot) installCommands(ctx context.Context) {
	if err := b.call(ctx, "setMyCommands", map[string]any{"commands": []map[string]string{
		{"command": "menu", "description": "Open current conversation controls"},
		{"command": "status", "description": "Show current conversation status"},
		{"command": "settings", "description": "Show Telegram preferences"},
		{"command": "sessions", "description": "List direct children of this conversation"},
		{"command": "workflows", "description": "Resume saved child workflows"},
		{"command": "question", "description": "Show this conversation's open question"},
		{"command": "clear", "description": "Clear the current conversation context"},
		{"command": "stop", "description": "Stop the current conversation and its child work"},
		{"command": "feedback", "description": "Prepare a public GitHub issue draft in private"},
		{"command": "help", "description": "List commands"},
	}}, nil); err != nil && ctx.Err() == nil {
		slog.Warn("telegram: command menu", "err", err)
	}
}

// Run receives messages and sends replies until ctx is done.
func (b *Bot) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait(); b.typing.Wait() }()
	webhook := b.cfg.Webhook.URL != ""
	if webhook {
		// Restart can replay committed events even when registration is unavailable.
		config := b.cfg
		workers.Go(func() { b.registerWebhook(ctx, config) })
	} else {
		b.installCommands(ctx)
	}
	workers.Go(func() { b.collectAlbums(ctx) })
	if err := b.drainWebhookInbox(ctx); err != nil || webhook {
		return err
	}

	// Telegram sends an update again until a getUpdates call with a higher offset confirms it. After
	// a restart the offset is 0, so the last batch comes again; Receive ignores the repeats.
	var offset int64
	for {
		var updates []update
		err := b.call(ctx, "getUpdates", map[string]any{
			"offset": offset, "timeout": 30, "allowed_updates": inboundUpdateTypes(),
		}, &updates)
		for _, u := range updates {
			var handled bool
			handled, err = b.chats.HandledWebhook(ctx, "telegram", b.cfg.botID(), strconv.FormatInt(u.UpdateID, 10))
			if err == nil && !handled {
				err = b.receive(ctx, u)
			}
			if err != nil {
				break // retry this update on the next poll
			}
			offset = u.UpdateID + 1
		}
		if ctx.Err() != nil {
			return nil //nolint:nilerr // an error after cancel is the requested stop
		}
		if err == nil && b.chats != nil {
			b.chats.Report("telegram", channel.Status{State: "running"})
		}
		if err != nil {
			if b.chats != nil {
				b.chats.Report("telegram", channel.Status{State: "error", Reason: err.Error()})
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
	if u.Callback != nil {
		return b.receiveCallback(ctx, u.Callback)
	}
	m := u.Message
	edited := u.EditedMessage != nil
	if edited {
		m = u.EditedMessage
	}
	if m == nil || m.From == nil || !m.hasContent() {
		return nil
	}
	source := channelPrefix + b.cfg.botID() + ":" + strconv.FormatInt(u.UpdateID, 10)
	if m.Chat.Type != "private" {
		return nil
	}
	peer := channelPrefix + strconv.FormatInt(m.Chat.ID, 10)
	identity := chat.ChannelContext{AccountID: b.cfg.botID(), SenderID: strconv.FormatInt(m.From.ID, 10), MessageID: strconv.FormatInt(m.ID, 10), MediaGroup: m.MediaGroupID}
	if code, ok := strings.CutPrefix(m.Text, "/start "); ok && b.cfg.Pair != nil {
		if slices.Contains(b.cfg.AllowedUsers, m.From.ID) {
			return b.chats.QueueChannelNotice(ctx, peer, source, identity, "Telegram is already paired. Send a message to talk to dex.")
		}
		users, err := b.cfg.Pair(ctx, strings.TrimSpace(code), m.From.ID)
		if err != nil || users == nil {
			return err
		}
		b.cfg.AllowedUsers = users
		return b.chats.QueueChannelNotice(ctx, peer, source, identity, "Telegram is paired. Send a message to talk to dex. /help lists commands.")
	}
	if !slices.Contains(b.cfg.AllowedUsers, m.From.ID) {
		slog.Warn("telegram: ignored a message from a user who is not allowed", "user", m.From.ID)
		return nil
	}
	allowed, err := b.chats.TelegramInputAllowed(ctx, peer, identity)
	if err != nil {
		return err
	}
	if !allowed {
		return nil
	}
	// Dispatch from the original text before quote handling, uploads or speech.
	first, rest := channel.SplitCommand(m.body())
	name, bot, addressed := strings.Cut(first, "@")
	if name == channel.FeedbackCommand {
		if addressed {
			username, err := b.botUsername(ctx)
			if err != nil {
				return err
			}
			if username == "" || !strings.EqualFold(bot, username) {
				return nil
			}
		}
		input := channel.FeedbackCommand
		if m.Text != "" && m.ForwardOrigin == nil {
			input += " " + rest
		}
		identity.MediaGroup = ""
		return b.chats.Receive(ctx, peer, source, chat.Message{Content: input, ChannelContext: identity})
	}
	if selected, _ := m.selectedMedia(); selected != nil {
		received, err := b.chats.ReceivedSource(ctx, source)
		if err != nil {
			return err
		}
		if received {
			return b.chats.FinishSpeechInput(ctx, source, m.MediaGroupID != "")
		}
	}
	staged, handled, err := b.chats.SpeechInput(ctx, source)
	if err != nil {
		return err
	}
	if handled {
		return nil
	}
	var attachments []engine.Attachment
	if staged != nil {
		attachments = staged.Attachments
	} else {
		attachments, err = b.downloadTo(ctx, m, b.cfg.Uploads)
	}
	if err != nil {
		return b.rejectAttachment(ctx, peer, source, identity, err)
	}
	incoming := m.inbound(m.body(), edited, attachments, identity)
	content := incoming.Content
	if _, kind := m.selectedMedia(); kind != "" {
		incoming.ChannelContext.MediaKind = kind
	}
	if staged != nil {
		incoming = *staged
	}
	if (incoming.ChannelContext.MediaKind == "Voice" || incoming.ChannelContext.MediaKind == "Audio") && (b.cfg.Options.STT.Enabled || staged != nil) {
		selected, _ := m.selectedMedia()
		duration := 0
		if selected != nil {
			duration = selected.Duration
		}
		incoming, err = b.chats.Transcribe(ctx, peer, source, incoming, duration, b.cfg.Options.STT, b.cfg.Speech, b.cfg.Uploads)
		if err != nil {
			return err
		}
	}
	receiveErr := b.submitInput(ctx, peer, source, incoming, m.MediaGroupID != "" && len(attachments) > 0)
	if len(attachments) > 0 {
		// A committed receipt can own the files even when admission returns an error.
		receipt, lookupErr := b.chats.Source(context.WithoutCancel(ctx), source)
		if lookupErr == nil && receipt.ID == "" && !receipt.Staged {
			for _, attachment := range attachments {
				if err := b.cfg.Uploads.Remove(attachment.ID); err != nil {
					slog.Warn("telegram: upload cleanup failed", "err", err)
				}
			}
		}
		if err := errors.Join(receiveErr, lookupErr); err != nil {
			return err
		}
	} else if receiveErr != nil {
		return receiveErr
	}
	if err := b.chats.FinishSpeechInput(ctx, source, m.MediaGroupID != ""); err != nil {
		return err
	}
	if !channel.IsCommand(content) && m.MediaGroupID == "" {
		b.showTyping(ctx, source, m.Chat.ID)
	}
	return nil
}

type apiError struct {
	Code        int
	Description string
	RetryAfter  int
}

func (e *apiError) Error() string { return fmt.Sprintf("Telegram API %d: %s", e.Code, e.Description) }

func (b *Bot) call(ctx context.Context, method string, params, result any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return b.request(ctx, method, bytes.NewReader(body), "application/json", result)
}

func (b *Bot) request(ctx context.Context, method string, body io.Reader, contentType string, result any) error {
	if err := b.waitRate(ctx); err != nil {
		return err
	}
	return b.requestAPI(ctx, method, body, contentType, result, true)
}

func (b *Bot) requestAPI(ctx context.Context, method string, body io.Reader, contentType string, result any, recordFloodWait bool) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		b.cfg.APIURL+"/bot"+b.cfg.Token+"/"+method, body)
	if err != nil {
		return fmt.Errorf("telegram %s: bad request", method) // the error text would hold the token
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := b.client.Do(req)
	if err != nil {
		// A *url.Error holds the URL, and the URL holds the token.
		if uerr, ok := errors.AsType[*url.Error](err); ok {
			err = uerr.Err
		}
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body
	// A proxy or service failure cannot authorize a fallback, even if its JSON says 400.
	if resp.StatusCode >= http.StatusInternalServerError {
		return &apiError{Code: resp.StatusCode, Description: "service outcome unknown"}
	}
	var out struct {
		OK          bool           `json:"ok"`
		Result      jsontext.Value `json:"result"`
		Description string         `json:"description"`
		Code        int            `json:"error_code"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.UnmarshalRead(resp.Body, &out); err != nil {
		return fmt.Errorf("telegram %s: HTTP %d: %w", method, resp.StatusCode, err)
	}
	if !out.OK {
		if recordFloodWait && out.Code == 429 && out.Parameters.RetryAfter > 0 {
			b.rateMu.Lock()
			until := time.Now().Add(time.Duration(out.Parameters.RetryAfter) * time.Second)
			if until.After(b.retryAt) {
				b.retryAt = until
			}
			b.rateMu.Unlock()
			if b.chats != nil {
				key := b.cfg.Token
				if b.cfg.CredentialRef != "" {
					key = b.cfg.CredentialRef
				}
				if err := b.chats.SetTelegramRetryAt(ctx, key, until.UnixMilli()); err != nil {
					return errors.Join(&apiError{Code: out.Code, Description: "Telegram flood control", RetryAfter: out.Parameters.RetryAfter}, fmt.Errorf("store Telegram retry window: %w", err))
				}
			}
		}
		return &apiError{Code: out.Code, Description: strings.ReplaceAll(out.Description, b.cfg.Token, "[redacted]"), RetryAfter: out.Parameters.RetryAfter}
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return &apiError{Code: resp.StatusCode, Description: "unexpected HTTP status"}
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(out.Result, result)
}

// Bot API types, with only the fields that dex reads.

type update struct {
	Callback      *callbackQuery `json:"callback_query"`
	UpdateID      int64          `json:"update_id"`
	Message       *message       `json:"message"`
	EditedMessage *message       `json:"edited_message"`
}

type user struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
	IsBot     bool   `json:"is_bot"`
}

type remoteChat struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

type message struct {
	Rich    jsontext.Value `json:"rich_message,omitempty"`
	ID      int64          `json:"message_id"`
	From    *user          `json:"from"`
	Chat    remoteChat     `json:"chat"`
	ReplyTo *message       `json:"reply_to_message"`
	Quote   *struct {
		Text string `json:"text"`
	} `json:"quote"`
	ForwardOrigin *struct {
		Type           string      `json:"type"`
		SenderUser     *user       `json:"sender_user"`
		SenderUserName string      `json:"sender_user_name"`
		Chat           *remoteChat `json:"chat"`
	} `json:"forward_origin"`
	MediaGroupID string  `json:"media_group_id"`
	Text         string  `json:"text"`
	Caption      string  `json:"caption"`
	Photo        []media `json:"photo"`
	Document     *media  `json:"document"`
	Voice        *media  `json:"voice"`
	Audio        *media  `json:"audio"`
	Video        *media  `json:"video"`
	Animation    *media  `json:"animation"`
	VideoNote    *media  `json:"video_note"`
	Sticker      *media  `json:"sticker"`
	Location     *struct {
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	} `json:"location"`
	Contact *struct {
		Phone     string `json:"phone_number"`
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
	} `json:"contact"`
	Poll *struct {
		Question string `json:"question"`
		Options  []struct {
			Text  string `json:"text"`
			Votes int    `json:"voter_count"`
		} `json:"options"`
	} `json:"poll"`
}

func (b *Bot) botUsername(ctx context.Context) (string, error) {
	if b.username == "" {
		var me user
		if err := b.call(ctx, "getMe", map[string]any{}, &me); err != nil {
			return "", err
		}
		b.username = me.Username
	}
	return b.username, nil
}
