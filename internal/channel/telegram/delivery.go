package telegram

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/chat"
)

// Parts renders a final reply into independently valid HTML messages.
func (b *Bot) Parts(reply chat.Reply) []channel.Part {
	text := reply.Answer
	if reply.Status != "done" {
		text = "No answer: " + reply.Reason
	}
	if strings.TrimSpace(text) == "" {
		text = "(empty answer)"
	}
	parts := render(text, !reply.LegacyDelivery)
	if len(parts) == 0 {
		return channel.TextParts([]string{html.EscapeString(text)})
	}
	return channel.TextParts(parts)
}

func parsePeer(key string) (int64, int64, error) {
	remote, ok := strings.CutPrefix(key, channelPrefix)
	chat, topic, thread := strings.Cut(remote, ":")
	chatID, err := strconv.ParseInt(chat, 10, 64)
	if !ok || err != nil || chatID == 0 || strconv.FormatInt(chatID, 10) != chat {
		return 0, 0, fmt.Errorf("%w: invalid Telegram destination", channel.ErrPermanent)
	}
	var topicID int64
	if thread {
		topicID, err = strconv.ParseInt(topic, 10, 64)
		if chatID > 0 || err != nil || topicID <= 0 || strconv.FormatInt(topicID, 10) != topic {
			return 0, 0, fmt.Errorf("%w: invalid Telegram topic", channel.ErrPermanent)
		}
	}
	return chatID, topicID, nil
}

func peerID(key string) (int64, error) { chatID, _, err := parsePeer(key); return chatID, err }

// sendText replaces its preview and confirms one final part. The caller stores progress.
func (b *Bot) sendText(ctx context.Context, key, deliveryID string, index int, part string, buttons []channel.Button) (string, error) {
	chatID, topicID, err := parsePeer(key)
	if err != nil {
		return "", err
	}
	var previewID int64
	if index == 0 {
		previewID = b.stopPreview(deliveryID)
	}
	method := "sendMessage"
	params := map[string]any{"chat_id": chatID, "text": part, "parse_mode": "HTML"}
	if len(buttons) > 0 {
		params["reply_markup"] = b.keyboard(buttons)
	}
	if topicID > 0 {
		params["message_thread_id"] = topicID
	}
	if previewID != 0 {
		method = "editMessageText"
		params["message_id"] = previewID
	}
	b.presentation(params, method)
	var sent struct {
		ID int64 `json:"message_id"`
	}
	err = b.sendFormatted(ctx, method, params, part, &sent)
	if e, ok := errors.AsType[*apiError](err); ok && e.Code == 400 && previewID != 0 {
		// The owner can delete the preview while the agent runs.
		delete(params, "message_id")
		previewID = 0
		err = b.sendFormatted(ctx, "sendMessage", params, part, &sent)
	}
	// An edit keeps the message ID of the preview. The fallback text gets the ID that it returns.
	if previewID != 0 {
		sent.ID = previewID
	}
	if e, ok := errors.AsType[*apiError](err); ok && (e.Code == 400 || e.Code == 403) {
		b.forgetPreview(deliveryID)
		return "", fmt.Errorf("%w: %w", channel.ErrPermanent, err)
	}
	if err != nil {
		return "", err
	}
	b.forgetPreview(deliveryID)
	if sent.ID == 0 {
		return "", nil
	}
	return strconv.FormatInt(sent.ID, 10), nil
}

func (b *Bot) sendFormatted(ctx context.Context, method string, params map[string]any, text string, result any) error {
	err := b.call(ctx, method, params, result)
	if e, ok := errors.AsType[*apiError](err); ok && e.Code == 400 {
		if method == "editMessageText" && strings.Contains(e.Description, "message is not modified") {
			return nil
		}
		delete(params, "parse_mode")
		params["text"] = html.UnescapeString(htmlTags.ReplaceAllString(text, ""))
		err = b.call(ctx, method, params, result)
		if e, ok := errors.AsType[*apiError](err); ok && e.Code == 400 && method == "editMessageText" && strings.Contains(e.Description, "message is not modified") {
			return nil
		}
	}
	return err
}
