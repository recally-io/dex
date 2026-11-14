package telegram

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"mime/multipart"
	"strconv"

	"github.com/recally-io/dex/internal/channel"
)

// GroupParts groups only compatible consecutive files. Existing frozen replies never call it again.
func (b *Bot) GroupParts(parts []channel.Part) []channel.Part {
	var out []channel.Part
	for start := 0; start < len(parts); {
		end := start + 1
		bucket := ""
		if file := parts[start].File; file != nil {
			_, bucket = fileMethod(file)
			if bucket == "photo" || bucket == "video" {
				bucket = "visual"
			}
			if bucket == "voice" || bucket == "animation" {
				bucket = ""
			}
		}
		if bucket != "" {
			for end < len(parts) && end-start < 10 && parts[end].File != nil {
				_, next := fileMethod(parts[end].File)
				if next == "photo" || next == "video" {
					next = "visual"
				}
				if next != bucket {
					break
				}
				end++
			}
		}
		if end-start >= 2 {
			group := channel.Part{}
			for _, part := range parts[start:end] {
				group.Group = append(group.Group, *part.File)
			}
			out = append(out, group)
		} else {
			out = append(out, parts[start])
		}
		start = end
	}
	return out
}

func (b *Bot) sendGroup(ctx context.Context, peer string, files []channel.File) (channel.Receipt, error) {
	chatID, err := peerID(peer)
	if err != nil {
		return channel.Receipt{}, err
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return channel.Receipt{}, err
	}
	type member struct {
		Type    string `json:"type"`
		Media   string `json:"media"`
		Caption string `json:"caption"`
	}
	media := make([]member, 0, len(files))
	for i, file := range files {
		_, kind := fileMethod(&file)
		name := "file" + strconv.Itoa(i)
		media = append(media, member{Type: kind, Media: "attach://" + name, Caption: file.Name})
		payload, err := form.CreateFormFile(name, file.Name)
		if err != nil {
			return channel.Receipt{}, err
		}
		if _, err := payload.Write(file.Body); err != nil {
			return channel.Receipt{}, err
		}
	}
	encoded, err := json.Marshal(media)
	if err != nil {
		return channel.Receipt{}, err
	}
	if err := form.WriteField("media", string(encoded)); err != nil {
		return channel.Receipt{}, err
	}
	if err := form.Close(); err != nil {
		return channel.Receipt{}, err
	}
	var result []struct {
		ID int64 `json:"message_id"`
	}
	if err := b.request(ctx, "sendMediaGroup", &body, form.FormDataContentType(), &result); err != nil {
		if api, ok := errors.AsType[*apiError](err); ok && api.Code == 400 {
			return channel.Receipt{}, fmt.Errorf("%w: %w", channel.ErrGroupRejected, err)
		}
		if api, ok := errors.AsType[*apiError](err); ok && (api.Code == 403 || api.Code == 429) {
			return channel.Receipt{}, err
		}
		return channel.Receipt{}, fmt.Errorf("telegram media group outcome unknown: %w", err)
	}
	// A missing or invalid ID makes the whole result unknown. Never switch methods or confirm a prefix.
	if len(result) != len(files) {
		return channel.Receipt{}, errors.New("telegram media group outcome unknown: incomplete message IDs")
	}
	receipt := channel.Receipt{}
	seen := map[int64]bool{}
	for _, message := range result {
		if message.ID <= 0 || seen[message.ID] {
			return channel.Receipt{}, errors.New("telegram media group outcome unknown: invalid message IDs")
		}
		seen[message.ID] = true
		receipt.MessageIDs = append(receipt.MessageIDs, strconv.FormatInt(message.ID, 10))
	}
	return receipt, nil
}
