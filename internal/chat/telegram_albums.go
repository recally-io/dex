package chat

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

// TelegramAlbum is a sealed set of member receipts. SubmitMessage still owns native admission.
type TelegramAlbum struct {
	Source  string
	Peer    string
	Message Message
}

// StoreTelegramAlbum commits upload ownership before the transport may ACK the member.
func (s *Service) StoreTelegramAlbum(ctx context.Context, agentID, peer, source string, msg Message) error {
	actor, err := strconv.ParseInt(msg.ChannelContext.SenderID, 10, 64)
	if err != nil || actor <= 0 || msg.ChannelContext.AccountID == "" || msg.ChannelContext.MediaGroup == "" || len(msg.Attachments) != 1 {
		return fmt.Errorf("%w: invalid Telegram album member", ErrSessionInvalid)
	}
	key, err := json.Marshal([]string{peer, msg.ChannelContext.SenderID, msg.ChannelContext.MediaGroup})
	if err != nil {
		return err
	}
	group := "telegram:" + msg.ChannelContext.AccountID + ":album:" + base64.RawURLEncoding.EncodeToString(key)
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := db.RequireActive(ctx, q, agentID); err != nil {
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
		AgentID: agentID, SourceKey: source, AccountID: msg.ChannelContext.AccountID, SenderID: msg.ChannelContext.SenderID,
		Peer: peer, GroupKey: group, InputJson: string(data), ReceivedAt: s.now().UnixMilli(),
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%w: Telegram album authorization changed", ErrSessionInvalid)
	}
	return tx.Commit()
}

// ReadyTelegramAlbums freezes each batch before any native call. Later members cannot change its payload.
func (s *Service) ReadyTelegramAlbums(ctx context.Context, agentID string) ([]TelegramAlbum, time.Duration, error) {
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	groups, err := q.ListCollectingTelegramAlbums(ctx, agentID)
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
		members, err := q.ListUnsealedTelegramAlbumMembers(ctx, sqlc.ListUnsealedTelegramAlbumMembersParams{AgentID: agentID, GroupKey: group.GroupKey})
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
		if err := q.SealTelegramAlbumMembers(ctx, sqlc.SealTelegramAlbumMembersParams{AgentID: agentID, BatchSource: batch, Sources: string(encoded)}); err != nil {
			return nil, 0, err
		}
		if group.Members > 10 {
			delay = 0
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	batches, err := s.q.ListSealedTelegramAlbums(ctx, agentID)
	if err != nil {
		return nil, 0, err
	}
	ready := make([]TelegramAlbum, 0, len(batches))
	for _, source := range batches {
		members, err := s.q.ListTelegramAlbumBatchMembers(ctx, sqlc.ListTelegramAlbumBatchMembersParams{AgentID: agentID, BatchSource: source})
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
				texts = append(texts, fmt.Sprintf("File %d (%s): %s", i+1, input.Attachments[0].Name, input.Content))
			}
			if input.ChannelNote != "" {
				notes = append(notes, input.ChannelNote)
			}
		}
		late, err := s.q.HasEarlierTelegramAlbumBatch(ctx, sqlc.HasEarlierTelegramAlbumBatchParams{AgentID: agentID, GroupKey: members[0].GroupKey, BatchSource: source})
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
func (s *Service) FinishTelegramAlbum(ctx context.Context, agentID, source string) error {
	receipt, err := s.LookupSource(ctx, agentID, source)
	if err != nil {
		return err
	}
	if receipt.ID == "" {
		return fmt.Errorf("%w: album input has no business receipt", ErrSessionInvalid)
	}
	return s.q.TransferTelegramAlbumMembers(ctx, sqlc.TransferTelegramAlbumMembersParams{AgentID: agentID, BatchSource: source})
}

// DiscardedTelegramUpload identifies files that no native input owns.
type DiscardedTelegramUpload struct {
	Source      string
	Attachments []engine.Attachment
}

func (s *Service) DiscardedTelegramUploads(ctx context.Context, agentID string) ([]DiscardedTelegramUpload, error) {
	members, err := s.q.ListDiscardedTelegramAlbumMembers(ctx, agentID)
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

func (s *Service) ClearDiscardedTelegramUpload(ctx context.Context, agentID, source string) error {
	return s.q.ClearDiscardedTelegramAlbumMember(ctx, sqlc.ClearDiscardedTelegramAlbumMemberParams{AgentID: agentID, SourceKey: source})
}
