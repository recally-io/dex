package policy

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/recally-io/dex/internal/channel/telegram"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/session"
	"github.com/recally-io/dex/internal/upload"
)

func telegramAlbumKey(peer string, identity session.ChannelContext) (string, error) {
	parts := []string{peer, identity.SenderID, identity.MediaGroup}
	key, err := json.Marshal(parts)
	return "telegram:" + identity.AccountID + ":album:" + base64.RawURLEncoding.EncodeToString(key), err
}

// StoreTelegramAlbum commits upload ownership before the transport may ACK the member.
func (p *Policy) StoreTelegramAlbum(ctx context.Context, peer, source string, msg session.Message) error {
	identity := msg.ChannelContext
	actor, err := strconv.ParseInt(identity.SenderID, 10, 64)
	if !PrivatePeer(peer) || err != nil || actor <= 0 || identity.AccountID == "" || identity.MediaGroup == "" || len(msg.Attachments) != 1 {
		return errors.New("invalid Telegram album member")
	}
	group, err := telegramAlbumKey(peer, identity)
	if err != nil {
		return err
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	tx, q, err := p.beginAlbumWrite(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Both receipts are global, including after a credential move.
	if id, err := session.SubmissionSourceIn(ctx, q, source); err != nil || id != "" {
		return err
	}
	if _, err := q.GetTelegramAlbumMember(ctx, source); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err := p.RequirePairedChannel(ctx, q, peer, identity); err != nil {
		return err
	}
	n, err := q.StoreTelegramAlbumMember(ctx, sqlc.StoreTelegramAlbumMemberParams{
		BotID: p.botID, SourceKey: source, AccountID: identity.AccountID, SenderID: identity.SenderID,
		Peer: peer, GroupKey: group, InputJson: string(data), ReceivedAt: time.Now().UnixMilli(),
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("album member authorization changed")
	}
	return tx.Commit()
}

// beginAlbumWrite takes the SQLite writer before reading so pairing changes,
// duplicate checks and album writes use the same state.
func (p *Policy) beginAlbumWrite(ctx context.Context) (*sql.Tx, *sqlc.Queries, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	q := p.q.WithTx(tx)
	if err := q.LockChannelWriter(ctx); err != nil {
		return nil, nil, errors.Join(err, tx.Rollback())
	}
	return tx, q, nil
}

// ReadyTelegramAlbums freezes each batch before delivery. Later members cannot change its payload.
func (p *Policy) ReadyTelegramAlbums(ctx context.Context) ([]telegram.Album, time.Duration, error) {
	tx, q, err := p.beginAlbumWrite(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	groups, err := q.ListCollectingTelegramAlbums(ctx, p.botID)
	if err != nil {
		return nil, 0, err
	}
	now := time.Now().UnixMilli()
	delay := time.Hour
	for _, group := range groups {
		due := min(group.FirstAt+3000, group.LastAt+800)
		if group.Members >= 10 {
			due = now
		}
		if due > now {
			delay = min(delay, time.Duration(due-now)*time.Millisecond)
			continue
		}
		members, err := q.ListUnsealedTelegramAlbumMembers(ctx, sqlc.ListUnsealedTelegramAlbumMembersParams{BotID: p.botID, GroupKey: group.GroupKey})
		if err != nil {
			return nil, 0, err
		}
		sources := make([]string, len(members))
		for i, member := range members {
			sources[i] = member.SourceKey
		}
		encoded, err := json.Marshal(sources)
		if err != nil {
			return nil, 0, err
		}
		batch := group.GroupKey + ":batch:" + members[0].SourceKey
		if err := q.SealTelegramAlbumMembers(ctx, sqlc.SealTelegramAlbumMembersParams{BotID: p.botID, BatchSource: batch, Sources: string(encoded)}); err != nil {
			return nil, 0, err
		}
		if group.Members > 10 {
			delay = 0
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	batches, err := p.q.ListSealedTelegramAlbums(ctx, p.botID)
	if err != nil {
		return nil, 0, err
	}
	ready := make([]telegram.Album, 0, len(batches))
	for _, source := range batches {
		members, err := p.q.ListTelegramAlbumBatchMembers(ctx, sqlc.ListTelegramAlbumBatchMembersParams{BotID: p.botID, BatchSource: source})
		if err != nil {
			return nil, 0, err
		}
		// Settings can discard a sealed batch after the list query.
		if len(members) == 0 {
			continue
		}
		var msg session.Message
		var texts, notes []string
		for i, member := range members {
			var input session.Message
			if err := json.Unmarshal([]byte(member.InputJson), &input); err != nil {
				return nil, 0, err
			}
			if i == 0 {
				msg = input
				msg.Attachments = nil
			}
			msg.Attachments = append(msg.Attachments, input.Attachments...)
			if input.Content != "" {
				texts = append(texts, input.Content)
			}
			if input.ChannelNote != "" {
				notes = append(notes, input.ChannelNote)
			}
		}
		late, err := p.q.HasEarlierTelegramAlbumBatch(ctx, sqlc.HasEarlierTelegramAlbumBatchParams{BotID: p.botID, GroupKey: members[0].GroupKey, BatchSource: source})
		if err != nil {
			return nil, 0, err
		}
		label := fmt.Sprintf("Telegram album: %d files.", len(msg.Attachments))
		if late {
			label = "Late album continuation. " + label
		}
		msg.AlbumSource = source
		msg.Content = strings.Join(texts, "\n\n")
		msg.ChannelNote = strings.Join(append([]string{label}, notes...), "\n\n")
		ready = append(ready, telegram.Album{Source: source, Peer: members[0].Peer, Message: msg})
	}
	return ready, delay, nil
}

// FinishTelegramAlbum transfers upload ownership to the stored business input.
func (p *Policy) FinishTelegramAlbum(ctx context.Context, source string) error {
	receipt, err := p.sessions.LookupSource(ctx, p.botID, source)
	if err != nil {
		return err
	}
	if receipt.ID == "" {
		return errors.New("album input has no business receipt")
	}
	return p.q.TransferTelegramAlbumMembers(ctx, sqlc.TransferTelegramAlbumMembersParams{BotID: p.botID, BatchSource: source})
}

// CleanupTelegramAlbums deletes only uploads that never transferred to a business input.
func (p *Policy) CleanupTelegramAlbums(ctx context.Context, store *upload.Store) error {
	if err := p.q.DiscardUnauthorizedTelegramAlbums(ctx, p.botID); err != nil {
		return err
	}
	members, err := p.q.ListDiscardedTelegramAlbumMembers(ctx, p.botID)
	if err != nil {
		return err
	}
	for _, member := range members {
		var id string
		if member.BatchSource != "" {
			id, err = session.SubmissionSourceIn(ctx, p.q, member.BatchSource)
			if err != nil {
				return err
			}
		}
		if id == "" {
			var msg session.Message
			if err := json.Unmarshal([]byte(member.InputJson), &msg); err != nil {
				return err
			}
			for _, file := range msg.Attachments {
				if err := store.Remove(file.ID); err != nil && !errors.Is(err, upload.ErrNotFound) {
					return err
				}
			}
		}
		if err := p.q.ClearDiscardedTelegramAlbumMember(ctx, sqlc.ClearDiscardedTelegramAlbumMemberParams{BotID: p.botID, SourceKey: member.SourceKey}); err != nil {
			return err
		}
	}
	return p.cleanupSpeechInputs(ctx, store)
}
