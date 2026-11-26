package email

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	tasksvc "github.com/recally-io/dex/internal/task"
)

// EmailReceived is the event type of a forwarded email.
const EmailReceived = "email.received"

// parseEmail turns one raw message into an event. The Message-ID, In-Reply-To, and References
// headers become message objects, so a reply finds the Task of its thread.
// The From header is part of the text only. Anyone can forge it, so it is not an author.
func parseEmail(body []byte, now time.Time) (payload, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(body))
	if err != nil {
		return payload{}, err
	}
	own := messageIDs(msg.Header.Get("Message-ID"))
	if len(own) != 1 {
		return payload{}, errors.New("the message needs one Message-ID")
	}
	// The thread references come newest first, so the cap of 16 objects keeps the closest messages.
	refs := append(messageIDs(msg.Header.Get("In-Reply-To")), reverse(messageIDs(msg.Header.Get("References")))...)
	seen := map[string]bool{}
	var objects []tasksvc.Object
	for _, id := range append(own, refs...) {
		key := objectKey(id)
		if !seen[key] && len(objects) < 16 {
			seen[key] = true
			objects = append(objects, tasksvc.Object{Type: "message", Key: key})
		}
	}
	decoder := new(mime.WordDecoder)
	subject, err := decoder.DecodeHeader(msg.Header.Get("Subject"))
	if err != nil {
		subject = msg.Header.Get("Subject")
	}
	from, err := decoder.DecodeHeader(msg.Header.Get("From"))
	if err != nil {
		from = msg.Header.Get("From")
	}
	text, err := plainText(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), msg.Body, 0)
	if err != nil {
		return payload{}, err
	}
	occurred, err := msg.Header.Date()
	if err != nil {
		occurred = now
	}
	subject = strings.TrimSpace(strings.ToValidUTF8(subject, ""))
	content := strings.ToValidUTF8("Subject: "+subject+"\nFrom: "+from+"\n\n"+strings.TrimSpace(text), "")
	return payload{
		Type: EmailReceived, ChangeKey: "message:" + objects[0].Key, Objects: objects,
		Title: truncate(subject, 160), Content: truncate(content, 32000), OccurredAt: occurred,
	}, nil
}

func messageIDs(header string) []string {
	var ids []string
	for field := range strings.FieldsSeq(header) {
		if id := strings.Trim(field, "<>,"); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func reverse(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[len(ids)-1-i] = id
	}
	return out
}

// objectKey keeps a valid Message-ID and hashes one that is too long or not valid UTF-8.
func objectKey(id string) string {
	if len(id) <= 200 && utf8.ValidString(id) {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func truncate(s string, runes int) string {
	if utf8.RuneCountInString(s) <= runes {
		return s
	}
	return string([]rune(s)[:runes])
}

// plainText returns the first text/plain part. Other parts, such as HTML and files, stay out.
func plainText(contentType, encoding string, body io.Reader, depth int) (string, error) {
	media, params, err := mime.ParseMediaType(contentType)
	if contentType == "" || err != nil {
		media = "text/plain"
	}
	if strings.HasPrefix(media, "multipart/") && depth < 5 {
		parts := multipart.NewReader(body, params["boundary"])
		for {
			part, err := parts.NextRawPart()
			if errors.Is(err, io.EOF) {
				return "", nil
			}
			if err != nil {
				return "", err
			}
			text, err := plainText(part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), part, depth+1)
			if err != nil || text != "" {
				return text, err
			}
		}
	}
	if media != "text/plain" {
		return "", nil
	}
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "quoted-printable":
		body = quotedprintable.NewReader(body)
	case "base64":
		body = base64.NewDecoder(base64.StdEncoding, &newlineStripper{r: body})
	}
	raw, err := io.ReadAll(io.LimitReader(body, MaxPayload))
	return string(raw), err
}

// newlineStripper removes line breaks, which base64 bodies of email contain.
type newlineStripper struct{ r io.Reader }

func (n *newlineStripper) Read(p []byte) (int, error) {
	for {
		c, err := n.r.Read(p)
		kept := 0
		for _, b := range p[:c] {
			if b != '\r' && b != '\n' {
				p[kept] = b
				kept++
			}
		}
		if kept > 0 || err != nil {
			return kept, err
		}
	}
}
