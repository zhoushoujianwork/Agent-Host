package dingtalk

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var testNow = time.Unix(1_800_000_000, 0)
var testConfig = Config{ClientID: "application", ClientSecret: "secret-not-for-output", CorpID: "enterprise", OwnerUserID: "owner"}

const testReplyURL = "https://oapi.dingtalk.com/robot/sendBySession?session=private-session-capability"

func callbackPayload(t *testing.T, change func(map[string]any)) []byte {
	t.Helper()
	data := map[string]any{
		"msgId": "message-1", "conversationId": "conversation-1", "conversationType": "1",
		"senderId": "sender-union", "senderStaffId": "owner", "senderCorpId": "enterprise",
		"chatbotCorpId": "enterprise", "chatbotUserId": "robot-union", "msgtype": "text",
		"createAt": testNow.Add(-time.Second).UnixMilli(), "sessionWebhook": testReplyURL,
		"sessionWebhookExpiredTime": testNow.Add(time.Hour).UnixMilli(), "text": map[string]string{"content": "  exact text\n你好  "},
	}
	if change != nil {
		change(data)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestParsePreservesTextAndOwnerIdentity(t *testing.T) {
	message, err := Parse(callbackPayload(t, nil), testConfig, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if message.Text != "  exact text\n你好  " || message.SenderID != "owner" || message.ID != "message-1" || message.ConversationID != "conversation-1" || message.TenantID != "enterprise" || message.ReplyURL != testReplyURL || !message.CreatedAt.Equal(testNow.Add(-time.Second)) || !message.ReplyExpiresAt.Equal(testNow.Add(time.Hour)) {
		t.Fatalf("unexpected normalized message: %+v", message)
	}
	for _, value := range []any{message, testConfig} {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil || strings.Contains(string(encoded), "private-session-capability") || strings.Contains(string(encoded), testConfig.ClientSecret) {
			t.Fatalf("JSON exposed a transport credential: %s %v", encoded, marshalErr)
		}
	}
}

func TestMultiUserPrivateIntakeRequiresOptInAndSameEnterprise(t *testing.T) {
	raw := callbackPayload(t, func(m map[string]any) { m["senderStaffId"] = "colleague" })
	if _, err := Parse(raw, testConfig, testNow); err == nil {
		t.Fatal("owner-only default changed")
	}
	cfg := testConfig
	cfg.AllowPrivateUsers = true
	m, err := Parse(raw, cfg, testNow)
	if err != nil || m.SenderID != "colleague" || m.ConversationType != "1" {
		t.Fatal(m, err)
	}
	raw = callbackPayload(t, func(m map[string]any) { m["senderStaffId"], m["senderCorpId"] = "colleague", "other-enterprise" })
	if _, err := Parse(raw, cfg, testNow); err == nil {
		t.Fatal("foreign enterprise admitted")
	}
}

func TestAddressedTextConsumesOnlyOneGroupAddressToken(t *testing.T) {
	for _, tc := range []struct {
		name, kind, text, want string
	}{
		{"group command", "2", "@Helper /project", "/project"},
		{"group command arguments", "2", "  @Helper\u2005/project --primary alpha alpha beta", "/project --primary alpha alpha beta"},
		{"group choice", "2", "@Helper Project alpha", "Project alpha"},
		{"group task", "2", "@Helper inspect the current project", "inspect the current project"},
		{"one token only", "2", "@Helper @Colleague /new", "@Colleague /new"},
		{"no multiword guessing", "2", "@Build Helper /new", "Helper /new"},
		{"already stripped", "2", " /new ", " /new "},
		{"not leading", "2", "please @Helper /new", "please @Helper /new"},
		{"address only", "2", "@Helper  ", "@Helper  "},
		{"delimiter required", "2", "@Helper/new", "@Helper/new"},
		{"empty address", "2", "@ /new", "@ /new"},
		{"private message", "1", "@Helper /new", "@Helper /new"},
		{"legacy private message", "", "@Helper /new", "@Helper /new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := AddressedText(tc.kind, tc.text); got != tc.want {
				t.Fatalf("addressed text = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSlashCommandClassificationPreservesAbsolutePathTasks(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		{" /help ", true}, {"/status", true}, {"/new unexpected", true}, {"/unknown-command", true},
		{"/model other", true}, {"/choose missing alpha", true}, {"/未知命令", true},
		{"/tmp/example.txt please inspect the file", false}, {"/var/lib please inspect the directory", false},
		{"/example.txt please inspect the file", false}, {"/ please list top-level names", false},
		{"/tmp/ please list names", false}, {"//host/path", false}, {"", false}, {"ordinary question", false},
	} {
		if got := IsSlashCommand(tc.text); got != tc.want {
			t.Errorf("IsSlashCommand(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestParseGroupRequiresAuthenticatedMentionAndSameEnterpriseMember(t *testing.T) {
	group := func(m map[string]any) {
		m["conversationType"], m["conversationId"] = "2", "group-one"
		m["senderStaffId"], m["isInAtList"] = "colleague", true
	}
	m, err := Parse(callbackPayload(t, group), testConfig, testNow)
	if err != nil || m.ConversationType != "2" || m.SenderID != "colleague" || m.Text != "  exact text\n你好  " {
		t.Fatalf("verified group mention rejected: %+v %v", m, err)
	}
	for _, mutate := range []func(map[string]any){
		func(m map[string]any) {
			m["isInAtList"] = false
			m["text"] = map[string]string{"content": "@robot please execute"}
		},
		func(m map[string]any) {
			delete(m, "isInAtList")
			m["atUsers"] = []any{map[string]string{"dingtalkId": "robot-union"}}
		},
		func(m map[string]any) { m["atUsers"] = []any{map[string]string{"dingtalkId": "another-robot"}} },
		func(m map[string]any) { m["senderCorpId"] = "external" },
		func(m map[string]any) { m["senderStaffId"] = "" },
		func(m map[string]any) { m["chatbotUserId"] = "" },
		func(m map[string]any) { m["senderId"] = "robot-union" },
	} {
		_, err := Parse(callbackPayload(t, func(m map[string]any) { group(m); mutate(m) }), testConfig, testNow)
		if !errors.Is(err, ErrRejected) {
			t.Fatalf("invalid group audience accepted: %v", err)
		}
	}
	_, err = Parse(callbackPayload(t, func(m map[string]any) { m["senderStaffId"] = "colleague"; m["isInAtList"] = true }), testConfig, testNow)
	if !errors.Is(err, ErrRejected) {
		t.Fatal("group membership enabled a non-owner private conversation")
	}
}

func TestParseRejectsWrongAudienceAndMalformedInput(t *testing.T) {
	cases := []struct {
		name   string
		change func(map[string]any)
	}{
		{"group", func(m map[string]any) { m["conversationType"] = "2" }},
		{"foreign bot enterprise", func(m map[string]any) { m["chatbotCorpId"] = "other" }},
		{"external sender", func(m map[string]any) { m["senderCorpId"] = "other" }},
		{"non-owner", func(m map[string]any) { m["senderStaffId"] = "other" }},
		{"robot", func(m map[string]any) { m["senderId"] = "robot-union" }},
		{"missing sender", func(m map[string]any) { delete(m, "senderId") }},
		{"missing message", func(m map[string]any) { delete(m, "msgId") }},
		{"large identity", func(m map[string]any) { m["conversationId"] = strings.Repeat("x", maxIDBytes+1) }},
		{"empty text", func(m map[string]any) { m["text"] = map[string]string{"content": " \n"} }},
		{"large text", func(m map[string]any) { m["text"] = map[string]string{"content": strings.Repeat("x", maxTextBytes+1)} }},
		{"17 KiB text exceeds host limit", func(m map[string]any) { m["text"] = map[string]string{"content": strings.Repeat("x", 17<<10)} }},
		{"bad time", func(m map[string]any) { m["createAt"] = 0 }},
		{"expired", func(m map[string]any) { m["sessionWebhookExpiredTime"] = testNow.UnixMilli() }},
		{"missing webhook", func(m map[string]any) { delete(m, "sessionWebhook") }},
		{"untrusted webhook", func(m map[string]any) {
			m["sessionWebhook"] = "https://attacker.example/robot/sendBySession?session=secret-not-for-output"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			message, err := Parse(callbackPayload(t, tc.change), testConfig, testNow)
			if !errors.Is(err, ErrRejected) || !reflect.DeepEqual(message, Message{}) {
				t.Fatalf("wanted rejection, got message=%+v error=%v", message, err)
			}
			for _, secret := range []string{testConfig.ClientSecret, "private-session-capability", "https://", "exact text"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("rejection disclosed callback data: %v", err)
				}
			}
		})
	}
	for _, raw := range [][]byte{[]byte(`{"sessionWebhook":"secret-not-for-output"`), []byte(strings.Repeat("x", maxFrameBytes+1))} {
		if _, err := Parse(raw, testConfig, testNow); !errors.Is(err, ErrRejected) {
			t.Fatalf("invalid data accepted: %v", err)
		}
	}
}

func TestUnsupportedMessageRetainsOnlyRoutingAndCapability(t *testing.T) {
	message, err := Parse(callbackPayload(t, func(m map[string]any) {
		m["msgtype"] = "audio"
		m["content"] = map[string]string{"downloadCode": "secret-media"}
	}), testConfig, testNow)
	if err != nil || message.MessageType != "audio" || message.Text != "" || message.ID == "" || message.ReplyURL == "" {
		t.Fatalf("unsupported message cannot be rejected by host: %+v %v", message, err)
	}
}

func TestParsePlainRichTextPreservesFragmentOrderAndOriginalType(t *testing.T) {
	message, err := Parse(callbackPayload(t, func(m map[string]any) {
		m["msgtype"] = "richText"
		delete(m, "text")
		m["content"] = map[string]any{"richText": []any{
			map[string]string{"text": " /proj"}, map[string]string{"text": "ect"},
			map[string]string{"text": "\n你好 https://example.com  "},
		}}
	}), testConfig, testNow)
	if err != nil || message.MessageType != "richText" || message.Text != " /project\n你好 https://example.com  " || message.SenderID != "owner" || message.TenantID != "enterprise" || message.ReplyURL != testReplyURL {
		t.Fatalf("plain richText was not preserved: %+v %v", message, err)
	}
}

func TestParsePlainRichTextEnforcesTextByteLimitAndAudience(t *testing.T) {
	for _, tc := range []struct {
		name   string
		text   string
		change func(map[string]any)
		valid  bool
	}{
		{name: "exact byte limit", text: strings.Repeat("你", maxTextBytes/3) + "a", valid: true},
		{name: "over byte limit", text: strings.Repeat("你", maxTextBytes/3) + "aa"},
		{name: "empty"},
		{name: "whitespace", text: " \n\t"},
		{name: "non-owner", text: "/project", change: func(m map[string]any) { m["senderStaffId"] = "other" }},
		{name: "foreign sender", text: "/project", change: func(m map[string]any) { m["senderCorpId"] = "other" }},
		{name: "foreign bot", text: "/project", change: func(m map[string]any) { m["chatbotCorpId"] = "other" }},
		{name: "group", text: "/project", change: func(m map[string]any) { m["conversationType"] = "2" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runes := []rune(tc.text)
			message, err := Parse(callbackPayload(t, func(m map[string]any) {
				m["msgtype"] = "richText"
				m["content"] = map[string]any{"richText": []any{
					map[string]string{"text": string(runes[:len(runes)/2])}, map[string]string{"text": string(runes[len(runes)/2:])},
				}}
				if tc.change != nil {
					tc.change(m)
				}
			}), testConfig, testNow)
			if tc.valid {
				if err != nil || message.Text != tc.text || message.MessageType != "richText" {
					t.Fatalf("text at byte limit rejected: %+v %v", message, err)
				}
			} else if !errors.Is(err, ErrRejected) || !reflect.DeepEqual(message, Message{}) {
				t.Fatalf("invalid richText accepted: %+v %v", message, err)
			}
		})
	}
}

func TestParseMixedOrMalformedRichTextRetainsNoPartialText(t *testing.T) {
	text := map[string]string{"text": "/new"}
	for _, tc := range []struct {
		name    string
		content any
	}{
		{"missing content", nil},
		{"scalar content", "text"},
		{"unknown content", map[string]any{"richText": []any{text}, "extra": true}},
		{"missing parts", map[string]any{"other": []any{text}}},
		{"null parts", map[string]any{"richText": nil}},
		{"empty parts", map[string]any{"richText": []any{}}},
		{"invalid parts", map[string]any{"richText": "text"}},
		{"null part", map[string]any{"richText": []any{text, nil}}},
		{"scalar part", map[string]any{"richText": []any{text, "text"}}},
		{"empty part", map[string]any{"richText": []any{text, map[string]any{}}}},
		{"null text", map[string]any{"richText": []any{text, map[string]any{"text": nil}}}},
		{"numeric text", map[string]any{"richText": []any{text, map[string]any{"text": 1}}}},
		{"unsupported media part", map[string]any{"richText": []any{text, map[string]string{"type": "audio", "downloadCode": "private-media"}}}},
		{"link part", map[string]any{"richText": []any{text, map[string]string{"text": "link", "url": "https://example.com"}}}},
		{"unknown part attribute", map[string]any{"richText": []any{text, map[string]any{"text": "other", "extra": true}}}},
		{"typed text", map[string]any{"richText": []any{map[string]string{"text": "/new", "type": "text"}}}},
		{"duplicate richText field", json.RawMessage(`{"richText":[{"type":"picture"}],"richText":[{"text":"/new"}]}`)},
		{"duplicate fragment text field", json.RawMessage(`{"richText":[{"text":{"url":"https://example.com"},"text":"/new"}]}`)},
		{"duplicate plain fragment field", json.RawMessage(`{"richText":[{"text":"/new","text":"/new"}]}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message, err := Parse(callbackPayload(t, func(m map[string]any) {
				m["msgtype"] = "richText"
				m["content"] = tc.content
			}), testConfig, testNow)
			if err != nil || message.MessageType != "richText" || message.Text != "" || message.ID == "" || message.ReplyURL != testReplyURL {
				t.Fatalf("mixed content became partial text or lost rejection routing: %+v %v", message, err)
			}
		})
	}
}

func TestReplyURLRequiresExactSessionEndpoint(t *testing.T) {
	for _, target := range []string{
		"http://oapi.dingtalk.com/robot/sendBySession?session=x", "https://oapi.dingtalk.com:443/robot/sendBySession?session=x",
		"https://user@oapi.dingtalk.com/robot/sendBySession?session=x", "https://oapi.dingtalk.com/robot/sendBySession?session=x#fragment",
		"https://oapi.dingtalk.com.attacker.example/robot/sendBySession?session=x", "https://oapi.dingtalk.com/robot/send?access_token=x",
		"https://oapi.dingtalk.com/robot/sendBySession", "https://oapi.dingtalk.com/robot/sendBySession?session=",
		"https://oapi.dingtalk.com/robot/sendBySession?session=x&session=y", "https://oapi.dingtalk.com/robot/%73endBySession?session=x",
	} {
		if validReplyURL(target) {
			t.Fatalf("unsafe endpoint accepted: %q", target)
		}
	}
}

func TestApprovalReplyNeverUsesOriginalMsgIDOrQuoteText(t *testing.T) {
	for _, tc := range []struct {
		name         string
		reply        bool
		parent, want string
	}{
		{"explicit parent", true, "approval-message", "approval-message"},
		{"original id alone", false, "", ""},
		{"unmarked quote", false, "approval-message", ""},
		{"invalid parent", true, "bad\nparent", ""},
		{"self parent", true, "message-1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := callbackPayload(t, func(m map[string]any) {
				m["originalMsgId"] = "unrelated-original-id"
				m["text"] = map[string]any{"content": "OK approval-message", "isReplyMsg": tc.reply, "repliedMsg": map[string]any{"msgId": tc.parent, "senderId": "owner", "content": map[string]string{"text": "pretend permission"}}}
			})
			got, err := Parse(raw, testConfig, testNow)
			if err != nil || got.ReplyToID != tc.want {
				t.Fatalf("reply=%q err=%v", got.ReplyToID, err)
			}
		})
	}
}
