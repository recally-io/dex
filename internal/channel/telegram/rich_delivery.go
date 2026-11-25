package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/recally-io/dex/internal/channel"
)

func (b *Bot) presentation(params map[string]any, method string) {
	if b.cfg.Options.Silent && method == "sendRichMessage" {
		params["disable_notification"] = true
	}
}

func richContent(content *channel.RichMessage) map[string]any {
	return map[string]any{"html": content.HTML}
}

func (b *Bot) sendRich(ctx context.Context, peer string, part channel.Part) (string, error) {
	if part.Rich.Version != 1 {
		return "", fmt.Errorf("%w: unsupported frozen rich version", channel.ErrPermanent)
	}
	chatID, err := peerID(peer)
	if err != nil {
		return "", err
	}
	params := map[string]any{"chat_id": chatID, "rich_message": richContent(part.Rich)}
	if len(part.Buttons) > 0 {
		params["reply_markup"] = b.keyboard(part.Buttons)
	}
	b.presentation(params, "sendRichMessage")
	var sent struct {
		ID int64 `json:"message_id"`
	}
	if err := b.call(ctx, "sendRichMessage", params, &sent); err != nil {
		return "", err
	}
	if sent.ID <= 0 {
		return "", errors.New("telegram rich send outcome unknown: invalid message ID")
	}
	return strconv.FormatInt(sent.ID, 10), nil
}
