package telegram

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/recally-io/dex/internal/chat"
)

func (b *Bot) groupText(ctx context.Context, m *message) (string, bool, error) {
	text := m.body()
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

func (b *Bot) receiveGroup(ctx context.Context, source string, m *message, edited bool) error {
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
		return b.rejectAttachment(ctx, peer, err)
	}
	// A group retains original audio. It never borrows the private speech consent or provider.
	incoming := m.inbound(content, edited, attachments, identity)
	receiveErr := b.submitInput(ctx, peer, source, incoming, m.MediaGroupID != "" && len(attachments) > 0)
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
	for _, part := range renderRich(text) {
		params := map[string]any{"chat_id": chatID, "rich_message": richContent(part.Rich)}
		if topicID > 0 {
			params["message_thread_id"] = topicID
		}
		b.presentation(params, "sendRichMessage")
		err = b.call(ctx, "sendRichMessage", params, nil)
		if api, ok := errors.AsType[*apiError](err); ok && (api.Code == 400 || api.Code == 403) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}
