package telegram

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/recally-io/dex/internal/channel"
)

// WebhookConfig contains the public endpoint and its write-only secret.
type WebhookConfig struct {
	URL         string `json:"url"`
	SecretToken string `json:"secretToken"`
}

var webhookSecretPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

func (c WebhookConfig) validate() error {
	if c == (WebhookConfig{}) {
		return nil
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.ForceQuery {
		return errors.New("enter a public HTTPS webhook URL without credentials, a query, or a fragment")
	}
	if !webhookSecretPattern.MatchString(c.SecretToken) {
		return errors.New("the Telegram webhook secret must contain 1 to 256 letters, digits, hyphens, or underscores")
	}
	return nil
}

var _ channel.WebhookReceiver = (*Bot)(nil)

// InboundWebhook returns nil when this integration uses long polling.
func (b *Bot) InboundWebhook() *channel.Webhook {
	config := b.cfg.Webhook
	if config.URL == "" {
		return nil
	}
	return &channel.Webhook{Account: b.cfg.botID(), Decode: func(header http.Header, body []byte) (channel.WebhookEvent, int) {
		if !channel.MatchWebhookSecret(header.Get("X-Telegram-Bot-Api-Secret-Token"), config.SecretToken) {
			return channel.WebhookEvent{}, http.StatusUnauthorized
		}
		var u update
		if err := json.Unmarshal(body, &u); err != nil || u.UpdateID <= 0 {
			return channel.WebhookEvent{}, http.StatusBadRequest
		}
		return channel.WebhookEvent{ID: strconv.FormatInt(u.UpdateID, 10), Payload: body}, http.StatusOK
	}}
}

func (b *Bot) registerWebhook(ctx context.Context, cfg Config) {
	params := map[string]any{
		"url": cfg.Webhook.URL, "secret_token": cfg.Webhook.SecretToken,
		"allowed_updates": inboundUpdateTypes(), "drop_pending_updates": false, "max_connections": 1,
	}
	for ctx.Err() == nil {
		err := changeWebhook(ctx, cfg, "setWebhook", params)
		if err == nil {
			b.chats.Report("telegram", channel.Status{State: "running"})
			b.installCommands(ctx)
			return
		}
		if ctx.Err() == nil {
			b.chats.Report("telegram", channel.Status{State: "error", Reason: err.Error()})
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
}

// DeleteWebhook removes platform registration before polling resumes. It keeps
// pending updates at Telegram. The caller must stop the old local receiver first.
func DeleteWebhook(ctx context.Context, cfg Config) error {
	return changeWebhook(ctx, cfg, "deleteWebhook", map[string]bool{"drop_pending_updates": false})
}

func changeWebhook(ctx context.Context, cfg Config, method string, params any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	b := New(cfg, nil)
	b.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	var confirmed bool
	err := b.call(ctx, method, params, &confirmed)
	if apiErr, ok := errors.AsType[*apiError](err); ok {
		return fmt.Errorf("telegram %s did not succeed (code %d)", method, apiErr.Code)
	}
	if err != nil || !confirmed {
		// The platform response and its errors can echo the callback URL or secret.
		return fmt.Errorf("telegram %s did not confirm the request", method)
	}
	return nil
}

func inboundUpdateTypes() []string { return []string{"message", "edited_message", "callback_query"} }

func (b *Bot) drainWebhookInbox(ctx context.Context) error {
	return b.chats.DrainWebhookInbox(ctx, "telegram", b.cfg.botID(), b.cfg.Webhook.URL != "", func(ctx context.Context, payload []byte) error {
		var u update
		if err := json.Unmarshal(payload, &u); err != nil {
			return errors.New("decode stored Telegram update")
		}
		return b.receive(ctx, u)
	})
}
