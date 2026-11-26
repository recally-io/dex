package delivery

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// Webhook handles a callback for the currently active bot, if it accepts HTTP.
func (m *Manager) Webhook(w http.ResponseWriter, r *http.Request, platform string) {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		status := http.StatusBadRequest
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			status = http.StatusRequestEntityTooLarge
		}
		w.WriteHeader(status)
		return
	}
	// A settings change cannot finish until the prior receiver finishes intake.
	m.intakeMu.Lock()
	defer m.intakeMu.Unlock()
	m.mu.Lock()
	active := m.bots[platform]
	m.mu.Unlock()
	if active == nil || active.webhook == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if active.ctx.Err() != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	hook := active.webhook
	event, status := hook.Decode(r.Header, data)
	if status != http.StatusOK {
		w.WriteHeader(status)
		return
	}
	if event.Challenge != "" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.MarshalWrite(w, struct {
			Challenge string `json:"challenge"`
		}{Challenge: event.Challenge})
		return
	}
	if err := m.q.EnqueueChannelWebhook(r.Context(), sqlc.EnqueueChannelWebhookParams{
		Platform: platform, Account: hook.Account, EventID: event.ID, BotID: m.botID, ReceivedAt: time.Now().UnixMilli(), Payload: event.Payload,
	}); err != nil {
		slog.Warn("channel: callback could not commit", "channel", platform, "err", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	select {
	case active.inboundWake <- struct{}{}:
	default:
	}
}

// HandledWebhook reports whether the webhook receiver finished the event.
func (m *Manager) HandledWebhook(ctx context.Context, platform, account, eventID string) (bool, error) {
	n, err := m.q.CountHandledChannelWebhook(ctx, sqlc.CountHandledChannelWebhookParams{Platform: platform, Account: account, EventID: eventID})
	return n != 0, err
}

// DrainWebhookInbox runs on the bot's receiver loop. On rollback, drain committed
// callbacks before starting the platform's other transport.
func (m *Manager) DrainWebhookInbox(ctx context.Context, platform, account string, continuous bool, receive func(context.Context, []byte) error) error {
	m.mu.Lock()
	active := m.bots[platform]
	m.mu.Unlock()
	if active == nil {
		return ctx.Err()
	}
	key := sqlc.NextChannelWebhookParams{BotID: m.botID, Platform: platform, Account: account}
	for ctx.Err() == nil {
		// Do not start processing a new row before its HTTP handler writes the ACK.
		m.intakeMu.Lock()
		row, err := m.q.NextChannelWebhook(ctx, key)
		m.intakeMu.Unlock()
		if err == nil {
			err = receive(ctx, row.Payload)
			if err == nil {
				err = m.q.FinishChannelWebhook(ctx, row.ID)
				if err == nil {
					continue
				}
			}
		} else if errors.Is(err, sql.ErrNoRows) {
			if !continuous {
				return nil
			}
			select {
			case <-ctx.Done():
			case <-active.inboundWake:
			}
			continue
		}
		if ctx.Err() == nil {
			slog.Warn("channel: callback processing waits for retry", "channel", platform, "err", err)
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}
	}
	return ctx.Err()
}
