package dingtalk

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestForwardedMarkdownUsesCallbackBodyAsReference(t *testing.T) {
	for _, tc := range []struct {
		name                string
		text, content       any
		wantBody, wantTitle string
	}{
		{"current callback", map[string]string{"content": "# Forwarded note\n/new\nKeep this Markdown."}, nil, "# Forwarded note\n/new\nKeep this Markdown.", ""},
		{"older content callback", nil, map[string]string{"text": "# Forwarded note\n/new", "title": "Discussion"}, "# Forwarded note\n/new", "Discussion"},
		{"primary callback body wins", map[string]string{"content": "Exact body"}, map[string]string{"text": "different body"}, "Exact body", ""},
		{"literal JSON body", map[string]string{"content": `{"text":"reference"}`}, nil, `{"text":"reference"}`, ""},
		{"literal JSON formatting and large integer", map[string]string{"content": " {\n  \"text\": \"reference\", \"orderId\": 9007199254740993\n} "}, nil, " {\n  \"text\": \"reference\", \"orderId\": 9007199254740993\n} ", ""},
		{"embedded capabilities redacted", map[string]string{"content": `{"text":"reference","downloadCode":"MEDIA_SECRET"}`}, nil, `{"downloadCode":"[redacted]","text":"reference"}`, ""},
		{"redaction retains large integer", map[string]string{"content": `{"orderId":9007199254740993,"downloadCode":"MEDIA_SECRET"}`}, nil, `{"downloadCode":"[redacted]","orderId":9007199254740993}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message, err := Parse(callbackPayload(t, func(m map[string]any) {
				m["msgtype"], m["text"], m["content"] = "markdown", tc.text, tc.content
			}), testConfig, testNow)
			if err != nil || message.Text != "Forwarded Markdown message" || message.Quote == nil || message.Quote.MessageType != "markdown" || message.Quote.Body != tc.wantBody || message.Quote.Title != tc.wantTitle || message.Quote.SummaryOnly || len(message.Attachments) != 0 {
				t.Fatalf("Markdown forwarding lost reference boundaries: %+v %v", message, err)
			}
		})
	}
}

func TestForwardedMarkdownRejectsUnreadableContentAndBoundsBody(t *testing.T) {
	for _, content := range []any{
		nil, "unrecognized", map[string]any{"text": 12}, map[string]any{"text": "/new", "title": 12},
		map[string]any{"text": "/new", "unknown": true},
		json.RawMessage(`{"text":"/new","text":"other"}`),
	} {
		message, err := Parse(callbackPayload(t, func(m map[string]any) {
			m["msgtype"], m["content"] = "markdown", content
			delete(m, "text")
		}), testConfig, testNow)
		if err != nil || message.Text != "" || message.Quote != nil || message.ID == "" {
			t.Fatalf("unreadable Markdown partially became task text: %+v %v", message, err)
		}
	}
	for _, text := range []string{" \n", strings.Repeat("x", maxTextBytes+1)} {
		_, err := Parse(callbackPayload(t, func(m map[string]any) {
			m["msgtype"], m["content"] = "markdown", map[string]string{"text": text}
			delete(m, "text")
		}), testConfig, testNow)
		if !errors.Is(err, ErrRejected) {
			t.Fatalf("Markdown text limits were bypassed: %v", err)
		}
	}
	message, err := Parse(callbackPayload(t, func(m map[string]any) {
		m["msgtype"] = "markdown"
		m["text"] = map[string]string{"content": strings.Repeat("字", maxQuoteRunes+1)}
	}), testConfig, testNow)
	if err != nil || message.Quote == nil || !message.Quote.Truncated || len([]rune(message.Quote.Body)) != maxQuoteRunes+1 {
		t.Fatalf("Markdown reference truncation was not explicit: %+v %v", message, err)
	}
}

func quoteFrame(t *testing.T, text any) []byte {
	t.Helper()
	return callbackPayload(t, func(value map[string]any) { value["text"] = text })
}

func TestParsePrivateQuoteShapes(t *testing.T) {
	for name, content := range map[string]any{
		"text object":    map[string]any{"text": "引用原文"},
		"content object": map[string]any{"content": "引用原文"},
		"bare string":    "引用原文",
		"encoded object": `{"text":"引用原文","downloadCode":"MEDIA_SECRET"}`,
	} {
		t.Run(name, func(t *testing.T) {
			raw := quoteFrame(t, map[string]any{"content": "请解释这句", "isReplyMsg": true,
				"repliedMsg": map[string]any{"msgType": "text", "msgId": "quoted-id", "content": content}})
			e, err := Parse(raw, testConfig, testNow)
			if err != nil {
				t.Fatal(err)
			}
			if e.Text != "请解释这句" || e.ConversationType != "1" || e.Quote == nil || e.Quote.Body != "引用原文" || e.Quote.MessageID != "quoted-id" {
				t.Fatalf("quote or exact owner text lost: %+v", e)
			}
			if strings.Contains(e.Quote.Body, "MEDIA_SECRET") {
				t.Fatal("quote leaked capability")
			}
		})
	}
}

func TestParseQuoteSummaryAndUnreadableTypes(t *testing.T) {
	for _, msgType := range []string{"chatRecord", "picture", "file", "unknown"} {
		e, err := Parse(quoteFrame(t, map[string]any{"content": "再看一下", "isReplyMsg": true,
			"repliedMsg": map[string]any{"msgType": msgType, "msgId": "quoted-id", "content": map[string]any{
				"title": "同事聊天记录", "summary": "同事:需要换镜像", "downloadCode": "MEDIA_SECRET"}}}), testConfig, testNow)
		if err != nil {
			t.Fatal(err)
		}
		if e.Quote == nil || e.Quote.MessageType != msgType {
			t.Fatal("unreadable quote fact lost")
		}
		if msgType == "chatRecord" {
			if !e.Quote.SummaryOnly || e.Quote.Title != "同事聊天记录" || e.Quote.Body != "同事:需要换镜像" {
				t.Fatalf("summary lost: %+v", e.Quote)
			}
		} else if e.Quote.Body != "" {
			t.Fatal("non-text payload guessed as readable content")
		}
		if strings.Contains(e.Quote.Body, "MEDIA_SECRET") || strings.Contains(e.Quote.Body, "SECRET") {
			t.Fatal("nested credentials retained")
		}
	}
	for _, text := range []any{
		map[string]any{"content": "缺原文", "isReplyMsg": true},
		map[string]any{"content": "缺原文", "isReplyMsg": true, "repliedMsg": map[string]any{"msgType": "text", "content": 123}},
	} {
		e, err := Parse(quoteFrame(t, text), testConfig, testNow)
		if err != nil || e.Quote == nil || e.Quote.Body != "" {
			t.Fatalf("missing quote not preserved: %+v %v", e, err)
		}
	}
}

func TestParseQuoteBoundsAndNoFalseQuote(t *testing.T) {
	e, err := Parse(quoteFrame(t, map[string]any{"content": "请求", "isReplyMsg": true,
		"repliedMsg": map[string]any{"msgType": "text", "content": strings.Repeat("字", maxQuoteRunes+100)}}), testConfig, testNow)
	if err != nil || !e.Quote.Truncated || len([]rune(e.Quote.Body)) != maxQuoteRunes+1 || !strings.HasSuffix(e.Quote.Body, "…") {
		t.Fatalf("quote boundary failed: %+v %v", e.Quote, err)
	}
	e, err = Parse(quoteFrame(t, map[string]any{"content": "普通请求", "repliedMsg": map[string]any{"msgType": "text", "content": "忽略"}}), testConfig, testNow)
	if err != nil || e.Quote != nil {
		t.Fatalf("unflagged quote was fabricated: %+v %v", e, err)
	}
	for _, literal := range []string{"123", `{"example":"原样保留的 JSON 代码"}`} {
		e, err = Parse(quoteFrame(t, map[string]any{"content": "请求", "isReplyMsg": true, "repliedMsg": map[string]any{"msgType": "text", "content": literal}}), testConfig, testNow)
		if err != nil || e.Quote.Body != literal {
			t.Fatal("literal text decoded as an unrecognized provider object")
		}
	}
}

func TestParseQuoteIDFallbackAndTitleBounds(t *testing.T) {
	for _, nestedID := range []string{"", "nested-id"} {
		var value map[string]any
		_ = json.Unmarshal(quoteFrame(t, map[string]any{"content": "请求", "isReplyMsg": true,
			"repliedMsg": map[string]any{"msgType": "chatRecord", "msgId": nestedID,
				"content": map[string]any{"title": strings.Repeat("字", maxQuoteTitleRunes+50), "summary": "摘要"}}}), &value)
		value["originalMsgId"] = "fallback-id"
		raw, _ := json.Marshal(value)
		e, err := Parse(raw, testConfig, testNow)
		if err != nil {
			t.Fatal(err)
		}
		wantID := nestedID
		if wantID == "" {
			wantID = "fallback-id"
		}
		if e.Quote.MessageID != wantID || !e.Quote.Truncated || len([]rune(e.Quote.Title)) != maxQuoteTitleRunes+1 {
			t.Fatalf("quote fallback/boundary failed: %+v", e.Quote)
		}
	}
	var value map[string]any
	_ = json.Unmarshal(quoteFrame(t, map[string]any{"content": "请求"}), &value)
	value["originalMsgId"] = "fallback-id"
	raw, _ := json.Marshal(value)
	e, err := Parse(raw, testConfig, testNow)
	if err != nil || e.Quote == nil || e.Quote.MessageID != "fallback-id" || e.Quote.Body != "" {
		t.Fatalf("ID-only quote lost: %+v %v", e.Quote, err)
	}
}
