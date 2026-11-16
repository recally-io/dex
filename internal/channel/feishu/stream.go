package feishu

import (
	"bufio"
	"context"
	"encoding/json/v2"
	"slices"
	"strings"
	"time"
)

type preview struct {
	messageID string
	cancel    context.CancelFunc
}
type streamItem struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Text   string `json:"text"`
	Queued bool   `json:"queued"`
}
type streamEvent struct {
	Type  string       `json:"type"`
	Items []streamItem `json:"items"`
	Item  streamItem   `json:"item"`
	ID    string       `json:"id"`
	Text  string       `json:"text"`
}

func (b *Bot) stream(ctx context.Context, id string, chatID int64, owner string, p *preview) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, err := b.hub.Open(ctx, chatID)
	if err != nil {
		return
	}
	defer func() { _ = reader.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = reader.Close() })
	defer stop()
	events := make(chan streamEvent, 32)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(events)
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 4096), 4<<20)
		for scanner.Scan() {
			line := scanner.Text()
			raw, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			var event streamEvent
			if err := json.Unmarshal([]byte(raw), &event); err != nil {
				return
			}
			select {
			case events <- event:
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() { cancel(); _ = reader.Close(); <-done }()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var items []streamItem
	latest := "Thinking…"
	sent := ""
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			switch event.Type {
			case "snapshot":
				items = slices.DeleteFunc(event.Items, func(item streamItem) bool { return item.Kind == "user" && item.Queued })
			case "item":
				if event.Item.Kind == "user" && event.Item.Queued {
					continue
				}
				found := false
				for i := range items {
					if items[i].ID == event.Item.ID {
						items[i] = event.Item
						found = true
						break
					}
				}
				if !found {
					items = append(items, event.Item)
				}
			case "append":
				for i := range items {
					if items[i].ID == event.ID {
						items[i].Text += event.Text
						break
					}
				}
			}
			active := false
			var text strings.Builder
			for _, item := range items {
				if item.Kind == "user" || item.Kind == "task" || item.Kind == "boundary" {
					if active {
						break
					}
					active = item.ID == id
					continue
				}
				if active && item.Kind == "assistant" {
					if text.Len() > 0 {
						text.WriteString("\n\n")
					}
					text.WriteString(item.Text)
				}
			}
			if text.Len() > 0 {
				parts := Parts(text.String())
				latest = parts[0]
			}
		case <-ticker.C:
			if latest == sent {
				continue
			}
			b.mu.Lock()
			if ctx.Err() != nil || b.previews[id] != p {
				b.mu.Unlock()
				return
			}
			if p.messageID == "" {
				p.messageID, err = b.client.Send(ctx, owner, "interactive", CardContent(latest, false), deliveryUUID(id+":preview", 0))
			} else {
				err = b.client.Patch(ctx, p.messageID, CardContent(latest, false))
			}
			b.mu.Unlock()
			if err != nil {
				return
			}
			sent = latest
		}
	}
}
