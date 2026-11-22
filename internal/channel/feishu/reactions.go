package feishu

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/url"
	"strings"

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
	path := "/open-apis/im/v1/messages/" + url.PathEscape(messageID) + "/reactions"
	if previous != "" {
		if _, err := b.reactions.request(ctx, http.MethodDelete, path+"/"+url.PathEscape(previous), nil, token); err != nil {
			return previous, reactionError(err)
		}
	}
	data, err := b.reactions.request(ctx, http.MethodPost, path, map[string]any{"reaction_type": map[string]string{"emoji_type": emoji}}, token)
	if err != nil {
		return "", reactionError(err)
	}
	var result struct {
		Data struct {
			ID string `json:"reaction_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil || result.Data.ID == "" {
		return "", channel.ErrReactionUnknown
	}
	return result.Data.ID, nil
}

func reactionError(err error) error {
	if api, ok := errors.AsType[*apiError](err); ok && api.Status >= 200 && api.Status < 500 && api.Code != 0 {
		return err
	}
	return channel.ErrReactionUnknown
}
