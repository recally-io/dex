package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/recally-io/dex/internal/channel"
)

func richContent(content *channel.RichMessage) map[string]any {
	return map[string]any{"html": content.HTML}
}

func (b *Bot) sendRich(ctx context.Context, peer, deliveryID string, index int, part channel.Part) (string, error) {
	if part.Rich.Version != 1 {
		return "", fmt.Errorf("%w: unsupported frozen rich version", channel.ErrPermanent)
	}
	chatID, topicID, err := parsePeer(peer)
	if err != nil {
		return "", err
	}
	var previewID int64
	if index == 0 {
		previewID = b.stopPreview(deliveryID)
	}
	method := "sendRichMessage"
	params := map[string]any{"chat_id": chatID, "rich_message": richContent(part.Rich)}
	if topicID > 0 {
		params["message_thread_id"] = topicID
	}
	if len(part.Buttons) > 0 {
		params["reply_markup"] = b.keyboard(part.Buttons)
	}
	if previewID != 0 {
		method = "editMessageText"
		params["message_id"] = previewID
	}
	b.presentation(params, method)
	var sent struct {
		ID int64 `json:"message_id"`
	}
	err = b.call(ctx, method, params, &sent)
	if api, ok := errors.AsType[*apiError](err); ok && api.Code == 400 && previewID != 0 {
		if strings.Contains(api.Description, "message is not modified") {
			sent.ID = previewID
			err = nil
		} else if strings.Contains(api.Description, "message to edit not found") {
			// A definite missing preview has no effect to duplicate. All other errors
			// keep the same method and payload; they do not select a text fallback.
			delete(params, "message_id")
			previewID = 0
			b.presentation(params, "sendRichMessage")
			err = b.call(ctx, "sendRichMessage", params, &sent)
		}
	}
	if err != nil {
		if api, ok := errors.AsType[*apiError](err); ok && (api.Code == 400 || api.Code == 403) {
			// A permanent rejection cannot own a final preview. Retain failed
			// cleanup for runtime shutdown; uncertain sends keep their preview.
			if previewID == 0 {
				b.forgetPreview(deliveryID)
			} else if cleanup := b.call(ctx, "deleteMessage", map[string]any{"chat_id": chatID, "message_id": previewID}, nil); cleanup == nil {
				b.forgetPreview(deliveryID)
			}
		}
		return "", err
	}
	if sent.ID <= 0 || previewID != 0 && sent.ID != previewID {
		return "", errors.New("telegram rich send outcome unknown: invalid message ID")
	}
	b.forgetPreview(deliveryID)
	return strconv.FormatInt(sent.ID, 10), nil
}
