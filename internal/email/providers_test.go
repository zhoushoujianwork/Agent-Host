package email

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"strings"
	"testing"
	"time"
)

func TestIMAP163MissingUIDNextAndReadOnlyFetch(t *testing.T) {
	_, cfg := fixture(t)
	raw := "From: sender@example.com\r\nTo: one@163.com\r\nSubject: test\r\nMessage-ID: <source@example.com>\r\n\r\nhello"
	var commands []string
	dial := func(ctx context.Context, address string) (net.Conn, error) {
		left, right := net.Pipe()
		go func() {
			defer right.Close()
			r := bufio.NewReader(right)
			fmt.Fprint(right, "* OK ready\r\n")
			for {
				line, e := r.ReadString('\n')
				if e != nil {
					return
				}
				commands = append(commands, line)
				fields := strings.Fields(line)
				tag := fields[0]
				cmd := strings.ToUpper(strings.Join(fields[1:], " "))
				switch {
				case strings.HasPrefix(cmd, "CAPABILITY"):
					fmt.Fprint(right, "* CAPABILITY IMAP4rev1\r\n")
				case strings.HasPrefix(cmd, "LOGIN"):
				case strings.HasPrefix(cmd, "EXAMINE"):
					fmt.Fprint(right, "* 1 EXISTS\r\n* OK [UIDVALIDITY 99] stable\r\n")
				case strings.HasPrefix(cmd, "STATUS"):
					fmt.Fprint(right, "* STATUS INBOX (UIDNEXT 8)\r\n")
				case strings.HasPrefix(cmd, "UID SEARCH"):
					fmt.Fprint(right, "* SEARCH 7\r\n")
				case strings.HasPrefix(cmd, "UID FETCH"):
					if strings.Contains(cmd, "BODY.PEEK") {
						fmt.Fprintf(right, "* 1 FETCH (UID 7 RFC822.SIZE %d BODY[]<0> {%d}\r\n%s)\r\n", len(raw), len(raw), raw)
					} else {
						fmt.Fprintf(right, "* 1 FETCH (UID 7 RFC822.SIZE %d FLAGS () ENVELOPE (NIL \"test\" ((NIL NIL \"sender\" \"example.com\")) NIL NIL NIL NIL NIL NIL \"<source@example.com>\"))\r\n", len(raw))
					}
				default:
					fmt.Fprintf(right, "%s BAD unexpected\r\n", tag)
					continue
				}
				fmt.Fprintf(right, "%s OK completed\r\n", tag)
			}
		}()
		return left, nil
	}
	p := &imapProvider{account: cfg.Accounts[0], dial: dial}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	r, e := p.Search(ctx, Args{AccountID: "one", Folder: "INBOX", Limit: 2})
	if e != nil {
		t.Fatal(e)
	}
	rows := r.(map[string]any)["messages"].([]map[string]any)
	if len(rows) != 1 {
		t.Fatal("missing search result")
	}
	ref, e := decodeRef(rows[0]["message_id"].(string), "one")
	if e != nil {
		t.Fatal(e)
	}
	data, e := p.Raw(ctx, ref)
	if e != nil || string(data) != raw {
		t.Fatal("raw fetch failed", e)
	}
	ref.Validity = 100
	if _, e = p.Raw(ctx, ref); e == nil {
		t.Fatal("stale UID accepted")
	}
	// Each command round trip completes before returning; no logger sees passwords.
	all := strings.Join(commands, "")
	if strings.Contains(all, " SELECT ") || !strings.Contains(all, " EXAMINE ") || !strings.Contains(all, "BODY.PEEK[]") {
		t.Fatal("read path did not preserve flags or handle missing UIDNEXT")
	}
}
func TestSMTPSubmissionOverTLS(t *testing.T) {
	_, cfg := fixture(t)
	certServer := httptest.NewTLSServer(nil)
	tlsConfig := certServer.TLS.Clone()
	trust := certServer.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	certServer.Close()
	listener, e := tls.Listen("tcp", "127.0.0.1:0", tlsConfig)
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	received := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		conn, e := listener.Accept()
		if e != nil {
			done <- e
			return
		}
		defer conn.Close()
		r := textproto.NewReader(bufio.NewReader(conn))
		fmt.Fprint(conn, "220 fixture ESMTP\r\n")
		for {
			line, e := r.ReadLine()
			if e != nil {
				done <- nil
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO"):
				fmt.Fprint(conn, "250-fixture\r\n250 AUTH PLAIN\r\n")
			case strings.HasPrefix(line, "AUTH PLAIN"):
				fmt.Fprint(conn, "235 authenticated\r\n")
			case strings.HasPrefix(line, "MAIL FROM:"), strings.HasPrefix(line, "RCPT TO:"):
				fmt.Fprint(conn, "250 accepted\r\n")
			case line == "DATA":
				fmt.Fprint(conn, "354 data\r\n")
				b, e := io.ReadAll(r.DotReader())
				if e != nil {
					done <- e
					return
				}
				received <- string(b)
				fmt.Fprint(conn, "250 queued\r\n")
			default:
				done <- fmt.Errorf("unexpected command")
				return
			}
		}
	}()
	p := &imapProvider{account: cfg.Accounts[0], dial: func(ctx context.Context, _ string) (net.Conn, error) {
		d := tls.Dialer{Config: trust}
		return d.DialContext(ctx, "tcp", listener.Addr().String())
	}}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	receipt, e := p.Send(ctx, []byte("From: one@163.com\r\nTo: a@example.com\r\n\r\nbody\r\n"), []string{"a@example.com"})
	if e != nil || receipt != "smtp_accepted" {
		t.Fatal(receipt, e)
	}
	if !strings.Contains(<-received, "body") {
		t.Fatal("body missing")
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
}
func TestGmailIdentityAndRawSend(t *testing.T) {
	_, cfg := fixture(t)
	a := cfg.Accounts[0]
	a.Provider = "gmail"
	a.Address = "owner@gmail.com"
	os.WriteFile(a.CredentialFile, []byte(`{"client_id":"client","client_secret":"secret","refresh_token":"refresh"}`), 0600)
	calls := []string{}
	identity := "owner@gmail.com"
	raw := []byte("From: owner@gmail.com\r\n\r\nhello")
	request := func(ctx context.Context, method, address, token string, body []byte, form bool, out any) error {
		calls = append(calls, address)
		response := "{}"
		switch {
		case strings.HasSuffix(address, "/token"):
			if !form || !strings.Contains(string(body), "refresh_token=refresh") {
				t.Error("missing refresh")
			}
			response = `{"access_token":"access"}`
		case strings.HasSuffix(address, "/profile"):
			response = `{"emailAddress":"` + identity + `"}`
		case strings.Contains(address, "?format=raw"):
			response = `{"raw":"` + base64.RawURLEncoding.EncodeToString(raw) + `","threadId":"thread-fixture"}`
		case strings.HasSuffix(address, "messages/send"):
			var msg map[string]string
			json.Unmarshal(body, &msg)
			if msg["threadId"] != "thread-fixture" {
				t.Error("reply thread identity missing")
			}
			b, _ := base64.RawURLEncoding.DecodeString(msg["raw"])
			if string(b) != string(raw) {
				t.Error("outgoing MIME changed")
			}
			response = `{"id":"sent-id"}`
		}
		if !strings.HasSuffix(address, "/token") && token != "access" {
			t.Error("missing access token")
		}
		return json.Unmarshal([]byte(response), out)
	}
	p := &gmailProvider{account: a, request: request}
	data, e := p.Raw(t.Context(), Ref{Account: a.ID, GmailID: "abc"})
	if e != nil || string(data) != string(raw) {
		t.Fatal(e)
	}
	id, e := p.Send(t.Context(), raw, nil)
	if e != nil || id != "sent-id" {
		t.Fatal(id, e)
	}
	if len(calls) != 4 {
		t.Fatal("unexpected OAuth refresh count", len(calls))
	}
	identity = "other@gmail.com"
	p = &gmailProvider{account: a, request: request}
	before := len(calls)
	if _, e = p.Send(t.Context(), raw, nil); e == nil || len(calls) != before+2 {
		t.Fatal("wrong Google account permitted")
	}
}
