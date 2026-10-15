package feishu

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
)

// Resource names one attachment from a message.
type Resource struct{ Key, Name, Type string }

// Message is an inbound private message before owner authorization.
type Message struct {
	ID, Sender, ChatType, Text string
	Resources                  []Resource
}

// ParseMessage extracts the supported content without retaining platform data.
func ParseMessage(data []byte) (Message, error) {
	var event struct {
		Header struct {
			Type string `json:"event_type"`
		} `json:"header"`
		Event struct {
			Sender struct {
				ID struct {
					OpenID string `json:"open_id"`
				} `json:"sender_id"`
				Type string `json:"sender_type"`
			} `json:"sender"`
			Message struct {
				ID       string `json:"message_id"`
				ChatType string `json:"chat_type"`
				Type     string `json:"message_type"`
				Content  string `json:"content"`
				Mentions []struct {
					Key string `json:"key"`
				} `json:"mentions"`
			} `json:"message"`
		} `json:"event"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return Message{}, err
	}
	if event.Header.Type != "im.message.receive_v1" || event.Event.Sender.Type != "user" {
		return Message{}, nil
	}
	raw := event.Event.Message
	m := Message{ID: raw.ID, Sender: event.Event.Sender.ID.OpenID, ChatType: raw.ChatType}
	if m.ID == "" || m.Sender == "" {
		return Message{}, errors.New("feishu message has no ID or sender")
	}
	if m.ChatType != "p2p" {
		return m, nil
	}
	var content struct {
		Text     string `json:"text"`
		ImageKey string `json:"image_key"`
		FileKey  string `json:"file_key"`
		FileName string `json:"file_name"`
	}
	switch raw.Type {
	case "text", "image", "file":
		if err := json.Unmarshal([]byte(raw.Content), &content); err != nil {
			return m, err
		}
		switch raw.Type {
		case "text":
			m.Text = content.Text
		case "image":
			if content.ImageKey == "" {
				return m, errors.New("image has no key")
			}
			m.Resources = []Resource{{content.ImageKey, "image.png", "image"}}
		case "file":
			if content.FileKey == "" || content.FileName == "" {
				return m, errors.New("file has no key or name")
			}
			m.Resources = []Resource{{content.FileKey, content.FileName, "file"}}
		}
	case "post":
		text, resources, err := parsePost([]byte(raw.Content))
		if err != nil {
			return m, err
		}
		m.Text = text
		m.Resources = resources
	default:
		return m, nil
	}
	for _, mention := range raw.Mentions {
		if mention.Key != "" {
			m.Text = strings.ReplaceAll(m.Text, mention.Key, "")
		}
	}
	m.Text = strings.TrimSpace(m.Text)
	return m, nil
}

type post struct {
	Title   string `json:"title"`
	Content [][]struct {
		Tag      string `json:"tag"`
		Text     string `json:"text"`
		Href     string `json:"href"`
		ImageKey string `json:"image_key"`
	} `json:"content"`
}

func parsePost(data []byte) (string, []Resource, error) {
	var p post
	if err := json.Unmarshal(data, &p); err != nil {
		return "", nil, err
	}
	if p.Content == nil {
		var locales map[string]jsontext.Value
		if err := json.Unmarshal(data, &locales); err != nil {
			return "", nil, err
		}
		// Prefer the platform's usual locales; select one to avoid duplicate input.
		for _, locale := range []string{"zh_cn", "en_us", "ja_jp"} {
			if b := locales[locale]; b != nil {
				if err := json.Unmarshal(b, &p); err != nil {
					return "", nil, err
				}
				break
			}
		}
		if p.Content == nil {
			for _, b := range locales {
				if err := json.Unmarshal(b, &p); err != nil {
					return "", nil, err
				}
				break
			}
		}
	}
	var text strings.Builder
	text.WriteString(p.Title)
	var resources []Resource
	for _, line := range p.Content {
		if text.Len() > 0 {
			text.WriteByte('\n')
		}
		for _, part := range line {
			switch part.Tag {
			case "text", "md":
				text.WriteString(part.Text)
			case "a":
				text.WriteString(part.Text)
				if part.Href != "" {
					fmt.Fprintf(&text, " (%s)", part.Href)
				}
			case "img":
				if part.ImageKey != "" {
					resources = append(resources, Resource{part.ImageKey, "image.png", "image"})
				}
			}
		}
	}
	if len(resources) > 10 {
		return "", nil, errors.New("post has more than 10 attachments")
	}
	return text.String(), resources, nil
}

// CardContent has no buttons or tool data. Text is one platform-sized part.
func CardContent(text string, failed bool) string {
	color := "blue"
	if failed {
		color = "red"
	}
	card := map[string]any{"config": map[string]bool{"update_multi": true}, "header": map[string]any{"template": color, "title": map[string]string{"tag": "plain_text", "content": "dex"}}, "elements": []any{map[string]string{"tag": "markdown", "content": text}}}
	b, _ := json.Marshal(card)
	return string(b)
}
