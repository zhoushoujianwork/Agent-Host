package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

type closeReader struct {
	io.Reader
	closed bool
}

func (r *closeReader) Close() error { r.closed = true; return nil }

func TestReplyRequiresExplicitBusinessAcceptance(t *testing.T) {
	cases := []struct {
		name     string
		httpCode int
		body     string
		want     string
	}{
		{"accepted", 200, `{"errcode":0,"errmsg":"ok"}`, Accepted},
		{"business rejection", 200, `{"errcode":40035,"errmsg":"secret-not-for-output"}`, Failed},
		{"missing code", 200, `{"errmsg":"ok"}`, Unknown},
		{"null code", 200, `{"errcode":null}`, Unknown},
		{"malformed code", 200, `{"errcode":"0"}`, Unknown},
		{"truncated response", 200, `{"errcode":`, Unknown},
		{"too large", 200, strings.Repeat("x", maxResponseBytes+1), Unknown},
		{"denied", 403, "secret-not-for-output", Failed},
		{"server failure", 500, "secret-not-for-output", Unknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := &closeReader{Reader: strings.NewReader(tc.body)}
			calls := 0
			replier := Replier{Now: func() time.Time { return testNow }, HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.String() != testReplyURL || req.Method != "POST" || req.Header.Get("Content-Type") != "application/json" {
					t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Redacted())
				}
				raw, _ := io.ReadAll(req.Body)
				if string(raw) != `{"markdown":{"text":"hello","title":"Agent-Host 回复"},"msgtype":"markdown"}` {
					t.Fatalf("unexpected message: %s", raw)
				}
				return &http.Response{StatusCode: tc.httpCode, Body: body, Header: make(http.Header), Request: req}, nil
			})}}
			status, err := replier.Reply(context.Background(), testReplyURL, testNow.Add(time.Hour), ReplyMessage{Text: "hello"})
			if status != tc.want || (status == Accepted) != (err == nil) || calls != 1 || !body.closed {
				t.Fatalf("status=%s err=%v calls=%d closed=%v", status, err, calls, body.closed)
			}
			if err != nil && strings.Contains(err.Error(), "secret-not-for-output") {
				t.Fatal("remote error was disclosed")
			}
		})
	}
}

func TestReplyMarkdownMentionsOnlyTheStructuredRecipient(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message ReplyMessage
	}{
		{"private text cannot create a mention", ReplyMessage{Text: "@untrusted-user this is ordinary text"}},
		{"group recipient", ReplyMessage{Text: "@member-1\n\n> A short question\n\nA task result.", AtUserID: "member-1"}},
		{"text cannot override recipient", ReplyMessage{Text: "@different-user result", AtUserID: "verified-user"}},
		{"final text at byte limit", ReplyMessage{Text: "@member-1\n\n" + strings.Repeat("x", MaxReplyBytes-len("@member-1\n\n")), AtUserID: "member-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			replier := Replier{Now: func() time.Time { return testNow }, HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				var payload struct {
					MessageType string                       `json:"msgtype"`
					Markdown    struct{ Title, Text string } `json:"markdown"`
					At          *struct {
						UserIDs []string `json:"atUserIds"`
						IsAtAll *bool    `json:"isAtAll"`
					} `json:"at"`
				}
				if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if payload.MessageType != "markdown" || payload.Markdown.Text != tc.message.Text || payload.Markdown.Title != "Agent-Host 回复" {
					t.Fatalf("formatted content changed: %+v", payload)
				}
				if tc.message.AtUserID == "" {
					if payload.At != nil {
						t.Fatal("private reply unexpectedly acquired mentions")
					}
				} else if payload.At == nil || len(payload.At.UserIDs) != 1 || payload.At.UserIDs[0] != tc.message.AtUserID || payload.At.IsAtAll == nil || *payload.At.IsAtAll {
					t.Fatalf("reply did not target exactly the authenticated sender: %+v", payload.At)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"errcode":0}`)), Header: make(http.Header), Request: req}, nil
			})}}
			status, err := replier.Reply(context.Background(), testReplyURL, testNow.Add(time.Hour), tc.message)
			if status != Accepted || err != nil || calls != 1 {
				t.Fatalf("reply = %s, %v; calls = %d", status, err, calls)
			}
		})
	}
}

func TestReplyRejectsUnsafeMentionTargetsBeforeHTTP(t *testing.T) {
	replier := Replier{Now: func() time.Time { return testNow }, HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid mention made an HTTP request")
		return nil, nil
	})}}
	for _, id := range []string{"@member", "member other", " member", "member\n", "member\t", "member\x00", "[member](url)", "member`", "member*", "member/other", "member\\other", "member\u202e", "member\xff", strings.Repeat("m", maxIDBytes+1)} {
		status, err := replier.Reply(context.Background(), testReplyURL, testNow.Add(time.Hour), ReplyMessage{Text: "A result.", AtUserID: id})
		if status != Failed || err == nil || err.Error() != "DingTalk reply mention target is invalid" {
			t.Fatalf("unsafe target accepted or disclosed: status=%s err=%v", status, err)
		}
	}
	status, err := replier.Reply(context.Background(), testReplyURL, testNow.Add(time.Hour), ReplyMessage{Text: "@member\n" + strings.Repeat("x", MaxReplyBytes), AtUserID: "member"})
	if status != Failed || err == nil {
		t.Fatal("mention bypassed the final text byte limit", status, err)
	}
}

func TestValidateAtUserIDAllowsOptionalBoundedStaffIdentifiers(t *testing.T) {
	for _, id := range []string{"", "290123", "member_1", "member-a.b", strings.Repeat("m", maxIDBytes)} {
		if err := ValidateAtUserID(id); err != nil {
			t.Fatal("safe staff identifier rejected", err)
		}
	}
}

func TestReplyNetworkFailureDoesNotRetryOrDiscloseURL(t *testing.T) {
	calls := 0
	replier := Replier{Now: func() time.Time { return testNow }, HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New(testReplyURL) })}}
	status, err := replier.Reply(context.Background(), testReplyURL, testNow.Add(time.Hour), ReplyMessage{Text: "hello"})
	if status != Unknown || err == nil || calls != 1 || strings.Contains(err.Error(), "private-session-capability") || strings.Contains(err.Error(), "https:") {
		t.Fatalf("unsafe network failure: %s %v calls=%d", status, err, calls)
	}
}

func TestReplyDoesNotFollowRedirects(t *testing.T) {
	calls := 0
	replier := Replier{Now: func() time.Time { return testNow }, HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://attacker.example/collect"}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	})}}
	status, err := replier.Reply(context.Background(), testReplyURL, testNow.Add(time.Hour), ReplyMessage{Text: "hello"})
	if status != Failed || err == nil || calls != 1 {
		t.Fatalf("redirect followed: %s %v calls=%d", status, err, calls)
	}
}

func TestReplyRejectsExpiryAndOversizeBeforeHTTP(t *testing.T) {
	replier := Replier{Now: func() time.Time { return testNow }, HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid reply made an HTTP request")
		return nil, nil
	})}}
	for _, tc := range []struct {
		target string
		expiry time.Time
		text   string
	}{
		{testReplyURL, testNow, "hello"}, {"https://attacker.example", testNow.Add(time.Hour), "hello"}, {testReplyURL, testNow.Add(time.Hour), strings.Repeat("x", MaxReplyBytes+1)}, {testReplyURL, testNow.Add(time.Hour), "\xff"},
	} {
		status, err := replier.Reply(context.Background(), tc.target, tc.expiry, ReplyMessage{Text: tc.text})
		if status != Failed || err == nil {
			t.Fatalf("invalid reply accepted: %s %v", status, err)
		}
	}
}
