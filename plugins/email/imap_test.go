package email

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/plugin"
	"github.com/recally-io/dex/internal/worktask"
	"github.com/recally-io/dex/plugins/email/store"
)

// imapFolder is a real IMAP server in memory. Plain IMAP is allowed only to a loopback address.
type imapFolder struct {
	t      *testing.T
	user   *imapmemserver.User
	mem    *imapmemserver.Server
	addr   string
	server *imapserver.Server
}

func startIMAP(t *testing.T) *imapFolder {
	t.Helper()
	f := &imapFolder{t: t, mem: imapmemserver.New(), user: imapmemserver.NewUser("owner", "app-password")}
	if err := f.user.Create("Dex", nil); err != nil {
		t.Fatal(err)
	}
	f.mem.AddUser(f.user)
	f.serve("127.0.0.1:0")
	return f
}

func (f *imapFolder) serve(addr string) {
	f.t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(f.t.Context(), "tcp", addr)
	if err != nil {
		f.t.Fatal(err)
	}
	f.addr = ln.Addr().String()
	f.server = imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return f.mem.NewSession(), nil, nil
		},
		InsecureAuth: true,
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}},
	})
	server := f.server
	go func() { _ = server.Serve(ln) }()
	f.t.Cleanup(func() { _ = server.Close() })
}

func (f *imapFolder) append(raw string) {
	f.t.Helper()
	if _, err := f.user.Append("Dex", bytes.NewReader([]byte(raw)), &imap.AppendOptions{}); err != nil {
		f.t.Fatal(err)
	}
}

// seen reports whether any message in the folder has the \Seen flag.
func (f *imapFolder) seen() bool {
	f.t.Helper()
	c, err := imapclient.DialInsecure(f.addr, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	defer c.Close()
	if err := c.Login("owner", "app-password").Wait(); err != nil {
		f.t.Fatal(err)
	}
	if _, err := c.Select("Dex", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		f.t.Fatal(err)
	}
	msgs, err := c.Fetch(imap.SeqSet{{Start: 1, Stop: 0}}, &imap.FetchOptions{Flags: true}).Collect()
	if err != nil {
		f.t.Fatal(err)
	}
	for _, m := range msgs {
		if slices.Contains(m.Flags, imap.FlagSeen) {
			return true
		}
	}
	return false
}

func message(id, refs, subject, text string) string {
	header := "Message-ID: <" + id + ">\r\nFrom: Ann <ann@example.com>\r\nSubject: " + subject + "\r\nDate: Fri, 21 Nov 2026 03:00:00 +0000\r\n"
	if refs != "" {
		header += "References: " + refs + "\r\n"
	}
	return header + "\r\n" + text + "\r\n"
}

func TestUserPullsAnEmailThreadFromAnIMAPFolder(t *testing.T) {
	ctx := t.Context()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := plugin.Migrate(ctx, conn, "email", Migrations); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlc.New(conn).CreateAgent(ctx, sqlc.CreateAgentParams{ID: "default", Name: "Dex", ThinkingLevel: "off"}); err != nil {
		t.Fatal(err)
	}
	q := store.New(conn)
	tasks := worktask.New(conn, nil)
	secret := strings.Repeat("m", 40)
	if _, err := q.SaveEmailSource(ctx, store.SaveEmailSourceParams{ID: "mail", Secret: secret, TaskAgentID: sqlNull("default"), HoldUnmatched: 1}); err != nil {
		t.Fatal(err)
	}
	folder := startIMAP(t)
	cursor := func() (int64, int64) {
		t.Helper()
		var validity, last int64
		if err := conn.QueryRowContext(ctx, "SELECT uid_validity, last_uid FROM plugin_email_imap_pulls WHERE source_id = 'mail'").Scan(&validity, &last); err != nil {
			t.Fatal(err)
		}
		return validity, last
	}
	// Each run uses a new puller, as after a restart. Only the stored cursor carries over.
	check := func() { (&puller{q: q, tasks: tasks}).Check(ctx) }

	// Given a folder with an old message when the owner saves the Pull.
	folder.append(message("old@x", "", "Old thread", "Before Pull."))
	if n, err := q.SaveEmailIMAPPull(ctx, store.SaveEmailIMAPPullParams{SourceID: "mail", Address: "imap://" + folder.addr, Username: "owner", Password: "app-password", Folder: "Dex", ConfigurationID: "c1"}); err != nil || n != 1 {
		t.Fatalf("save pull: %d %v", n, err)
	}
	check()
	if validity, last := cursor(); validity == 0 || last != 1 {
		t.Fatalf("the first run must start after the old message: %d %d", validity, last)
	}

	// When a new thread, a message without a Message-ID, a message above the size limit, and a
	// reply arrive, and the forwarder also posts the reply to the hook.
	folder.append(message("a1@x", "", "Contract review", "Please check section 2."))
	folder.append("Subject: broken\r\n\r\nNo Message-ID.\r\n")
	folder.append(message("big@x", "", "Big", strings.Repeat("x", MaxPayload)))
	reply := message("a2@x", "<a1@x>", "Re: Contract review", "I approve.")
	folder.append(reply)
	check()
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(reply))
	if _, err := Accept(ctx, tasks, Source{ID: "mail", Secret: []byte(secret), TaskAgentID: "default", HoldUnmatched: true}, "fwd-1", "sha256="+hex.EncodeToString(mac.Sum(nil)), []byte(reply)); err != nil {
		t.Fatal(err)
	}
	check()

	// Then one Task follows the thread, with one entry per message.
	list, err := tasks.List(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Title != "Contract review" {
		t.Fatalf("only the new thread must open a Task: %+v", list)
	}
	detail, err := tasks.Detail(ctx, "default", list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("artifact: entries=%+v", detail.Entries)
	if len(detail.Entries) != 2 || !strings.Contains(detail.Entries[0].Content, "I approve.") {
		t.Fatalf("the reply must add one entry, from Pull or the hook: %+v", detail.Entries)
	}
	// The bad messages did not stop the folder, and Pull did not mark anything as read.
	if _, last := cursor(); last != 5 {
		t.Fatalf("the cursor must pass the bad messages: %d", last)
	}
	if folder.seen() {
		t.Fatal("Pull must open the folder read-only")
	}

	// When the server is down, the cursor stays, and the next run reads the message.
	folder.append(message("a3@x", "<a1@x> <a2@x>", "Re: Contract review", "Signed."))
	addr := folder.addr
	_ = folder.server.Close()
	check()
	if _, last := cursor(); last != 5 {
		t.Fatalf("a failed run must keep the cursor: %d", last)
	}
	folder.serve(addr)
	check()
	if _, last := cursor(); last != 6 {
		t.Fatalf("the next run must read the message: %d", last)
	}
	detail, err = tasks.Detail(ctx, "default", list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Entries) != 3 {
		t.Fatalf("the message after the outage must reach the Task once: %+v", detail.Entries)
	}
}

func TestUserIMAPAddressMustUseTLSOffLoopback(t *testing.T) {
	for addr, ok := range map[string]bool{
		"imaps://imap.example.com":      true,
		"imaps://imap.example.com:993":  true,
		"imap://127.0.0.1:1143":         true,
		"imap://localhost":              true,
		"imap://imap.example.com:143":   false,
		"imaps://user@imap.example.com": false,
		"https://imap.example.com":      false,
		"imap.example.com:993":          false,
	} {
		if _, err := parseIMAPAddress(addr); (err == nil) != ok {
			t.Errorf("%s: err=%v, want ok=%v", addr, err, ok)
		}
	}
}

func sqlNull(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }
