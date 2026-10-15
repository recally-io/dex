package feishu

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/upload"
)

// EventStreams opens the worker-backed transcript of a chat.
type EventStreams interface {
	Open(context.Context, int64) (io.ReadCloser, error)
}

// Bot connects the platform to durable admission, uploads and replies.
type Bot struct {
	cfg      Config
	client   *Client
	manager  *channel.Manager
	q        *sqlc.Queries
	hub      EventStreams
	uploads  *upload.Store
	pair     func(context.Context, string, string) (bool, error)
	mu       sync.Mutex // one owner's traffic; per preview if outbound latency blocks admission
	previews map[string]*preview
	wg       sync.WaitGroup
}

// New creates a bot. pair atomically consumes a manual setup code.
func New(cfg Config, manager *channel.Manager, q *sqlc.Queries, hub EventStreams, uploads *upload.Store, pair func(context.Context, string, string) (bool, error)) *Bot {
	return &Bot{cfg: cfg, client: NewClient(cfg), manager: manager, q: q, hub: hub, uploads: uploads, pair: pair, previews: make(map[string]*preview)}
}

// Run receives events until the manager cancels it.
func (b *Bot) Run(ctx context.Context) error {
	defer b.wg.Wait()
	return b.client.Listen(ctx, func(session context.Context, data []byte) error {
		return b.receive(session, ctx, data)
	}, func(err error) {
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
	})
}

func (b *Bot) receive(ctx, previewCtx context.Context, data []byte) error {
	m, err := ParseMessage(data)
	if err != nil {
		slog.Warn("feishu: ignored invalid message")
		return nil
	}
	if m.ID == "" {
		return nil
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
		paired, err := b.pair(ctx, m.Sender, strings.TrimSpace(code))
		if err != nil {
			return err
		}
		if !paired {
			return nil
		}
		b.cfg.OwnerID = m.Sender
		b.manager.Report("feishu", channel.Status{State: "running"})
		_, err = b.client.Send(ctx, m.Sender, "text", TextContent("Paired. Send a message to start."), deliveryUUID("pair:"+m.ID, 0))
		return err
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
	// Avoid a second download or preview when the platform repeats an admitted event.
	if _, err := b.q.GetSubmissionBySource(ctx, sql.NullString{String: source, Valid: true}); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	msg := chat.Message{Content: m.Text}
	for _, resource := range m.Resources {
		reader, err := b.client.Download(ctx, m.ID, resource.Key, resource.Type)
		if err != nil {
			if permanentAPIError(err) {
				return nil
			}
			return err
		}
		attachment, saveErr := b.uploads.Save(resource.Name, reader)
		closeErr := reader.Close()
		if err := errors.Join(saveErr, closeErr); err != nil {
			if errors.Is(err, upload.ErrTooLarge) || errors.Is(err, upload.ErrInvalidName) {
				slog.Warn("feishu: ignored rejected attachment")
				return nil
			}
			return err
		}
		msg.Attachments = append(msg.Attachments, attachment)
	}
	peer := "feishu:" + m.Sender
	if err := b.manager.Receive(ctx, peer, source, msg); err != nil {
		return err
	}
	id, err := b.q.GetSubmissionBySource(ctx, sql.NullString{String: source, Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	row, err := b.q.GetSubmission(ctx, id)
	if err != nil {
		return err
	}
	if row.Status != "pending" {
		return nil
	}
	// Final delivery takes the same lock, so a fast answer cannot pass this preview.
	p := &preview{}
	b.previews[id] = p
	streamCtx, cancel := context.WithCancel(previewCtx)
	p.cancel = cancel
	b.wg.Go(func() { b.stream(streamCtx, id, row.ConversationID, m.Sender, p) })
	return nil
}

// Parts renders final replies. The durable sender records each confirmed part.
func (b *Bot) Parts(reply chat.Reply) []string {
	text := reply.Answer
	if reply.Status != "done" {
		text = "No answer: " + reply.Reason
	}
	if strings.TrimSpace(text) == "" {
		text = "(empty answer)"
	}
	return Parts(text)
}

// Send patches the preview for part zero. Other parts use a stable create ID.
func (b *Bot) Send(ctx context.Context, peer, id string, part int, text string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	owner, ok := strings.CutPrefix(peer, "feishu:")
	if !ok || owner == "" || owner != b.cfg.OwnerID {
		return fmt.Errorf("%w: feishu destination is not the paired owner", channel.ErrPermanent)
	}
	card := CardContent(text, strings.HasPrefix(text, "No answer: "))
	p := b.previews[id]
	if p != nil && part == 0 {
		p.cancel()
		if p.messageID != "" {
			err := b.client.Patch(ctx, p.messageID, card)
			if err == nil {
				delete(b.previews, id)
				b.manager.Report("feishu", channel.Status{State: "running"})
				return nil
			}
			if !permanentAPIError(err) {
				b.manager.Report("feishu", channel.Status{State: "error", Reason: err.Error()})
				return err
			}
		}
	}
	uuid := deliveryUUID(id, part)
	_, err := b.client.Send(ctx, owner, "interactive", card, uuid)
	if e, ok := errors.AsType[*APIError](err); ok && e.Code == 230001 && (e.Status == http.StatusBadRequest || e.Status >= 200 && e.Status < 300) {
		_, err = b.client.Send(ctx, owner, "text", TextContent(text), deliveryUUID(id+":text", part))
	}
	if err != nil {
		b.manager.Report("feishu", channel.Status{State: "error", Reason: err.Error()})
		if permanentAPIError(err) {
			delete(b.previews, id)
			return fmt.Errorf("%w: %w", channel.ErrPermanent, err)
		}
	} else {
		delete(b.previews, id)
		b.manager.Report("feishu", channel.Status{State: "running"})
	}
	return err
}

func deliveryUUID(id string, part int) string {
	// Feishu limits uuid to 50 characters. The complete identity remains in the digest.
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", id, part)))
	return hex.EncodeToString(sum[:24])
}

func permanentAPIError(err error) bool {
	e, ok := errors.AsType[*APIError](err)
	return ok && ((e.Status >= 400 && e.Status < 500 && e.Status != http.StatusUnauthorized) ||
		(e.Status >= 200 && e.Status < 300 && e.Code != 0)) &&
		!retryable(e.Status, e.Code) && (e.Code < 99991600 || e.Code >= 99991700)
}
