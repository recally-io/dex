package telegram

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/upload"
)

func (b *Bot) groupText(ctx context.Context, m *message) (string, bool, error) {
	text := m.Text
	if text == "" {
		text = m.Caption
	}
	acceptUnmentioned := !b.cfg.Options.RequireMention
	botID, _ := strconv.ParseInt(b.cfg.botID(), 10, 64)
	replied := m.ReplyTo != nil && m.ReplyTo.From != nil && m.ReplyTo.From.ID == botID
	if !strings.Contains(text, "@") {
		return text, replied || acceptUnmentioned, nil
	}
	if b.username == "" {
		var me user
		if err := b.call(ctx, "getMe", map[string]any{}, &me); err != nil {
			return "", false, err
		}
		b.username = me.Username
	}
	if b.username == "" {
		return text, replied || acceptUnmentioned, nil
	}
	first, rest, _ := strings.Cut(text, " ")
	if command, name, ok := strings.Cut(first, "@"); strings.HasPrefix(command, "/") && ok {
		if !strings.EqualFold(name, b.username) {
			return text, false, nil
		}
		return strings.TrimSpace(command + " " + rest), true, nil
	}
	mention := regexp.MustCompile(`(?i)(^|\s)@` + regexp.QuoteMeta(b.username) + `\b`)
	mentioned := mention.MatchString(text)
	return strings.TrimSpace(mention.ReplaceAllString(text, "$1")), replied || mentioned || acceptUnmentioned, nil
}

func (b *Bot) receiveGroup(ctx context.Context, u update, m *message, edited bool) error {
	if m.Chat.ID >= 0 || m.From.ID <= 0 || m.From.IsBot || !slices.Contains(b.cfg.AllowedUsers, m.From.ID) {
		return nil
	}
	identity := chat.ChannelContext{AccountID: b.cfg.botID(), SenderID: strconv.FormatInt(m.From.ID, 10), MessageID: strconv.FormatInt(m.ID, 10), ChatID: m.Chat.ID, TopicID: m.ThreadID, TopicShared: m.Chat.IsForum || m.IsTopic, MediaGroup: m.MediaGroupID}
	peer := channelPrefix + strconv.FormatInt(m.Chat.ID, 10)
	if m.ThreadID > 0 {
		peer += ":" + strconv.FormatInt(m.ThreadID, 10)
	}
	allowed, err := b.chats.TelegramInputAllowed(ctx, peer, identity)
	if err != nil {
		return err
	}
	if !allowed {
		return nil
	}
	content, triggered, err := b.groupText(ctx, m)
	if err != nil {
		return err
	}
	if !triggered && m.MediaGroupID != "" {
		triggered, err = b.chats.KnownTelegramAlbum(ctx, peer, identity)
		if err != nil {
			return err
		}
	}
	if !triggered {
		return nil
	}
	source := channelPrefix + b.cfg.botID() + ":" + strconv.FormatInt(u.UpdateID, 10)
	if received, err := b.chats.ReceivedSource(ctx, source); err != nil {
		return err
	} else if received {
		return nil
	}
	store, err := b.chats.GroupUploads(ctx, peer, identity)
	if err != nil {
		return err
	}
	defer store.Close()
	attachments, err := b.downloadTo(ctx, m, store)
	if err != nil {
		permanent := errors.Is(err, upload.ErrTooLarge) || errors.Is(err, upload.ErrInvalidName)
		if api, ok := errors.AsType[*apiError](err); ok && api.Code == 400 {
			permanent = true
		}
		if !permanent {
			return err
		}
		return b.controlPeerReply(ctx, peer, "Cannot read this attachment. Send a supported file under 20 MB.")
	}
	if m.ForwardOrigin != nil {
		content = "Forwarded message:\n" + content
	}
	if edited {
		content = fmt.Sprintf("Correction to Telegram message %d (earlier actions are not undone):\n%s", m.ID, content)
	}
	incoming := chat.Message{Content: content, Attachments: attachments, ChannelNote: m.contextNote(), ChannelContext: identity}
	if selected, kind := m.selectedMedia(); selected != nil {
		incoming.ChannelContext.FileID = selected.ID
		incoming.ChannelContext.MediaKind = kind
	}
	if m.ReplyTo != nil {
		incoming.ReplyTo = strconv.FormatInt(m.ReplyTo.ID, 10)
		incoming.ChannelContext.ReplyTo = incoming.ReplyTo
	}
	// A group retains original audio. It never borrows the private speech consent or provider.
	var receiveErr error
	if m.MediaGroupID != "" && len(attachments) > 0 {
		receiveErr = b.chats.StoreTelegramAlbum(ctx, peer, source, incoming)
		if receiveErr == nil {
			b.WakeMedia()
		}
	} else {
		receiveErr = b.chats.Receive(ctx, peer, source, incoming)
	}
	if len(attachments) > 0 {
		receipt, lookupErr := b.chats.Source(context.WithoutCancel(ctx), source)
		if lookupErr == nil && receipt.ID == "" && !receipt.Staged {
			for _, file := range attachments {
				if err := store.Remove(file.ID); err != nil {
					return errors.Join(receiveErr, err)
				}
			}
		}
		return errors.Join(receiveErr, lookupErr)
	}
	return receiveErr
}

func (b *Bot) controlPeerReply(ctx context.Context, peer, text string) error {
	chatID, topicID, err := parsePeer(peer)
	if err != nil {
		return err
	}
	params := map[string]any{"chat_id": chatID, "text": text}
	if topicID > 0 {
		params["message_thread_id"] = topicID
	}
	b.presentation(params, "sendMessage")
	err = b.call(ctx, "sendMessage", params, nil)
	if api, ok := errors.AsType[*apiError](err); ok && (api.Code == 400 || api.Code == 403) {
		return nil
	}
	return err
}
