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

func TestDWSRichTextQuoteSeparatesSenderTextAndReference(t *testing.T) {
	for _, kind := range []string{"interactiveCard", "text"} {
		t.Run(kind, func(t *testing.T) {
			raw := callbackPayload(t, func(m map[string]any) {
				delete(m, "text")
				m["msgtype"] = "richText"
				m["content"] = map[string]any{"richText": []any{map[string]string{"text": "Read the latest comment"}}, "isReplyMsg": true,
					"repliedMsg": map[string]any{"msgId": "exact-original-id", "msgType": kind, "senderId": "untrusted-author", "createdAt": 123,
						"content": map[string]any{"text": "Agent-Host 回复 [agh:original]", "downloadCode": "QUOTED_CAPABILITY", "cardContent": []any{map[string]string{"elementType": "TEXT", "value": "FTP request"}}}}}
			})
			got, err := Parse(raw, testConfig, testNow)
			if err != nil || got.Text != "Read the latest comment" || got.ReplyToID != "exact-original-id" || got.Quote == nil || got.Quote.MessageID != "exact-original-id" || got.Quote.MessageType != kind || len(got.Attachments) != 0 {
				t.Fatal("DWS quote wrapper lost text/reference or imported quoted media", got, err)
			}
			encoded, _ := json.Marshal(got)
			if strings.Contains(string(encoded), "QUOTED_CAPABILITY") || strings.Contains(got.Text, "FTP request") {
				t.Fatal("quoted bytes became sender text or capabilities", string(encoded))
			}
		})
	}
}

func TestDWSRichTextQuoteRejectsMalformedWrapperWithoutTextPrefix(t *testing.T) {
	for _, content := range []string{
		`{"richText":[{"text":"/new"}],"unexpected":"value"}`,
		`{"richText":[{"text":"/new"}],"isReplyMsg":"true"}`,
		`{"richText":[{"text":"/new"}],"isReplyMsg":null}`,
		`{"richText":[{"text":"/new"}],"isReplyMsg":true,"repliedMsg":null}`,
		`{"richText":[{"text":"/new"}],"repliedMsg":{"msgId":"unmarked"}}`,
		`{"richText":[{"text":"/new"},{"type":"unknown"}],"isReplyMsg":true}`,
		`{"richText":[{"text":"/new"}],"isReplyMsg":true,"isReplyMsg":false}`,
	} {
		got, err := Parse(callbackPayload(t, func(m map[string]any) {
			delete(m, "text")
			m["msgtype"] = "richText"
			m["content"] = json.RawMessage(content)
		}), testConfig, testNow)
		if err != nil || got.Text != "" || got.Quote != nil || got.ReplyToID != "" || len(got.Attachments) != 0 {
			t.Fatal("malformed wrapper admitted a command prefix", got, err)
		}
	}
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

func TestInteractiveCardQuoteExtractsCallbackPreview(t *testing.T) {
	// A reported live callback nests TEXT values under LIST/RICHTEXT children.
	tree := []any{map[string]any{"elementType": "LIST", "children": []any{
		map[string]any{"elementType": "RICHTEXT", "children": []any{
			map[string]any{"elementType": "TEXT", "value": "Approval request"},
			map[string]any{"elementType": "TEXT", "value": "Machine: buildhub-01"},
			map[string]any{"elementType": "IMAGE", "value": "MEDIA_SECRET", "downloadCode": "DOWNLOAD_SECRET"},
		}},
	}}}
	object := map[string]any{"cardContent": tree, "title": "Ticket preview", "downloadCode": "DOWNLOAD_SECRET"}
	encoded, _ := json.Marshal(object)
	for _, content := range []any{object, string(encoded)} {
		for _, conversation := range []string{"1", "2"} {
			raw := callbackPayload(t, func(m map[string]any) {
				m["conversationType"], m["isInAtList"] = conversation, true
				m["text"] = map[string]any{"content": "Explain this ticket", "isReplyMsg": true,
					"repliedMsg": map[string]any{"msgType": "interactiveCard", "msgId": "card-id", "content": content}}
			})
			message, err := Parse(raw, testConfig, testNow)
			if err != nil || message.Quote == nil || message.Quote.Body != "Approval request\nMachine: buildhub-01" || message.Quote.Title != "Ticket preview" || !message.Quote.SummaryOnly || message.Text != "Explain this ticket" || len(message.Attachments) != 0 {
				t.Fatalf("card preview lost provenance or content: %+v %v", message, err)
			}
		}
	}
}

func TestForwardedInteractiveCardPreservesPreviewBoundaries(t *testing.T) {
	object := map[string]any{"title": "Approval", "cardContent": []any{
		map[string]any{"elementType": "TEXT", "value": "/new\nRequest: network access"},
		map[string]any{"elementType": "IMAGE", "value": "MEDIA_SECRET"},
	}, "downloadCode": "DOWNLOAD_SECRET"}
	encoded, _ := json.Marshal(object)
	for _, content := range []any{object, string(encoded)} {
		for _, conversation := range []string{"1", "2"} {
			m, err := Parse(callbackPayload(t, func(m map[string]any) {
				m["msgtype"], m["content"] = "interactiveCard", content
				m["conversationType"], m["isInAtList"] = conversation, true
				delete(m, "text")
			}), testConfig, testNow)
			if err != nil || m.Text != "Forwarded interactive card" || m.Quote == nil || m.Quote.Body != "/new\nRequest: network access" || m.Quote.Title != "Approval" || !m.Quote.SummaryOnly || m.Quote.MessageID != "" || m.ReplyToID != "" || len(m.Attachments) != 0 {
				t.Fatalf("forwarded card lost preview boundaries: %+v %v", m, err)
			}
		}
	}
	m, err := Parse(callbackPayload(t, func(m map[string]any) {
		m["msgtype"] = "interactiveCard"
		m["cardContent"] = object["cardContent"]
		m["title"] = "Approval"
		delete(m, "text")
	}), testConfig, testNow)
	if err != nil || m.Quote == nil || m.Quote.Title != "Approval" || m.Quote.Body != "/new\nRequest: network access" {
		t.Fatalf("top-level card preview lost: %+v %v", m, err)
	}
}

func TestCardQuoteTextLinkFallbackAndBounds(t *testing.T) {
	for _, tc := range []struct {
		name        string
		quoted      *quotedCallback
		body, title string
		truncated   bool
	}{
		{"text and link", &quotedCallback{MsgType: "interactiveCard", Content: json.RawMessage(`{"text":"Ticket 123","title":"Approval","biz_custom_action_url":"https://example.test/ticket/123","downloadUrl":"SECRET"}`)}, "Ticket 123\nhttps://example.test/ticket/123", "Approval", false},
		{"top-level preview", &quotedCallback{MsgType: "interactiveCard", Text: json.RawMessage(`"Ticket 123"`), Title: json.RawMessage(`"Approval"`), ActionURL: json.RawMessage(`"https://example.test/ticket/123"`)}, "Ticket 123\nhttps://example.test/ticket/123", "Approval", false},
		{"metadata is not body", &quotedCallback{MsgType: "interactiveCard", Content: json.RawMessage(`{"cardContent":[{"elementType":"IMAGE","value":"SECRET"}],"downloadCode":"SECRET","url":"https://example.test/secret"}`)}, "", "", false},
		{"unsafe link omitted", &quotedCallback{MsgType: "interactiveCard", ActionURL: json.RawMessage(`"javascript:alert(1)"`)}, "", "", false},
		{"large unicode body", &quotedCallback{MsgType: "interactiveCard", Text: json.RawMessage(`"` + strings.Repeat("字", maxQuoteRunes+20) + `"`)}, strings.Repeat("字", maxQuoteRunes), "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			quote := parseQuote("card-id", true, tc.quoted)
			if quote.Body != tc.body || quote.Title != tc.title || quote.Truncated != tc.truncated || !quote.SummaryOnly {
				t.Fatalf("unexpected card projection: %+v", quote)
			}
		})
	}
	var tree any = map[string]any{"elementType": "TEXT", "value": "Too deep"}
	for range 20 {
		tree = map[string]any{"elementType": "LIST", "children": []any{tree}}
	}
	raw, _ := json.Marshal(map[string]any{"cardContent": tree})
	quote := parseQuote("card-id", true, &quotedCallback{MsgType: "interactiveCard", Content: raw})
	if !quote.Truncated || quote.Body != "" {
		t.Fatal("deep card traversal was not bounded", quote)
	}
}

func TestQuotedMarkdownAndRichTextAreReferenceText(t *testing.T) {
	for kind, content := range map[string]string{
		"markdown": `{"text":"# Quoted note","title":"Note"}`,
		"richText": `{"richText":[{"type":"text","text":"Quoted note"},{"type":"picture","downloadCode":"SECRET"}]}`,
	} {
		quote := parseQuote("original", true, &quotedCallback{MsgType: kind, Content: json.RawMessage(content)})
		if !strings.Contains(quote.Body, "Quoted note") || strings.Contains(quote.Body, "SECRET") {
			t.Fatal("quoted text was dropped or media capability retained", quote)
		}
	}
}
