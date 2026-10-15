package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/recally-io/dex/internal/upload"
	"github.com/recally-io/dex/internal/worker"
)

type media struct {
	ID     string `json:"file_id"`
	Size   int64  `json:"file_size"`
	Name   string `json:"file_name"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

func (b *Bot) download(ctx context.Context, m *message) ([]worker.Attachment, error) {
	selected := m.Document
	for i := range m.Photo {
		photo := &m.Photo[i]
		if selected == nil || photo.Width*photo.Height > selected.Width*selected.Height {
			selected = photo
		}
	}
	if selected == nil {
		return nil, nil
	}
	if selected.Size > upload.MaxSize {
		return nil, upload.ErrTooLarge
	}
	var file struct {
		Path string `json:"file_path"`
		Size int64  `json:"file_size"`
	}
	if err := b.call(ctx, "getFile", map[string]any{"file_id": selected.ID}, &file); err != nil {
		return nil, err
	}
	if file.Size > upload.MaxSize {
		return nil, upload.ErrTooLarge
	}
	// Telegram returns a relative file path. It cannot select another server or a bot method.
	if file.Path == "" || file.Path == "." || file.Path == ".." || strings.HasPrefix(file.Path, "/") || strings.ContainsAny(file.Path, `\?#`) || path.Clean(file.Path) != file.Path || strings.HasPrefix(file.Path, "../") {
		return nil, errors.New("telegram returned an invalid file path")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.cfg.APIURL+"/file/bot"+b.cfg.Token+"/"+file.Path, nil)
	if err != nil {
		return nil, errors.New("telegram file request is invalid")
	}
	resp, err := b.client.Do(req)
	if err != nil {
		if e, ok := errors.AsType[*url.Error](err); ok {
			err = e.Err
		}
		return nil, fmt.Errorf("telegram file download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram file download: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > upload.MaxSize {
		return nil, upload.ErrTooLarge
	}
	name := selected.Name
	if name == "" {
		name = path.Base(file.Path)
	}
	a, err := b.cfg.Uploads.Save(name, resp.Body)
	if err != nil {
		return nil, err
	}
	return []worker.Attachment{a}, nil
}
