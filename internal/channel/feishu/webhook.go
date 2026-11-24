package feishu

import (
	"encoding/json/v2"
	"errors"
	"net/http"
	"strconv"
	"time"

	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/recally-io/dex/internal/channel"
)

// WebhookConfig contains the write-only credentials from Event Subscriptions.
type WebhookConfig struct {
	VerificationToken string `json:"verificationToken"`
	EncryptKey        string `json:"encryptKey,omitempty"`
}

// InboundWebhook returns nil when this app uses a WebSocket.
func (b *Bot) InboundWebhook() *channel.Webhook {
	config, appID := b.cfg.Webhook, b.cfg.AppID
	if config.VerificationToken == "" {
		return nil
	}
	return &channel.Webhook{Account: appID, Decode: func(headers http.Header, body []byte) (channel.WebhookEvent, int) {
		var signed bool
		requestTime := headers.Get(larkevent.EventRequestTimestamp)
		if config.EncryptKey != "" {
			signature := headers.Get(larkevent.EventSignature)
			if signature != "" {
				nonce := headers.Get(larkevent.EventRequestNonce)
				if requestTime == "" || nonce == "" || !channel.MatchWebhookSecret(signature, larkevent.Signature(requestTime, nonce, config.EncryptKey, string(body))) {
					return channel.WebhookEvent{}, http.StatusUnauthorized
				}
				signed = true
			}
			var envelope larkevent.EventEncryptMsg
			if err := json.Unmarshal(body, &envelope); err != nil || envelope.Encrypt == "" {
				return channel.WebhookEvent{}, http.StatusUnauthorized
			}
			var err error
			body, err = decryptWebhook(envelope.Encrypt, config.EncryptKey)
			if err != nil {
				return channel.WebhookEvent{}, http.StatusUnauthorized
			}
		}
		var event struct {
			Type      string `json:"type"`
			Challenge string `json:"challenge"`
			Token     string `json:"token"`
			Header    struct {
				ID         string `json:"event_id"`
				AppID      string `json:"app_id"`
				Token      string `json:"token"`
				CreateTime string `json:"create_time"`
			} `json:"header"`
		}
		err := json.Unmarshal(body, &event)
		// Feishu can send URL verification without a signature, even in encrypted mode.
		if err == nil && event.Type == string(larkevent.ReqTypeChallenge) {
			if event.Challenge == "" || !channel.MatchWebhookSecret(event.Token, config.VerificationToken) {
				return channel.WebhookEvent{}, http.StatusUnauthorized
			}
			return channel.WebhookEvent{Challenge: event.Challenge}, http.StatusOK
		}
		if config.EncryptKey != "" && !signed {
			return channel.WebhookEvent{}, http.StatusUnauthorized
		}
		if err != nil || event.Header.ID == "" {
			return channel.WebhookEvent{}, http.StatusBadRequest
		}
		if event.Header.AppID != appID || (!signed && !channel.MatchWebhookSecret(event.Header.Token, config.VerificationToken)) {
			return channel.WebhookEvent{}, http.StatusUnauthorized
		}
		now := time.Now()
		if signed {
			seconds, err := strconv.ParseInt(requestTime, 10, 64)
			if err != nil || !freshWebhookTime(time.Unix(seconds, 0), now) {
				return channel.WebhookEvent{}, http.StatusBadRequest
			}
		}
		// A new signature on a retry must not make an old event fresh again.
		if !signed || event.Header.CreateTime != "" {
			millis, err := strconv.ParseInt(event.Header.CreateTime, 10, 64)
			if err != nil || !freshWebhookTime(time.UnixMilli(millis), now) {
				return channel.WebhookEvent{}, http.StatusBadRequest
			}
		}
		return channel.WebhookEvent{ID: event.Header.ID, Payload: body}, http.StatusOK
	}}
}

func freshWebhookTime(platformTime, now time.Time) bool {
	// Future timestamps could outlive the deduplication row.
	return !platformTime.Before(now.Add(-channel.WebhookRetention)) && !platformTime.After(now)
}

func decryptWebhook(ciphertext, key string) (body []byte, err error) {
	// The SDK can panic when malformed plaintext has a closing brace before
	// its opening brace. Unsigned verification requests can reach this helper.
	defer func() {
		if recover() != nil {
			err = errors.New("invalid Feishu ciphertext")
		}
	}()
	return larkevent.EventDecrypt(ciphertext, key)
}
