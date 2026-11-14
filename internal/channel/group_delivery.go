package channel

import (
	"context"
	"errors"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// sendReplyPart confirms either the whole group or one fallback member at a time.
func (m *Manager) sendReplyPart(ctx context.Context, platform string, bot Bot, r sqlc.ListChannelRepliesRow, index int, part Part, remotes *[]string) error {
	if len(part.Group) == 0 || r.ChannelGroupFallback == 0 {
		receipt, err := m.sendPart(ctx, bot, r.Origin, r.ID, index, part)
		if !errors.Is(err, ErrGroupRejected) {
			if err != nil {
				return err
			}
			return m.confirmPart(ctx, r, receipt, remotes)
		}
		// Commit the mode before any fallback request. Unknown group outcomes never enter this path.
		if err := m.q.StartChannelGroupFallback(ctx, sqlc.StartChannelGroupFallbackParams{ID: r.ID, Part: int64(index)}); err != nil {
			return err
		}
	}
	for member := int(r.ChannelGroupCursor); member < len(part.Group); member++ {
		allowed, err := m.authorizedReply(ctx, platform, r)
		if err != nil {
			return err
		}
		if !allowed {
			return errors.New("destination no longer authorized during group fallback")
		}
		file := part.Group[member]
		receipt, err := m.sendPart(ctx, bot, r.Origin, r.ID, index, Part{File: &file})
		if err != nil {
			return err
		}
		// The last member and outer cursor commit together. Earlier members retain the group cursor.
		if err := m.confirmDelivery(ctx, r, receipt, remotes, member+1 < len(part.Group)); err != nil {
			return err
		}
	}
	return nil
}
