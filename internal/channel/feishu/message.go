package feishu

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
)

// resource names one attachment from a message.
type resource struct{ Key, Name, Type string }

// message is an inbound private message before owner authorization.
type message struct {
	ID, Sender, ChatType, Text string
	// ReplyTo is the ID of the message that this message quotes. It is empty for a plain message.
	ReplyTo   string
	Resources []resource
	PlainText bool
}

// parseMessage extracts the supported content without retaining platform data.
func parseMessage(data []byte) (message, error) {
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
				ParentID string `json:"parent_id"`
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
		return message{}, err
	}
	if event.Header.Type != "im.message.receive_v1" || event.Event.Sender.Type != "user" {
		return message{}, nil
	}
	raw := event.Event.Message
	m := message{ID: raw.ID, Sender: event.Event.Sender.ID.OpenID, ChatType: raw.ChatType, ReplyTo: raw.ParentID}
	if m.ID == "" || m.Sender == "" {
		return message{}, errors.New("feishu message has no ID or sender")
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
			m.PlainText = true
		case "image":
			if content.ImageKey == "" {
				return m, errors.New("image has no key")
			}
			m.Resources = []resource{{content.ImageKey, "image.png", "image"}}
		case "file":
			if content.FileKey == "" || content.FileName == "" {
				return m, errors.New("file has no key or name")
			}
			m.Resources = []resource{{content.FileKey, content.FileName, "file"}}
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
		Language string `json:"language"`
		Text     string `json:"text"`
		Href     string `json:"href"`
		ImageKey string `json:"image_key"`
	} `json:"content"`
}

func parsePost(data []byte) (string, []resource, error) {
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
	var resources []resource
	for _, line := range p.Content {
		if text.Len() > 0 {
			text.WriteByte('\n')
		}
		for _, part := range line {
			switch part.Tag {
			case "text", "md":
				text.WriteString(part.Text)
			case "code_block":
				fmt.Fprintf(&text, "\n```%s\n%s\n```\n", part.Language, part.Text)
			case "a":
				text.WriteString(part.Text)
				if part.Href != "" {
					fmt.Fprintf(&text, " (%s)", part.Href)
				}
			case "img":
				if part.ImageKey != "" {
					resources = append(resources, resource{part.ImageKey, "image.png", "image"})
				}
			}
		}
	}
	if len(resources) > 10 {
		return "", nil, errors.New("post has more than 10 attachments")
	}
	return text.String(), resources, nil
}

// cardContent has no buttons or tool data. Text is one platform-sized part.
func cardContent(text string, failed bool) string {
	color := "blue"
	if failed {
		color = "red"
	}
	card := map[string]any{"config": map[string]bool{"update_multi": true}, "header": map[string]any{"template": color, "title": map[string]string{"tag": "plain_text", "content": "dex"}}, "elements": []any{map[string]string{"tag": "markdown", "content": text}}}
	b, _ := json.Marshal(card)
	return string(b)
}
