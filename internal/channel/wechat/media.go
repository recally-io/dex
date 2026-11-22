package wechat

import (
	"bytes"
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/upload"
)

var errInvalidMessage = errors.New("invalid inbound WeChat message")

func (b *Bot) convert(ctx context.Context, items []item) (chat.Message, error) {
	var input chat.Message
	var texts []string
	for _, it := range items {
		if it.Ref != nil {
			quote := it.Ref.Title
			if quote == "" && it.Ref.Item != nil {
				quote = itemText(*it.Ref.Item)
			}
			if quote != "" {
				texts = append(texts, "[引用: "+quote+"]")
			}
		}
		if text := itemText(it); text != "" {
			texts = append(texts, text)
		}
		var ref *media
		name, key := "", ""
		switch {
		case it.Type == 2 && it.Image != nil:
			ref = it.Image.Media
			key = it.Image.Key
		case it.Type == 4 && it.File != nil:
			ref = it.File.Media
			name = it.File.Name
		default:
			continue
		}
		if len(input.Attachments) >= 10 {
			return input, errInvalidMessage
		}
		if ref == nil {
			return input, errInvalidMessage
		}
		data, err := b.download(ctx, *ref, key)
		if err != nil {
			return input, err
		}
		if name == "" {
			if it.Type == 4 {
				name = "file.bin"
			} else {
				switch http.DetectContentType(data) {
				case "image/png":
					name = "image.png"
				case "image/jpeg":
					name = "image.jpg"
				case "image/gif":
					name = "image.gif"
				case "image/webp":
					name = "image.webp"
				default:
					name = "image.bin"
				}
			}
		}
		attachment, err := b.uploads.Save(name, bytes.NewReader(data))
		if errors.Is(err, upload.ErrInvalidName) || errors.Is(err, upload.ErrTooLarge) {
			return input, fmt.Errorf("%w: %w", errInvalidMessage, err)
		}
		if err != nil {
			return input, err
		}
		input.Attachments = append(input.Attachments, attachment)
	}
	input.Content = strings.Join(texts, "\n")
	return input, nil
}

func itemText(it item) string {
	switch {
	case it.Type == 1 && it.Text != nil:
		return it.Text.Text
	case it.Type == 3 && it.Voice != nil:
		return it.Voice.Text
	case it.Type == 5:
		return "[video]"
	}
	return ""
}

func (b *Bot) download(ctx context.Context, ref media, hexKey string) ([]byte, error) {
	var key []byte
	var err error
	if hexKey != "" {
		key, err = hex.DecodeString(hexKey)
	} else {
		key, err = base64.StdEncoding.DecodeString(ref.Key)
		if err == nil && len(key) == 32 {
			key, err = hex.DecodeString(string(key))
		}
	}
	if err != nil || len(key) != aes.BlockSize {
		return nil, errInvalidMessage
	}
	rawURL := ref.URL
	if rawURL == "" {
		if ref.Query == "" {
			return nil, errInvalidMessage
		}
		rawURL = strings.TrimRight(b.client.options.CDNURL, "/") + "/download?encrypted_query_param=" + url.QueryEscape(ref.Query)
	}
	if err = checkURL(rawURL); err != nil {
		return nil, errInvalidMessage
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, errInvalidMessage
	}
	resp, err := b.client.http.Do(req)
	if err != nil {
		return nil, networkError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests {
		return nil, errInvalidMessage
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("WeChat CDN HTTP %d", resp.StatusCode)
	}
	// A 20 MB plaintext needs one extra block for PKCS7 padding.
	encrypted, err := io.ReadAll(io.LimitReader(resp.Body, upload.MaxSize+aes.BlockSize+1))
	if err != nil {
		return nil, err
	}
	if len(encrypted) == 0 || len(encrypted) > upload.MaxSize+aes.BlockSize || len(encrypted)%aes.BlockSize != 0 {
		return nil, errInvalidMessage
	}
	cipher, err := aes.NewCipher(key)
	if err != nil {
		return nil, errInvalidMessage
	}
	for offset := 0; offset < len(encrypted); offset += aes.BlockSize {
		cipher.Decrypt(encrypted[offset:offset+aes.BlockSize], encrypted[offset:offset+aes.BlockSize])
	}
	padding := int(encrypted[len(encrypted)-1])
	if padding < 1 || padding > aes.BlockSize {
		return nil, errInvalidMessage
	}
	for _, v := range encrypted[len(encrypted)-padding:] {
		if int(v) != padding {
			return nil, errInvalidMessage
		}
	}
	data := encrypted[:len(encrypted)-padding]
	if len(data) > upload.MaxSize {
		return nil, errInvalidMessage
	}
	return data, nil
}
