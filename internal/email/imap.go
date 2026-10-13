package email

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"time"

	"github.com/emersion/go-imap"
	imapid "github.com/emersion/go-imap-id"
	"github.com/emersion/go-imap/client"
)

type imapProvider struct {
	account Account
	dial    func(context.Context, string) (net.Conn, error)
}

// Bound the entire connection's response, including literals before parsing.
type boundedConn struct {
	net.Conn
	remaining int64
}

func (c *boundedConn) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		return 0, errors.New("mail response limit exceeded")
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, e := c.Conn.Read(p)
	c.remaining -= int64(n)
	return n, e
}
func dialTLS(ctx context.Context, address string) (net.Conn, error) {
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: &tls.Config{MinVersion: tls.VersionTLS12}}
	conn, e := d.DialContext(ctx, "tcp", address)
	if e != nil {
		return nil, errors.New("mail TLS connection failed")
	}
	deadline := time.Now().Add(30 * time.Second)
	if t, ok := ctx.Deadline(); ok && t.Before(deadline) {
		deadline = t
	}
	_ = conn.SetDeadline(deadline)
	return conn, nil
}
func (p *imapProvider) connect(ctx context.Context) (*client.Client, func(), error) {
	password, e := secret(p.account)
	if e != nil {
		return nil, nil, e
	}
	dial := p.dial
	if dial == nil {
		dial = dialTLS
	}
	conn, e := dial(ctx, "imap.163.com:993")
	if e != nil {
		return nil, nil, e
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	cleanup := func() { stop(); conn.Close() }
	c, e := client.New(&boundedConn{conn, 16 << 20})
	if e != nil {
		cleanup()
		return nil, nil, errors.New("IMAP greeting failed")
	}
	c.ErrorLog = discardLogger{}
	if e = c.Login(p.account.Address, password); e != nil {
		cleanup()
		return nil, nil, errors.New("IMAP authentication failed; check service enablement and authorization password")
	}
	if ok, e := c.Support("ID"); e != nil {
		cleanup()
		return nil, nil, errors.New("IMAP capability check failed")
	} else if ok {
		if _, e = imapid.NewClient(c).ID(imapid.ID{"name": "Agent-Host", "version": "1.0"}); e != nil {
			cleanup()
			return nil, nil, errors.New("IMAP client identification failed")
		}
	}
	return c, cleanup, nil
}

type discardLogger struct{}

func (discardLogger) Printf(string, ...interface{}) {}
func (discardLogger) Println(...interface{})        {}
func (p *imapProvider) Check(ctx context.Context) (any, error) {
	c, close, e := p.connect(ctx)
	if e != nil {
		return nil, e
	}
	defer close()
	_, e = c.Select("INBOX", true)
	if e != nil {
		return nil, errors.New("IMAP read-only inbox selection failed")
	}
	smtpStatus := "disabled"
	if p.account.SendEnabled {
		_, finish, err := p.smtpConnect(ctx)
		if err != nil {
			return nil, err
		}
		finish()
		smtpStatus = "authenticated"
	}
	return map[string]any{"account_id": p.account.ID, "imap": "authenticated", "smtp": smtpStatus, "checked_at": time.Now().UTC()}, nil
}
func (p *imapProvider) Folders(ctx context.Context) (any, error) {
	c, close, e := p.connect(ctx)
	if e != nil {
		return nil, e
	}
	defer close()
	ch := make(chan *imap.MailboxInfo)
	done := make(chan error, 1)
	go func() { done <- c.List("", "*", ch) }()
	out := []string{}
	truncated := false
	for f := range ch {
		if len(out) < 200 {
			out = append(out, f.Name)
		} else {
			truncated = true
		}
	}
	if <-done != nil {
		return nil, errors.New("IMAP folder listing failed")
	}
	return map[string]any{"account_id": p.account.ID, "folders": out, "truncated": truncated}, nil
}
func (p *imapProvider) Search(ctx context.Context, a Args) (any, error) {
	c, close, e := p.connect(ctx)
	if e != nil {
		return nil, e
	}
	defer close()
	box, e := c.Select(a.Folder, true)
	if e != nil {
		return nil, errors.New("IMAP folder unavailable")
	}
	// 163 may omit UIDNEXT and uses sparse UIDs. Bound response bytes rather
	// than assuming a small numeric UID window represents recent messages.
	high := ^uint32(0)
	if a.Cursor != "" {
		r, e := decodeRef(a.Cursor, a.AccountID)
		if e != nil || r.Folder != a.Folder || r.Validity != box.UidValidity || r.UID == 0 {
			return nil, errors.New("invalid or stale search cursor")
		}
		high = r.UID
	}
	low := uint32(1)
	criteria := imap.NewSearchCriteria()
	criteria.Uid = new(imap.SeqSet)
	if high > 0 {
		criteria.Uid.AddRange(low, high)
	}
	if a.From != "" {
		criteria.Header.Set("From", a.From)
	}
	if a.Subject != "" {
		criteria.Header.Set("Subject", a.Subject)
	}
	if a.Query != "" {
		criteria.Text = []string{a.Query}
	}
	if a.Unread {
		criteria.WithoutFlags = []string{imap.SeenFlag}
	}
	if a.Since != "" {
		criteria.Since, e = time.Parse("2006-01-02", a.Since)
		if e != nil {
			return nil, errors.New("since must be YYYY-MM-DD")
		}
	}
	ids := []uint32{}
	if high > 0 {
		ids, e = c.UidSearch(criteria)
		if e != nil {
			return nil, errors.New("IMAP search failed")
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
	next := uint32(0)
	if low > 1 {
		next = low - 1
	}
	if len(ids) > a.Limit {
		ids = ids[:a.Limit]
		next = ids[len(ids)-1] - 1
	}
	rows := []map[string]any{}
	if len(ids) > 0 {
		set := new(imap.SeqSet)
		set.AddNum(ids...)
		ch := make(chan *imap.Message)
		done := make(chan error, 1)
		go func() {
			done <- c.UidFetch(set, []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope, imap.FetchFlags, imap.FetchRFC822Size}, ch)
		}()
		for m := range ch {
			if m.Envelope == nil {
				continue
			}
			env := m.Envelope
			rows = append(rows, map[string]any{"message_id": encodeRef(Ref{Account: a.AccountID, Folder: a.Folder, Validity: box.UidValidity, UID: m.Uid}), "uid": m.Uid, "subject": env.Subject, "from": imapAddresses(env.From), "date": env.Date, "flags": m.Flags, "size": m.Size})
		}
		if <-done != nil {
			return nil, errors.New("IMAP header fetch failed")
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i]["uid"].(uint32) > rows[j]["uid"].(uint32) })
	}
	cursor := ""
	if next > 0 {
		cursor = encodeRef(Ref{Account: a.AccountID, Folder: a.Folder, Validity: box.UidValidity, UID: next})
	}
	return map[string]any{"account_id": a.AccountID, "folder": a.Folder, "messages": rows, "next_cursor": cursor, "coverage": map[string]any{"uid_start": low, "uid_end": high, "more_results": next > 0, "scope": "specified folder and filters only; live mailbox pagination is not a snapshot"}}, nil
}
func imapAddresses(in []*imap.Address) []string {
	out := []string{}
	for _, a := range in {
		out = append(out, a.Address())
	}
	return out
}
func (p *imapProvider) Raw(ctx context.Context, r Ref) ([]byte, error) {
	if r.Account != p.account.ID || r.Folder == "" || r.UID == 0 || r.Validity == 0 {
		return nil, errors.New("invalid IMAP message reference")
	}
	c, close, e := p.connect(ctx)
	if e != nil {
		return nil, e
	}
	defer close()
	box, e := c.Select(r.Folder, true)
	if e != nil {
		return nil, errors.New("IMAP folder unavailable")
	}
	if box.UidValidity != r.Validity {
		return nil, errors.New("stale IMAP UIDVALIDITY; search again")
	}
	set := new(imap.SeqSet)
	set.AddNum(r.UID)
	section := &imap.BodySectionName{Peek: true, Partial: []int{0, maxMessageBytes + 1}}
	ch := make(chan *imap.Message)
	done := make(chan error, 1)
	go func() {
		done <- c.UidFetch(set, []imap.FetchItem{imap.FetchUid, imap.FetchRFC822Size, section.FetchItem()}, ch)
	}()
	var data []byte
	var readErr error
	for m := range ch {
		body := m.GetBody(section)
		if body == nil {
			continue
		}
		data, e = io.ReadAll(io.LimitReader(body, maxMessageBytes+1))
		if e != nil || len(data) > maxMessageBytes || m.Size > maxMessageBytes {
			readErr = errors.New("message exceeds 8 MiB limit or cannot be read")
		}
	}
	if <-done != nil {
		return nil, errors.New("IMAP message fetch failed")
	}
	if readErr != nil {
		return nil, readErr
	}
	if data == nil {
		return nil, fmt.Errorf("message no longer exists")
	}
	return data, nil
}
