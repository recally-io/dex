package telegram

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json/v2"
	"io"
	"slices"
	"strings"
	"time"
)

type preview struct {
	cancel    context.CancelFunc
	done      chan struct{}
	messageID int64
}

type streamItem struct {
	Queued bool   `json:"queued"`
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Text   string `json:"text"`
}
type streamEvent struct {
	Type  string       `json:"type"`
	ID    string       `json:"id"`
	Text  string       `json:"text"`
	Item  streamItem   `json:"item"`
	Items []streamItem `json:"items"`
}

// preview reads the same native watch as the web UI. Final answers use durable delivery.
func (b *Bot) startPreview(ctx context.Context, key string, chatID int64) {
	if b.cfg.Watch == nil {
		return
	}
	b.previewMu.Lock()
	defer b.previewMu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	receipt, err := b.chats.Source(ctx, key)
	if err != nil || !receipt.Pending {
		cancel()
		return
	}
	id := receipt.ID
	reader, err := b.cfg.Watch(ctx, receipt.ChatID)
	if err != nil || reader == nil {
		cancel()
		return
	}
	if b.previews[id] != nil {
		_ = reader.Close()
		cancel()
		return
	}
	p := &preview{cancel: cancel, done: make(chan struct{})}
	b.previews[id] = p
	go func() { defer close(p.done); b.watchPreview(ctx, id, chatID, p, reader) }()
}

func (b *Bot) watchPreview(ctx context.Context, id string, chatID int64, p *preview, reader io.ReadCloser) {
	var err error
	var random [4]byte
	_, _ = rand.Read(random[:])
	draftID := int(binary.BigEndian.Uint32(random[:])&0x3fffffff) + 1
	_ = b.call(ctx, "sendChatAction", map[string]any{"chat_id": chatID, "action": "typing"}, nil)
	defer func() { _ = reader.Close() }()
	events := make(chan streamEvent)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer close(events)
		scan := bufio.NewScanner(reader)
		scan.Buffer(make([]byte, 4096), 4<<20)
		for scan.Scan() {
			data, ok := strings.CutPrefix(scan.Text(), "data: ")
			if !ok {
				continue
			}
			var ev streamEvent
			if json.Unmarshal([]byte(data), &ev) != nil {
				continue
			}
			select {
			case events <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() { _ = reader.Close(); <-readDone }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	typing := time.NewTicker(4 * time.Second)
	defer typing.Stop()
	var items []streamItem
	last := ""
	var lastSend time.Time
	drafts := true
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			switch ev.Type {
			case "snapshot":
				items = slices.DeleteFunc(ev.Items, func(item streamItem) bool { return item.Kind == "user" && item.Queued })
			case "item":
				if ev.Item.Kind == "user" && ev.Item.Queued {
					continue
				}
				found := false
				for i := range items {
					if items[i].ID == ev.Item.ID {
						items[i] = ev.Item
						found = true
						break
					}
				}
				if !found {
					items = append(items, ev.Item)
				}
			case "append":
				for i := range items {
					if items[i].ID == ev.ID {
						items[i].Text += ev.Text
						break
					}
				}
			}
		case <-typing.C:
			_ = b.call(ctx, "sendChatAction", map[string]any{"chat_id": chatID, "action": "typing"}, nil)
		case <-ticker.C:
			current := ""
			active := false
			for _, item := range items {
				if item.Kind == "user" || item.Kind == "task" || item.Kind == "boundary" {
					if active {
						break
					}
					active = item.ID == id
				} else if active && item.Kind == "assistant" {
					current += item.Text
				}
			}
			if current == "" {
				continue
			}
			current = split(current, 4000)[0]
			if current == last || time.Since(lastSend) < time.Second {
				continue
			}
			if drafts {
				err = b.call(ctx, "sendMessageDraft", map[string]any{"chat_id": chatID, "draft_id": draftID, "text": current}, nil)
				if err != nil {
					drafts = false
				}
			}
			if !drafts {
				params := map[string]any{"chat_id": chatID, "text": current}
				if p.messageID == 0 {
					var result struct {
						ID int64 `json:"message_id"`
					}
					err = b.call(ctx, "sendMessage", params, &result)
					if err == nil {
						p.messageID = result.ID
					}
				} else {
					params["message_id"] = p.messageID
					err = b.call(ctx, "editMessageText", params, nil)
				}
			}
			lastSend = time.Now()
			if err == nil {
				last = current
			}
		}
	}
}

func (b *Bot) stopPreview(key string) int64 {
	b.previewMu.Lock()
	p := b.previews[key]
	b.previewMu.Unlock()
	if p == nil {
		return 0
	}
	p.cancel()
	<-p.done
	return p.messageID
}

func (b *Bot) closePreviews() {
	b.previewMu.Lock()
	for _, p := range b.previews {
		p.cancel()
	}
	defer b.previewMu.Unlock()
	for _, p := range b.previews {
		<-p.done
	}
}

func (b *Bot) forgetPreview(id string) {
	b.previewMu.Lock()
	delete(b.previews, id)
	b.previewMu.Unlock()
}
