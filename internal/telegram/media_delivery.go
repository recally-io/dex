package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"strconv"
	"time"

	"github.com/recally-io/dex/internal/channel"
)

// Send confirms one frozen text or media part through the shared durable sender.
func (b *Bot) Send(ctx context.Context, peer, deliveryID string, index int, part channel.Part) (channel.Receipt, error) {
	var id string
	var err error
	if part.File != nil {
		id, err = b.sendFile(ctx, peer, part.File)
	} else {
		id, err = b.sendText(ctx, peer, deliveryID, index, part.Text)
	}
	receipt := channel.Receipt{}
	if id != "" {
		receipt.MessageIDs = []string{id}
	}
	if api, ok := errors.AsType[*apiError](err); ok {
		if api.Code == 429 && api.RetryAfter > 0 {
			return receipt, &channel.RetryError{After: time.Duration(api.RetryAfter) * time.Second, Err: err}
		}
		if (api.Code == 400 || api.Code == 403) && !errors.Is(err, channel.ErrPermanent) {
			err = fmt.Errorf("%w: %w", channel.ErrPermanent, err)
		}
	}
	return receipt, err
}

func (b *Bot) sendFile(ctx context.Context, peer string, file *channel.File) (string, error) {
	chatID, err := peerID(peer)
	if err != nil {
		return "", err
	}
	method, field := "sendDocument", "document"
	if file.Size <= 10<<20 && (file.MimeType == "image/png" || file.MimeType == "image/jpeg") {
		decode := png.DecodeConfig
		if file.MimeType == "image/jpeg" {
			decode = jpeg.DecodeConfig
		}
		config, err := decode(bytes.NewReader(file.Body))
		if err == nil && config.Width > 0 && config.Height > 0 && config.Width+config.Height <= 10000 && config.Width <= 20*config.Height && config.Height <= 20*config.Width {
			method, field = "sendPhoto", "photo"
		}
	}
	send := func(method, field string) (string, error) {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		if err := form.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
			return "", err
		}
		if err := form.WriteField("caption", file.Name); err != nil {
			return "", err
		}
		payload, err := form.CreateFormFile(field, file.Name)
		if err != nil {
			return "", err
		}
		if _, err := payload.Write(file.Body); err != nil {
			return "", err
		}
		if err := form.Close(); err != nil {
			return "", err
		}
		var result struct {
			ID int64 `json:"message_id"`
		}
		if err := b.request(ctx, method, &body, form.FormDataContentType(), &result); err != nil {
			return "", err
		}
		return strconv.FormatInt(result.ID, 10), nil
	}
	id, err := send(method, field)
	// Only a definitive photo rejection may change the API method. A network failure is ambiguous.
	if api, ok := errors.AsType[*apiError](err); ok && api.Code == 400 && method == "sendPhoto" {
		return send("sendDocument", "document")
	}
	return id, err
}

func (b *Bot) waitRate(ctx context.Context) error {
	for {
		b.rateMu.Lock()
		delay := time.Until(b.retryAt)
		b.rateMu.Unlock()
		if delay <= 0 {
			return nil
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
