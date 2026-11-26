package session

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

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

// TelegramAlbum is a sealed set of member receipts. AcceptMessage records the input for admission.
type TelegramAlbum struct {
	Source  string
	Peer    string
	Message Message
}

func telegramAlbumKey(peer string, identity ChannelContext) (string, error) {
	parts := []string{peer, identity.SenderID, identity.MediaGroup}
	key, err := json.Marshal(parts)
	return "telegram:" + identity.AccountID + ":album:" + base64.RawURLEncoding.EncodeToString(key), err
}

func (s *Service) KnownTelegramAlbum(ctx context.Context, botID, peer string, identity ChannelContext) (bool, error) {
	key, err := telegramAlbumKey(peer, identity)
	if err != nil {
		return false, err
	}
	return s.q.HasTelegramAlbumGroup(ctx, sqlc.HasTelegramAlbumGroupParams{BotID: botID, GroupKey: key})
}

// StoreTelegramAlbum commits upload ownership before the transport may ACK the member.
func (s *Service) StoreTelegramAlbum(ctx context.Context, botID, peer, source string, msg Message) error {
	actor, err := strconv.ParseInt(msg.ChannelContext.SenderID, 10, 64)
	if NonPrivateTelegramPeer(peer) || err != nil || actor <= 0 || msg.ChannelContext.AccountID == "" || msg.ChannelContext.MediaGroup == "" || len(msg.Attachments) != 1 {
		return fmt.Errorf("%w: invalid Telegram album member", errSessionInvalid)
	}
	group, err := telegramAlbumKey(peer, msg.ChannelContext)
	if err != nil {
		return err
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := db.RequireActive(ctx, q, botID); err != nil {
		return err
	}
	// A native receipt or a member receipt owns this global source, even after a credential move.
	if _, err := q.GetSubmissionBySource(ctx, sql.NullString{String: source, Valid: true}); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := q.GetTelegramAlbumMember(ctx, source); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	n, err := q.StoreTelegramAlbumMember(ctx, sqlc.StoreTelegramAlbumMemberParams{
		BotID: botID, SourceKey: source, AccountID: msg.ChannelContext.AccountID, SenderID: msg.ChannelContext.SenderID,
		Peer: peer, GroupKey: group, InputJson: string(data), ReceivedAt: s.now().UnixMilli(),
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%w: Telegram album authorization changed", errSessionInvalid)
	}
	return tx.Commit()
}

// ReadyTelegramAlbums freezes each batch before any native call. Later members cannot change its payload.
func (s *Service) ReadyTelegramAlbums(ctx context.Context, botID string) ([]TelegramAlbum, time.Duration, error) {
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	groups, err := q.ListCollectingTelegramAlbums(ctx, botID)
	if err != nil {
		return nil, 0, err
	}
	now := s.now().UnixMilli()
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
		members, err := q.ListUnsealedTelegramAlbumMembers(ctx, sqlc.ListUnsealedTelegramAlbumMembersParams{BotID: botID, GroupKey: group.GroupKey})
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
		if err := q.SealTelegramAlbumMembers(ctx, sqlc.SealTelegramAlbumMembersParams{BotID: botID, BatchSource: batch, Sources: string(encoded)}); err != nil {
			return nil, 0, err
		}
		if group.Members > 10 {
			delay = 0
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	batches, err := s.q.ListSealedTelegramAlbums(ctx, botID)
	if err != nil {
		return nil, 0, err
	}
	ready := make([]TelegramAlbum, 0, len(batches))
	for _, source := range batches {
		members, err := s.q.ListTelegramAlbumBatchMembers(ctx, sqlc.ListTelegramAlbumBatchMembersParams{BotID: botID, BatchSource: source})
		if err != nil {
			return nil, 0, err
		}
		// Settings can discard a sealed batch after the list query.
		if len(members) == 0 {
			continue
		}
		var msg Message
		var texts, notes []string
		for i, member := range members {
			var input Message
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
		late, err := s.q.HasEarlierTelegramAlbumBatch(ctx, sqlc.HasEarlierTelegramAlbumBatchParams{BotID: botID, GroupKey: members[0].GroupKey, BatchSource: source})
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
		ready = append(ready, TelegramAlbum{Source: source, Peer: members[0].Peer, Message: msg})
	}
	return ready, delay, nil
}

// FinishTelegramAlbum transfers upload ownership to the stored business input, not a second execution queue.
func (s *Service) FinishTelegramAlbum(ctx context.Context, botID, source string) error {
	receipt, err := s.LookupSource(ctx, botID, source)
	if err != nil {
		return err
	}
	if receipt.ID == "" {
		return fmt.Errorf("%w: album input has no business receipt", errSessionInvalid)
	}
	return s.q.TransferTelegramAlbumMembers(ctx, sqlc.TransferTelegramAlbumMembersParams{BotID: botID, BatchSource: source})
}

// DiscardedTelegramUpload identifies files that no native input owns.
type DiscardedTelegramUpload struct {
	Source      string
	Attachments []engine.Attachment
}

func (s *Service) DiscardedTelegramUploads(ctx context.Context, botID string) ([]DiscardedTelegramUpload, error) {
	if err := s.q.DiscardUnauthorizedTelegramAlbums(ctx, botID); err != nil {
		return nil, err
	}
	members, err := s.q.ListDiscardedTelegramAlbumMembers(ctx, botID)
	if err != nil {
		return nil, err
	}
	out := make([]DiscardedTelegramUpload, 0, len(members))
	for _, member := range members {
		var attachments []engine.Attachment
		if member.BatchSource != "" {
			_, err := s.q.GetSubmissionBySource(ctx, sql.NullString{String: member.BatchSource, Valid: true})
			if err == nil {
				out = append(out, DiscardedTelegramUpload{Source: member.SourceKey})
				continue
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
		}
		var msg Message
		if err := json.Unmarshal([]byte(member.InputJson), &msg); err != nil {
			return nil, err
		}
		attachments = msg.Attachments
		out = append(out, DiscardedTelegramUpload{Source: member.SourceKey, Attachments: attachments})
	}
	return out, nil
}

func (s *Service) ClearDiscardedTelegramUpload(ctx context.Context, botID, source string) error {
	return s.q.ClearDiscardedTelegramAlbumMember(ctx, sqlc.ClearDiscardedTelegramAlbumMemberParams{BotID: botID, SourceKey: source})
}
