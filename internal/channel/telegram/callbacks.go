package telegram

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/chat"
)

type callbackQuery struct {
	ID      string   `json:"id"`
	From    *user    `json:"from"`
	Message *message `json:"message"`
	Data    string   `json:"data"`
}

// The opaque ID names an immutable server binding. The current bot token signs that reference.
// Message IDs are bound after send confirmation and are checked before the atomic claim.
func (b *Bot) signedCallback(id string) string {
	mac := hmac.New(sha256.New, []byte(b.cfg.Token))
	_, _ = mac.Write([]byte("dex-telegram-callback-v1:" + id))
	return "d1." + id + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:12])
}

func (b *Bot) callbackID(data string) (string, bool) {
	if len(data) > 64 {
		return "", false
	}
	prefix, rest, ok := strings.Cut(data, ".")
	if !ok || prefix != "d1" {
		return "", false
	}
	id, _, ok := strings.Cut(rest, ".")
	if !ok || !hmac.Equal([]byte(data), []byte(b.signedCallback(id))) {
		return "", false
	}
	return id, true
}

func (b *Bot) keyboard(buttons []channel.Button) map[string]any {
	rows := make([][]map[string]string, 0, len(buttons))
	for _, button := range buttons {
		rows = append(rows, []map[string]string{{"text": button.Text, "callback_data": b.signedCallback(button.Data)}})
	}
	return map[string]any{"inline_keyboard": rows}
}

func (b *Bot) receiveCallback(ctx context.Context, query *callbackQuery) error {
	if query.From == nil || query.From.IsBot || query.From.ID <= 0 || query.Message == nil || query.Message.ID <= 0 {
		return nil
	}
	m := query.Message
	botID, _ := strconv.ParseInt(b.cfg.botID(), 10, 64)
	if m.From == nil || m.From.ID != botID {
		return nil
	}
	accepted := false
	id, valid := b.callbackID(query.Data)
	if valid {
		identity := chat.ChannelContext{AccountID: b.cfg.botID(), SenderID: strconv.FormatInt(query.From.ID, 10), MessageID: strconv.FormatInt(m.ID, 10)}
		peer := channelPrefix + strconv.FormatInt(m.Chat.ID, 10)
		if m.Chat.Type == "group" || m.Chat.Type == "supergroup" {
			identity.ChatID = m.Chat.ID
			identity.TopicID = m.ThreadID
			identity.TopicShared = m.Chat.IsForum || m.IsTopic
			if m.ThreadID > 0 {
				peer += ":" + strconv.FormatInt(m.ThreadID, 10)
			}
		} else if m.Chat.Type != "private" {
			return nil
		}
		var err error
		accepted, err = b.chats.ClaimTelegramCallback(ctx, id, peer, strconv.FormatInt(m.ID, 10), identity)
		if err != nil {
			return err
		}
	}
	text := "Button expired, already used or not authorized. Open a new /menu."
	if accepted {
		text = "Accepted."
	}
	// Claim processing has finished. A definite rejection of this acknowledgement
	// must not hold the polling offset; network, 5xx and 429 errors still retry the update.
	err := b.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": query.ID, "text": text}, nil)
	if e, ok := errors.AsType[*apiError](err); ok && (e.Code == 400 || e.Code == 403) {
		slog.Warn("telegram: skipped a rejected callback acknowledgement", "err", err)
		return nil
	}
	return err
}
