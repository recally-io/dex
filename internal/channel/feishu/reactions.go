package feishu

import (
	"context"
	"errors"
	"strings"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/recally-io/dex/internal/channel"
)

func (b *Bot) ReactionsEnabled(peer string) bool {
	return strings.HasPrefix(peer, "feishu:") && (b.cfg.Reactions == nil || *b.cfg.Reactions)
}

func (b *Bot) React(ctx context.Context, _ string, messageID, previous string, status channel.Reaction) (string, error) {
	emoji := map[channel.Reaction]string{
		channel.ReactionQueued: "OneSecond", channel.ReactionProcessing: "OnIt", channel.ReactionSucceeded: "DONE",
		channel.ReactionFailed: "CrossMark", channel.ReactionCancelled: "MinusOne",
	}[status]
	if emoji == "" || messageID == "" {
		return previous, errors.New("invalid feishu status reaction")
	}
	// Use a separate token cache and do not refresh/retry the mutation. Token retrieval
	// can block other reactions, but never the main bot's token, intake or sends.
	token, err := b.reactions.tenantToken(ctx)
	if err != nil {
		return previous, err
	}
	opt := larkcore.WithTenantAccessToken(token)
	if previous != "" {
		req := larkim.NewDeleteMessageReactionReqBuilder().MessageId(messageID).ReactionId(previous).Build()
		if _, err := rest(ctx, b.reactions, func(ctx context.Context) (*larkim.DeleteMessageReactionResp, error) {
			return b.reactions.im.MessageReaction.Delete(ctx, req, opt)
		}); err != nil {
			return previous, reactionError(err)
		}
	}
	req := larkim.NewCreateMessageReactionReqBuilder().MessageId(messageID).Body(&larkim.CreateMessageReactionReqBody{
		ReactionType: &larkim.Emoji{EmojiType: new(emoji)},
	}).Build()
	resp, err := rest(ctx, b.reactions, func(ctx context.Context) (*larkim.CreateMessageReactionResp, error) {
		return b.reactions.im.MessageReaction.Create(ctx, req, opt)
	})
	if err != nil {
		return "", reactionError(err)
	}
	if resp.Data == nil || resp.Data.ReactionId == nil || *resp.Data.ReactionId == "" {
		return "", channel.ErrReactionUnknown
	}
	return *resp.Data.ReactionId, nil
}

func reactionError(err error) error {
	if api, ok := errors.AsType[*apiError](err); ok && api.Status >= 200 && api.Status < 500 && api.Code != 0 {
		return err
	}
	return channel.ErrReactionUnknown
}
