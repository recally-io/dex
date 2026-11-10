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
func (b *Bot) Parts(reply chat.Reply) []string {
	text := reply.Answer
	if reply.Status != "done" {
		text = "No answer: " + reply.Reason
	}
	if strings.TrimSpace(text) == "" {
		text = "(empty answer)"
	}
	parts := render(text)
	if len(parts) == 0 {
		return []string{html.EscapeString(text)}
	}
	return parts
}

// Send replaces its preview and confirms one final part. The caller stores progress.
func (b *Bot) Send(ctx context.Context, key, deliveryID string, index int, part string) (string, error) {
	id, ok := strings.CutPrefix(key, channelPrefix)
	if !ok {
		return "", errors.New("telegram destination is invalid")
	}
	id, _, _ = strings.Cut(id, "/")
	chatID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return "", errors.New("telegram destination is invalid")
	}
	var previewID int64
	if index == 0 {
		previewID = b.stopPreview(deliveryID)
	}
	method := "sendMessage"
	params := map[string]any{"chat_id": chatID, "text": part, "parse_mode": "HTML"}
	if previewID != 0 {
		method = "editMessageText"
		params["message_id"] = previewID
	}
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
