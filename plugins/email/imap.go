package email

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	tasksvc "github.com/recally-io/dex/internal/task"
	"github.com/recally-io/dex/plugins/email/store"
)

const (
	// imapBatch caps the messages of one run, so a large folder cannot hold the job for long.
	imapBatch = 50
	// imapDeadline bounds one run against one server, including a server that stops answering.
	imapDeadline = 2 * time.Minute
)

// parseIMAPAddress accepts imaps://host[:port], or imap://host[:port] for a loopback host only.
// Plain IMAP to loopback serves a local bridge, such as Proton Mail Bridge; it never leaves the machine.
func parseIMAPAddress(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("use imaps://host:port")
	}
	switch u.Scheme {
	case "imaps":
		if u.Port() == "" {
			u.Host = net.JoinHostPort(u.Hostname(), "993")
		}
	case "imap":
		if !loopback(u.Hostname()) {
			return nil, errors.New("use imaps:// for a host that is not loopback")
		}
		if u.Port() == "" {
			u.Host = net.JoinHostPort(u.Hostname(), "143")
		}
	default:
		return nil, errors.New("use imaps://host:port")
	}
	return u, nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

type puller struct {
	q     *store.Queries
	tasks *tasksvc.Service
}

// Check reads new messages of each configured folder. A failure of one source does not stop the others.
func (p *puller) Check(ctx context.Context) {
	pulls, err := p.q.ListEmailIMAPPulls(ctx)
	if err != nil {
		slog.Error("list email IMAP pulls", "err", err)
		return
	}
	for _, pull := range pulls {
		if err := p.pull(ctx, pull); err != nil && ctx.Err() == nil {
			// The error never holds the password: Login reports only the server response.
			slog.Warn("pull email over IMAP", "source", pull.SourceID, "err", err)
		}
	}
}

func (p *puller) pull(ctx context.Context, pull store.ListEmailIMAPPullsRow) error {
	ctx, cancel := context.WithTimeout(ctx, imapDeadline)
	defer cancel()
	c, err := dialIMAP(ctx, pull.Address)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Login(pull.Username, pull.Password).Wait(); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	// EXAMINE opens the folder read-only, so Pull does not change flags such as \Seen.
	box, err := c.Select(pull.Folder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return fmt.Errorf("examine %q: %w", pull.Folder, err)
	}
	if box.UIDValidity == 0 || box.UIDNext == 0 {
		return errors.New("the server does not report UIDVALIDITY and UIDNEXT")
	}
	if box.UIDNext == 0 || box.UIDValidity == 0 {
		return errors.New("the server did not report UIDNEXT and UIDVALIDITY")
	}
	cursor := store.SetEmailIMAPCursorParams{SourceID: pull.SourceID, ConfigurationID: pull.ConfigurationID, UidValidity: int64(box.UIDValidity), LastUid: pull.LastUid}
	if pull.UidValidity != int64(box.UIDValidity) {
		// A new configuration, or a folder that the server renumbered, starts at the next message.
		// Old messages would open one Task each and flood Main with Task alerts.
		cursor.LastUid = int64(box.UIDNext) - 1
		return p.q.SetEmailIMAPCursor(ctx, cursor)
	}
	if int64(box.UIDNext)-1 <= pull.LastUid {
		return nil
	}
	sizes, err := c.Fetch(imap.UIDSet{{Start: imap.UID(pull.LastUid + 1), Stop: 0}}, &imap.FetchOptions{UID: true, RFC822Size: true}).Collect() //nolint:gosec // The check above keeps LastUid below UIDNext, a uint32.
	if err != nil {
		return fmt.Errorf("fetch sizes: %w", err)
	}
	// "n:*" also returns the last message when its UID is below n.
	sizes = slices.DeleteFunc(sizes, func(m *imapclient.FetchMessageBuffer) bool { return int64(m.UID) <= pull.LastUid })
	slices.SortFunc(sizes, func(a, b *imapclient.FetchMessageBuffer) int { return int(a.UID) - int(b.UID) })
	source := Source{ID: pull.SourceID, TaskBotID: pull.TaskBotID.String, HoldUnmatched: pull.HoldUnmatched == 1}
	for _, m := range sizes[:min(len(sizes), imapBatch)] {
		if err := p.accept(ctx, c, source, box.UIDValidity, m); err != nil {
			return err
		}
		cursor.LastUid = int64(m.UID)
		if err := p.q.SetEmailIMAPCursor(ctx, cursor); err != nil {
			return err
		}
	}
	return c.Logout().Wait()
}

// accept stores one message. It returns nil for a message that can never be stored, so the
// cursor moves past it. It returns an error for a failure that a later run can fix.
func (p *puller) accept(ctx context.Context, c *imapclient.Client, source Source, validity uint32, m *imapclient.FetchMessageBuffer) error {
	if m.RFC822Size > MaxPayload {
		slog.Warn("skip an email above the size limit", "source", source.ID, "uid", m.UID, "size", m.RFC822Size)
		return nil
	}
	section := &imap.FetchItemBodySection{Peek: true}
	msgs, err := c.Fetch(imap.UIDSetNum(m.UID), &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{section}}).Collect()
	if err != nil {
		return fmt.Errorf("fetch message %d: %w", m.UID, err)
	}
	if len(msgs) != 1 {
		// The message was expunged after the size fetch.
		return nil
	}
	body := msgs[0].FindBodySection(section)
	_, err = accept(ctx, p.tasks, source, fmt.Sprintf("imap:%d:%d", validity, m.UID), body)
	if errors.Is(err, ErrPayload) || errors.Is(err, tasksvc.ErrInvalid) {
		slog.Warn("skip an email that is not a valid message", "source", source.ID, "uid", m.UID, "err", err)
		return nil
	}
	return err
}

func dialIMAP(ctx context.Context, address string) (*imapclient.Client, error) {
	u, err := parseIMAPAddress(address)
	if err != nil {
		return nil, err
	}
	conn, err := (&net.Dialer{Timeout: 30 * time.Second}).DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return nil, err
	}
	// The client has no command timeouts, so the connection deadline bounds the whole run.
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	context.AfterFunc(ctx, func() { _ = conn.Close() })
	if u.Scheme == "imaps" {
		tc := tls.Client(conn, &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12})
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, err
		}
		conn = tc
	}
	c := imapclient.New(conn, nil)
	if err := c.WaitGreeting(); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}
