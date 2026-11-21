package email

import (
	"strings"
	"testing"
	"time"
)

func TestUserEmailBecomesAThreadEvent(t *testing.T) {
	now := time.Date(2026, 11, 21, 5, 0, 0, 0, time.UTC)
	long := strings.Repeat("x", 300) + "@example.com"
	cases := []struct {
		name, raw  string
		objects    []string
		title      string
		contains   []string
		excludes   []string
		occurredAt time.Time
	}{
		{
			name: "a plain first message",
			raw: "Message-ID: <a1@mail.example>\r\nFrom: Ann <ann@example.com>\r\nSubject: Contract review\r\n" +
				"Date: Fri, 21 Nov 2026 03:00:00 +0000\r\n\r\nPlease check section 2.\r\n",
			objects: []string{"a1@mail.example"}, title: "Contract review",
			contains:   []string{"Subject: Contract review", "From: Ann <ann@example.com>", "Please check section 2."},
			occurredAt: time.Date(2026, 11, 21, 3, 0, 0, 0, time.UTC),
		},
		{
			name: "a multipart reply with an encoded subject",
			raw: "Message-ID: <a3@mail.example>\r\nIn-Reply-To: <a2@mail.example>\r\nReferences: <a1@mail.example> <a2@mail.example>\r\n" +
				"Subject: =?UTF-8?B?UmU6IOWQiOWQjA==?=\r\nMIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=\"b\"\r\n\r\n" +
				"--b\r\nContent-Type: text/html\r\n\r\n<p>HTML only</p>\r\n" +
				"--b\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nSigned =E2=9C=93\r\n--b--\r\n",
			objects: []string{"a3@mail.example", "a2@mail.example", "a1@mail.example"}, title: "Re: 合同",
			contains: []string{"Signed ✓"}, excludes: []string{"HTML only"}, occurredAt: now,
		},
		{
			name: "a base64 body and a long reference",
			raw: "Message-ID: <b1@mail.example>\r\nReferences: <" + long + ">\r\nSubject: Long\r\n" +
				"Content-Transfer-Encoding: base64\r\n\r\nSGVsbG8g\r\nd29ybGQ=\r\n",
			objects:  []string{"b1@mail.example", "sha256:"},
			title:    "Long",
			contains: []string{"Hello world"}, occurredAt: now,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := parseEmail([]byte(c.raw), now)
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Objects) != len(c.objects) {
				t.Fatalf("objects: %+v", p.Objects)
			}
			for i, want := range c.objects {
				if got := p.Objects[i]; got.Type != "message" || !strings.HasPrefix(got.Key, want) || len(got.Key) > 512 {
					t.Fatalf("object %d: %+v, want %s", i, got, want)
				}
			}
			if p.Type != EmailReceived || p.ChangeKey != "message:"+p.Objects[0].Key || p.Title != c.title || !p.OccurredAt.Equal(c.occurredAt) {
				t.Fatalf("event: %+v", p)
			}
			for _, want := range c.contains {
				if !strings.Contains(p.Content, want) {
					t.Fatalf("content lacks %q: %q", want, p.Content)
				}
			}
			for _, unwanted := range c.excludes {
				if strings.Contains(p.Content, unwanted) {
					t.Fatalf("content has %q: %q", unwanted, p.Content)
				}
			}
		})
	}
	if _, err := parseEmail([]byte("Subject: none\r\n\r\nbody"), now); err == nil {
		t.Fatal("a message without a Message-ID must fail")
	}
}
