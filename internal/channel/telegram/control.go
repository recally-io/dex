package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/engine"
)

// Control accepts only a remote ID selected by the channel service from an owned delivery.
func (b *Bot) Control(ctx context.Context, peer string, remote channel.RemoteMessage, args engine.TelegramRequest) (channel.Receipt, error) {
	chatID, err := peerID(peer)
	if err != nil {
		return channel.Receipt{}, err
	}
	id, err := strconv.ParseInt(remote.ID, 10, 64)
	if err != nil || id <= 0 {
		return channel.Receipt{}, fmt.Errorf("%w: invalid confirmed message ID", channel.ErrPermanent)
	}
	params := map[string]any{"chat_id": chatID, "message_id": id}
	method := ""
	switch args.Action {
	case "edit":
		if remote.Kind != "rich-v1" {
			return channel.Receipt{}, fmt.Errorf("%w: telegram text edit requires a rich message", channel.ErrPermanent)
		}
		method = "editMessageText"
		parts := renderRich(args.Text)
		if len(parts) != 1 {
			return channel.Receipt{}, fmt.Errorf("%w: rich edit must fit one message", channel.ErrPermanent)
		}
		params["rich_message"] = richContent(parts[0].Rich)
	case "edit_caption":
		method = "editMessageCaption"
		params["caption"] = args.Text
	case "delete":
		method = "deleteMessage"
	case "react":
		method = "setMessageReaction"
		params["reaction"] = []map[string]string{}
		if args.Emoji != "" {
			params["reaction"] = []map[string]string{{"type": "emoji", "emoji": args.Emoji}}
		}
	case "copy", "forward":
		method = "copyMessage"
		if args.Action == "forward" {
			method = "forwardMessage"
		}
		params["from_chat_id"] = chatID
	default:
		return channel.Receipt{}, fmt.Errorf("%w: unsupported message action", channel.ErrPermanent)
	}
	b.presentation(params, method)
	var result struct {
		ID int64 `json:"message_id"`
	}
	var completed bool
	var output any = &result
	if args.Action == "delete" || args.Action == "react" {
		output = &completed
	}
	err = b.call(ctx, method, params, output)
	noChange := false
	if api, ok := errors.AsType[*apiError](err); ok && api.Code == 400 && ((args.Action == "edit" || args.Action == "edit_caption") && strings.Contains(api.Description, "message is not modified") || args.Action == "delete" && strings.Contains(api.Description, "message to delete not found")) {
		err = nil
		noChange = true
	}
	if api, ok := errors.AsType[*apiError](err); ok && (api.Code == 400 || api.Code == 403) {
		err = fmt.Errorf("%w: %w", channel.ErrPermanent, err)
	}
	receipt := channel.Receipt{Kind: remote.Kind}
	if err == nil && !noChange && ((args.Action == "delete" || args.Action == "react") && !completed || (args.Action != "delete" && args.Action != "react") && result.ID <= 0) {
		return receipt, errors.New("telegram operation outcome unknown: invalid API result")
	}
	if err == nil && (args.Action == "copy" || args.Action == "forward") {
		receipt.MessageIDs = []string{strconv.FormatInt(result.ID, 10)}
	}
	return receipt, err
}

func (b *Bot) presentation(params map[string]any, method string) {
	if b.cfg.Options.Silent && (method == "sendRichMessage" || method == "copyMessage" || method == "forwardMessage") {
		params["disable_notification"] = true
	}
}
