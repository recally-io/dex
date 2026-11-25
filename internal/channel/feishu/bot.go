package feishu

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/upload"
)

type inbound interface {
	channel.WebhookInbox
	Receive(ctx context.Context, peer, source string, msg chat.Message) error
	QueueChannelNotice(ctx context.Context, peer, source string, identity chat.ChannelContext, body string) error
	ReceivedSource(ctx context.Context, source string) (bool, error)
	Source(ctx context.Context, source string) (chat.SourceReceipt, error)
	Report(name string, status channel.Status)
}

// Bot connects the platform to durable admission, uploads and replies.
type Bot struct {
	cfg       Config
	client    *client
	reactions *client
	manager   inbound
	uploads   *upload.Store
	pair      func(context.Context, string, string) (bool, error)
	mu        sync.Mutex
}

// New creates a bot. pair atomically consumes a manual setup code.
func New(cfg Config, manager inbound, uploads *upload.Store, pair func(context.Context, string, string) (bool, error)) *Bot {
	reactionClient := newClient(cfg)
	reactionClient.once = true
	return &Bot{cfg: cfg, client: newClient(cfg), reactions: reactionClient, manager: manager, uploads: uploads, pair: pair}
}

// Run receives events until the manager cancels it.
func (b *Bot) Run(ctx context.Context) error {
	webhook := b.cfg.Webhook.VerificationToken != ""
	if webhook {
		b.reportConnection(nil)
	}
	if err := b.manager.DrainWebhookInbox(ctx, "feishu", b.cfg.AppID, webhook, func(ctx context.Context, data []byte) error {
		return b.receive(ctx, ctx, data)
	}); err != nil || webhook {
		return err
	}
	return b.client.Listen(ctx, func(session context.Context, data []byte) error {
		return b.receive(session, ctx, data)
	}, b.reportConnection)
}

func (b *Bot) reportConnection(err error) {
	state, reason := "running", ""
	b.mu.Lock()
	owner := b.cfg.OwnerID
	b.mu.Unlock()
	if owner == "" {
		state = "needs_login"
	}
	if err != nil {
		state = "error"
		reason = err.Error()
	}
	b.manager.Report("feishu", channel.Status{State: state, Reason: reason})
}

func (b *Bot) receive(sessionCtx, botCtx context.Context, data []byte) error {
	m, err := parseMessage(data)
	if err != nil {
		slog.Warn("feishu: ignored invalid message")
		return nil
	}
	if m.ID == "" {
		return nil
	}
	if b.cfg.Webhook.VerificationToken == "" && m.EventID != "" {
		if handled, err := b.manager.HandledWebhook(sessionCtx, "feishu", b.cfg.AppID, m.EventID); err != nil || handled {
			return err
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if m.ChatType != "p2p" {
		slog.Info("feishu: ignored non-private message")
		return nil
	}
	if b.cfg.OwnerID == "" {
		code, ok := strings.CutPrefix(m.Text, "/pair ")
		if !ok {
			slog.Info("feishu: owner not paired; ignored message")
			return nil
		}
		paired, err := b.pair(sessionCtx, m.Sender, strings.TrimSpace(code))
		if err != nil {
			return err
		}
		if !paired {
			return nil
		}
		b.cfg.OwnerID = m.Sender
		b.manager.Report("feishu", channel.Status{State: "running"})
		return b.manager.QueueChannelNotice(sessionCtx, "feishu:"+m.Sender, "feishu:"+b.cfg.AppID+":"+m.ID,
			chat.ChannelContext{AccountID: b.cfg.AppID, SenderID: m.Sender}, "Paired. Send a message to start.")
	}
	// Pairing codes are control data, including repeated setup events.
	if strings.HasPrefix(m.Text, "/pair ") {
		return nil
	}
	if m.Sender != b.cfg.OwnerID {
		slog.Info("feishu: ignored message from another user")
		return nil
	}
	if m.Text == "" && len(m.Resources) == 0 {
		return nil
	}
	source := "feishu:" + b.cfg.AppID + ":" + m.ID
	// A stored receipt owns its attachments before native admission.
	if received, err := b.manager.ReceivedSource(sessionCtx, source); err != nil {
		return err
	} else if received {
		return nil
	}
	peer := "feishu:" + m.Sender
	actor := chat.ChannelContext{AccountID: b.cfg.AppID, SenderID: m.Sender, MessageID: m.ID}
	if channel.IsFeedbackCommand(m.Text) {
		text := channel.FeedbackCommand
		if m.PlainText {
			text = m.Text
		}
		return b.manager.Receive(botCtx, peer, source, chat.Message{Content: text, ChannelContext: actor})
	}
	msg := chat.Message{Content: m.Text, ReplyTo: m.ReplyTo, ChannelContext: actor}
	stored := false
	defer func() {
		if !stored {
			for _, attachment := range msg.Attachments {
				if err := b.uploads.Remove(attachment.ID); err != nil {
					slog.Warn("feishu: upload cleanup failed", "err", err)
				}
			}
		}
	}()
	for _, resource := range m.Resources {
		reader, err := b.client.Download(sessionCtx, m.ID, resource.Key, resource.Type)
		if err != nil {
			if permanentAPIError(err) {
				return nil
			}
			return err
		}
		attachment, saveErr := b.uploads.Save(resource.Name, reader)
		if saveErr == nil {
			msg.Attachments = append(msg.Attachments, attachment)
		}
		closeErr := reader.Close()
		if err := errors.Join(saveErr, closeErr); err != nil {
			if errors.Is(err, upload.ErrTooLarge) || errors.Is(err, upload.ErrInvalidName) {
				slog.Warn("feishu: ignored rejected attachment")
				return nil
			}
			return err
		}
	}
	receiveErr := b.manager.Receive(botCtx, peer, source, msg)
	// Preserve files when the receipt query fails or the receipt committed before admission.
	receipt, err := b.manager.Source(context.WithoutCancel(sessionCtx), source)
	stored = err != nil || receipt.ID != ""
	if err := errors.Join(receiveErr, err); err != nil {
		return err
	}
	return nil
}

// Parts renders a final reply that fits in one message.
func (b *Bot) Parts(reply channel.Reply) []channel.Part {
	text := reply.Answer
	if reply.Status != "done" {
		text = "No answer: " + reply.Reason
	}
	if strings.TrimSpace(text) == "" {
		text = "(empty answer)"
	}
	if len(text) > 4000 {
		return nil
	}
	return []channel.Part{{Text: text}}
}

// Send uses a stable platform message ID for retries.
func (b *Bot) Send(ctx context.Context, peer, id string, part channel.Part) (channel.Receipt, error) {
	if part.File != nil {
		return channel.Receipt{}, fmt.Errorf("%w: Feishu file delivery is unavailable", channel.ErrPermanent)
	}
	remote, err := b.sendText(ctx, peer, id, part.Text)
	return channel.Receipt{MessageID: remote}, err
}

func (b *Bot) sendText(ctx context.Context, peer, id, text string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	owner, ok := strings.CutPrefix(peer, "feishu:")
	if !ok || owner == "" || owner != b.cfg.OwnerID {
		return "", fmt.Errorf("%w: feishu destination is not the paired owner", channel.ErrPermanent)
	}
	card := cardContent(text, strings.HasPrefix(text, "No answer: "))
	uuid := deliveryUUID(id)
	remote, err := b.client.Send(ctx, owner, "interactive", card, uuid)
	if e, ok := errors.AsType[*apiError](err); ok && e.Code == 230001 && (e.Status == http.StatusBadRequest || e.Status >= 200 && e.Status < 300) {
		remote, err = b.client.Send(ctx, owner, "text", textContent(text), uuid)
	}
	if err != nil {
		b.manager.Report("feishu", channel.Status{State: "error", Reason: err.Error()})
		if permanentAPIError(err) {
			return "", fmt.Errorf("%w: %w", channel.ErrPermanent, err)
		}
		return "", err
	}
	b.manager.Report("feishu", channel.Status{State: "running"})
	return remote, nil
}

func deliveryUUID(id string) string {
	// Feishu limits uuid to 50 characters. The complete identity remains in the digest.
	sum := sha256.Sum256([]byte(id + ":0"))
	return hex.EncodeToString(sum[:24])
}

func permanentAPIError(err error) bool {
	e, ok := errors.AsType[*apiError](err)
	return ok && ((e.Status >= 400 && e.Status < 500 && e.Status != http.StatusUnauthorized) ||
		(e.Status >= 200 && e.Status < 300 && e.Code != 0)) &&
		!retryable(e.Status, e.Code) && (e.Code < 99991600 || e.Code >= 99991700)
}
