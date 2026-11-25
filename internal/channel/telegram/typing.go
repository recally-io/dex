package telegram

import (
	"context"
	"time"
)

func (b *Bot) showTyping(ctx context.Context, source string, chatID int64) {
	if b.cfg.Options.Typing != nil && !*b.cfg.Options.Typing {
		return
	}
	b.typing.Go(func() {
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()
		for {
			receipt, err := b.chats.Source(ctx, source)
			if err != nil || !receipt.Pending {
				return
			}
			_ = b.call(ctx, "sendChatAction", map[string]any{"chat_id": chatID, "action": "typing"}, nil)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}
