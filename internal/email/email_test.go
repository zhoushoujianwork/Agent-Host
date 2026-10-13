package email

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func fixture(t *testing.T) (Service, Config) {
	t.Helper()
	dir := t.TempDir()
	credential := filepath.Join(dir, "secret")
	if e := os.WriteFile(credential, []byte("test-password"), 0600); e != nil {
		t.Fatal(e)
	}
	c := Config{StateDirectory: filepath.Join(dir, "state"), Accounts: []Account{{ID: "one", Address: "one@163.com", Provider: "163", CredentialFile: credential, SendEnabled: true}, {ID: "two", Address: "two@163.com", Provider: "163", CredentialFile: credential}}}
	path := filepath.Join(dir, "accounts.json")
	b, _ := json.Marshal(c)
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	_, fp, e := Load(path)
	if e != nil {
		t.Fatal(e)
	}
	return Service{Path: path, Fingerprint: fp}, c
}

type fakeProvider struct {
	mu    sync.Mutex
	sends int
	err   error
	raw   []byte
}

func (p *fakeProvider) Folders(context.Context) (any, error)      { return []string{"INBOX"}, nil }
func (p *fakeProvider) Search(context.Context, Args) (any, error) { return nil, p.err }
func (p *fakeProvider) Raw(context.Context, Ref) ([]byte, error)  { return p.raw, p.err }
func (p *fakeProvider) Send(context.Context, []byte, []string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sends++
	return "provider-id", p.err
}
func (p *fakeProvider) Check(context.Context) (any, error) { return nil, p.err }
func call(t *testing.T, s Service, name string, args any) (any, error) {
	t.Helper()
	b, _ := json.Marshal(args)
	return s.Call(context.Background(), name, b)
}
func TestAccountDiscoveryAndExplicitRouting(t *testing.T) {
	s, _ := fixture(t)
	var selected []string
	s.NewProvider = func(a Account) Provider { selected = append(selected, a.ID); return &fakeProvider{} }
	out, e := call(t, s, "list_accounts", map[string]any{})
	if e != nil || len(out.(map[string]any)["accounts"].([]map[string]any)) != 2 || len(selected) != 0 {
		t.Fatal(out, e)
	}
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), "secret") || strings.Contains(string(b), "password") {
		t.Fatal("credential leaked")
	}
	if _, e = call(t, s, "list_folders", map[string]any{"account_id": "two"}); e != nil || len(selected) != 1 || selected[0] != "two" {
		t.Fatal(selected, e)
	}
	for _, args := range []map[string]any{{"account_id": "absent"}, {"account_id": "one", "endpoint": "override"}, {}} {
		if _, e = call(t, s, "list_folders", args); e == nil {
			t.Fatal("invalid call accepted", args)
		}
	}
	if _, e = call(t, s, "read_message", map[string]any{"account_id": "two", "message_id": encodeRef(Ref{Account: "one", UID: 1})}); e == nil {
		t.Fatal("cross-account reference accepted")
	}
}
func TestConfigurationRevocationAndPrivateFiles(t *testing.T) {
	s, c := fixture(t)
	c.Accounts[0].Disabled = true
	b, _ := json.Marshal(c)
	os.WriteFile(s.Path, b, 0600)
	if _, e := call(t, s, "list_accounts", map[string]any{}); e == nil {
		t.Fatal("changed config accepted")
	}
	os.Chmod(s.Path, 0644)
	if _, _, e := Load(s.Path); e == nil {
		t.Fatal("public config accepted")
	}
	os.Chmod(s.Path, 0600)
	alias := filepath.Join(filepath.Dir(s.Path), "link")
	os.Symlink(s.Path, alias)
	if _, _, e := Load(alias); e == nil {
		t.Fatal("symlink accepted")
	}
}
func TestMCPDiscoveryAndMalformedArguments(t *testing.T) {
	s, _ := fixture(t)
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test"}}}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n" + `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_accounts","arguments":{}}}` + "\n"
	var out bytes.Buffer
	if e := Serve(context.Background(), s.Path, s.Fingerprint, strings.NewReader(input), &out); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(out.String(), "send_message") || !strings.Contains(out.String(), "two@163.com") || strings.Contains(out.String(), "test-password") {
		t.Fatal("missing tools/accounts or leaked credentials")
	}
	for _, raw := range []string{`{"account_id":"one","account_id":"two"}`, `{"account_id":null}`, `{"account_id":"one","offset":-1}`} {
		if _, e := s.Call(context.Background(), "read_message", []byte(raw)); e == nil {
			t.Fatal("invalid args accepted")
		}
	}
}
func TestSendReceiptPreventsReplayAndContentReuse(t *testing.T) {
	s, c := fixture(t)
	p := &fakeProvider{}
	s.NewProvider = func(Account) Provider { return p }
	args := Args{AccountID: "one", OperationID: "op-1", To: []string{"recipient@example.com"}, Subject: "Fixture", Body: "hello"}
	// Use direct calls to share canonical arguments across retries.
	r, e := sendOnce(context.Background(), c, c.Accounts[0], p, args, false)
	if e != nil || r.(map[string]any)["status"] != "accepted" {
		t.Fatal(r, e)
	}
	r, e = sendOnce(context.Background(), c, c.Accounts[0], p, args, false)
	if e != nil || p.sends != 1 {
		t.Fatal(r, e, p.sends)
	}
	args.Body = "different"
	if _, e = sendOnce(context.Background(), c, c.Accounts[0], p, args, false); e == nil || p.sends != 1 {
		t.Fatal("operation content reuse")
	}
	args.OperationID = "op-unknown"
	p.err = errors.New("network timeout")
	r, e = sendOnce(context.Background(), c, c.Accounts[0], p, args, false)
	if e != nil || r.(map[string]any)["status"] != "unknown" {
		t.Fatal(r, e)
	}
	p.err = nil
	_, e = sendOnce(context.Background(), c, c.Accounts[0], p, args, false)
	if e != nil || p.sends != 2 {
		t.Fatal("unknown operation replayed")
	}
	if _, e = call(t, s, "send_message", map[string]any{"account_id": "two", "operation_id": "blocked", "to": []string{"a@example.com"}, "subject": "hi", "body": "hi"}); e == nil {
		t.Fatal("disabled sending accepted")
	}
}
func TestConcurrentSendHasOneSubmission(t *testing.T) {
	_, c := fixture(t)
	p := &fakeProvider{}
	a := Args{AccountID: "one", OperationID: "concurrent", To: []string{"a@example.com"}, Subject: "test", Body: "test"}
	db, e := openOperations(c)
	if e != nil {
		t.Fatal(e)
	}
	db.Close()
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := sendOnce(context.Background(), c, c.Accounts[0], p, a, false)
			if e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if p.sends != 1 {
		t.Fatal(p.sends)
	}
}
func TestMIMERoundTripReplyAndAttachmentChunks(t *testing.T) {
	_, c := fixture(t)
	a := Args{To: []string{"recipient@example.com"}, Subject: "中文主题", Body: "你好", Attachments: []OutboundAttachment{{Filename: "test.txt", ContentType: "text/plain", Data: base64.StdEncoding.EncodeToString([]byte("attachment"))}}}
	raw, _, _, e := composeMessage(c.Accounts[0], a, nil)
	if e != nil {
		t.Fatal(e)
	}
	p, e := parseMIME(raw)
	if e != nil || !strings.Contains(p.Body, "你好") || len(p.Attachments) != 1 {
		t.Fatal(p, e)
	}
	r, e := readMIME(raw, Args{AccountID: "one", AttachmentID: p.Attachments[0].ID, Offset: 2}, true)
	if e != nil {
		t.Fatal(e)
	}
	data, _ := base64.StdEncoding.DecodeString(r.(map[string]any)["data_base64"].(string))
	if string(data) != "tachment" {
		t.Fatal(string(data))
	}
	reply, _, _, e := composeMessage(c.Accounts[0], Args{To: a.To, Body: "reply"}, raw)
	if e != nil {
		t.Fatal(e)
	}
	rp, e := parseMIME(reply)
	if e != nil || rp.Headers.Get("In-Reply-To") == "" {
		t.Fatal("reply lost original identity", e)
	}
	subject, _ := rp.Headers.Subject()
	if subject != "Re: 中文主题" {
		t.Fatal(subject)
	}
	a.To = []string{"x@example.com\r\nBcc: other@example.com"}
	if _, _, _, e = composeMessage(c.Accounts[0], a, nil); e == nil {
		t.Fatal("header injection accepted")
	}
}
func TestBodyContinuationDoesNotSplitUTF8(t *testing.T) {
	_, c := fixture(t)
	raw, _, _, e := composeMessage(c.Accounts[0], Args{To: []string{"a@example.com"}, Subject: "test", Body: strings.Repeat("你好", 3000)}, nil)
	if e != nil {
		t.Fatal(e)
	}
	first, e := readMIME(raw, Args{}, false)
	if e != nil {
		t.Fatal(e)
	}
	r := first.(map[string]any)
	if r["complete"] != false {
		t.Fatal("missing truncation")
	}
	second, e := readMIME(raw, Args{Offset: r["next_offset"].(int)}, false)
	if e != nil || second.(map[string]any)["complete"] != true {
		t.Fatal(second, e)
	}
}

func TestEscapedBodyCanUseSmallerChunks(t *testing.T) {
	_, c := fixture(t)
	raw, _, _, e := composeMessage(c.Accounts[0], Args{To: []string{"a@example.com"}, Subject: "test", Body: strings.Repeat("<", 13000)}, nil)
	if e != nil {
		t.Fatal(e)
	}
	result, e := readMIME(raw, Args{Length: 1000}, false)
	if e != nil {
		t.Fatal(e)
	}
	r := result.(map[string]any)
	if len(r["body"].(string)) != 1000 || r["next_offset"] != 1000 {
		t.Fatal("length not honored")
	}
}
func TestPendingOperationNeverReplays(t *testing.T) {
	_, c := fixture(t)
	p := &fakeProvider{}
	a := Args{AccountID: "one", OperationID: "pending", To: []string{"a@example.com"}, Subject: "test", Body: "test"}
	if _, e := sendOnce(t.Context(), c, c.Accounts[0], p, a, false); e != nil {
		t.Fatal(e)
	}
	db, e := openOperations(c)
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`UPDATE operations SET status='pending' WHERE account='one' AND id='pending'`)
	db.Close()
	if e != nil {
		t.Fatal(e)
	}
	result, e := sendOnce(t.Context(), c, c.Accounts[0], p, a, false)
	if e != nil || result.(map[string]any)["status"] != "pending" || p.sends != 1 {
		t.Fatal("pending operation replayed", e)
	}
}
