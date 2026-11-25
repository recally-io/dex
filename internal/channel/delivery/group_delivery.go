package delivery

import (
	"context"
	"errors"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/db/sqlc"
)

// sendReplyPart confirms either the whole group or one fallback member at a time.
func (m *Manager) sendReplyPart(ctx context.Context, platform string, bot channel.Bot, r sqlc.ListPendingOutboxMessagesRow, index int, part channel.Part, remotes *[]channel.RemoteMessage) error {
	var beforeSend func() error
	if isFinalFollowUp(r) && (part.File != nil || len(part.Group) != 0) {
		// Reading attachment snapshots can block. Recheck after that work,
		// immediately before handing the request to the platform adapter.
		beforeSend = func() error {
			allowed, err := m.authorizeOutboxPart(ctx, platform, r)
			if err == nil && !allowed {
				err = errFollowUpNotAuthorized
			}
			return err
		}
	}
	if len(part.Group) == 0 || r.GroupFallback == 0 {
		receipt, err := m.sendPart(ctx, bot, r.Peer, r.ID, index, part, false, beforeSend)
		if !errors.Is(err, channel.ErrGroupRejected) {
			if err != nil {
				return err
			}
			return m.confirmDelivery(ctx, r, receipt, remotes, false)
		}
		// Commit the mode before any fallback request. Unknown group outcomes never enter this path.
		if err := m.q.StartOutboxGroupFallback(ctx, sqlc.StartOutboxGroupFallbackParams{ID: r.ID, Part: int64(index)}); err != nil {
			return err
		}
		if source := legacyReplySource(r); source.Valid {
			if err := m.q.StartChannelGroupFallback(ctx, sqlc.StartChannelGroupFallbackParams{ID: source.String, Part: int64(index)}); err != nil {
				return err
			}
		}
	}
	for member := int(r.GroupCursor); member < len(part.Group); member++ {
		allowed, err := m.authorizeOutboxPart(ctx, platform, r)
		if err != nil {
			return err
		}
		if !allowed {
			if isFinalFollowUp(r) {
				return errFollowUpNotAuthorized
			}
			return errors.New("destination no longer authorized during group fallback")
		}
		file := part.Group[member]
		receipt, err := m.sendPart(ctx, bot, r.Peer, r.ID, index, channel.Part{File: &file}, false, beforeSend)
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
