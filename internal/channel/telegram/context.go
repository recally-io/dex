package telegram

import (
	"cmp"
	"fmt"
	"strconv"
	"strings"

	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/session"
)

func (m *message) inbound(content string, edited bool, attachments []engine.Attachment, identity session.ChannelContext) session.Message {
	if m.ForwardOrigin != nil {
		content = "Forwarded message:\n" + content
	}
	if edited {
		identity.Edited = true
		content = fmt.Sprintf("Correction to Telegram message %d (earlier actions are not undone):\n%s", m.ID, content)
	}
	msg := session.Message{Content: content, Attachments: attachments, ChannelNote: m.contextNote(), ChannelContext: identity}
	if m.ReplyTo != nil {
		msg.ReplyTo = strconv.FormatInt(m.ReplyTo.ID, 10)
	}
	return msg
}

func (m *message) selectedMedia() (*media, string) {
	for _, candidate := range []struct {
		file *media
		kind string
	}{
		{m.Voice, "Voice"}, {m.Audio, "Audio"}, {m.Video, "Video"}, {m.VideoNote, "Video note"}, {m.Animation, "Animation"}, {m.Sticker, "Sticker"},
	} {
		if candidate.file != nil {
			return candidate.file, candidate.kind
		}
	}
	selected, kind := m.Document, "Document"
	for i := range m.Photo {
		photo := &m.Photo[i]
		if selected == nil || photo.Width*photo.Height > selected.Width*selected.Height {
			selected, kind = photo, "Photo"
		}
	}
	return selected, kind
}

func (m *message) hasContent() bool {
	file, _ := m.selectedMedia()
	return m.Text != "" || m.Caption != "" || len(m.Rich) > 0 || file != nil || m.Location != nil || m.Contact != nil || m.Poll != nil
}

func authorName(u *user) string {
	if u == nil {
		return "unknown sender"
	}
	name := cmp.Or(strings.Join(strings.Fields(u.FirstName+" "+u.LastName), " "), u.Username, strconv.FormatInt(u.ID, 10))
	return name
}

// contextNote keeps quoted identities as data. Only the outer From field grants entry.
func (m *message) contextNote() string {
	var notes []string
	if file, kind := m.selectedMedia(); file != nil && kind != "Photo" && kind != "Document" {
		note := "[" + kind + " message]"
		if file.Duration > 0 {
			note += fmt.Sprintf(" Duration: %d seconds.", file.Duration)
		}
		switch kind {
		case "Voice", "Audio":
			note += " Automatic transcription is not configured. The original audio file is attached."
		case "Video", "Video note":
			note += " The video file is attached. No automatic frame extraction is configured."
		case "Animation":
			note += " The animation file is attached."
		case "Sticker":
			if file.Emoji != "" {
				note += " Emoji: " + file.Emoji + "."
			}
			if file.SetName != "" {
				note += " Set: " + file.SetName + "."
			}
			if file.IsAnimated || file.IsVideo {
				note += " Animated sticker kept as a file; no automatic decoder is configured."
			}
		}
		notes = append(notes, note)
	}
	if m.Location != nil {
		notes = append(notes, fmt.Sprintf("Location: latitude %g, longitude %g.", m.Location.Latitude, m.Location.Longitude))
	}
	if m.Contact != nil {
		notes = append(notes, "Contact: "+strings.TrimSpace(m.Contact.FirstName+" "+m.Contact.LastName)+"; phone "+m.Contact.Phone+".")
	}
	if m.Poll != nil {
		var poll strings.Builder
		poll.WriteString("Poll: " + m.Poll.Question)
		for _, option := range m.Poll.Options {
			_, _ = fmt.Fprintf(&poll, "\n- %s (%d votes)", option.Text, option.Votes)
		}
		notes = append(notes, poll.String())
	}
	if m.ReplyTo != nil || m.Quote != nil {
		text, name, id := "", "unknown sender", int64(0)
		if reply := m.ReplyTo; reply != nil {
			name, id = authorName(reply.From), reply.ID
			text = reply.body()
			if text == "" {
				if file, kind := reply.selectedMedia(); file != nil {
					text = "[" + kind + ": " + file.Name + "]"
				}
			}
		}
		if m.Quote != nil && m.Quote.Text != "" {
			text = m.Quote.Text
		}
		if text != "" {
			notes = append(notes, fmt.Sprintf("Reply context from %s (message %d):\n> %s", name, id, strings.ReplaceAll(text, "\n", "\n> ")))
		}
	}
	if origin := m.ForwardOrigin; origin != nil {
		name := cmp.Or(origin.SenderUserName, authorName(origin.SenderUser))
		if origin.Chat != nil {
			name = cmp.Or(origin.Chat.Title, strconv.FormatInt(origin.Chat.ID, 10))
		}
		notes = append(notes, "Forward context: "+origin.Type+" from "+name+".")
	}
	return strings.Join(notes, "\n\n")
}
