package feishu

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"agent-host/internal/state"
)

func fixtureConfig() Config {
	return Config{Profile: "test-profile", AppID: "cli_test", TenantID: "tenant-test", BotOpenID: "ou_bot", OwnerOpenID: "ou_owner"}
}

func fixtureEvent() map[string]any {
	return map[string]any{"schema": "2.0", "header": map[string]any{
		"event_type": "im.message.receive_v1", "event_id": "event-one", "create_time": "1791776000000", "app_id": "cli_test", "tenant_key": "tenant-test"},
		"event": map[string]any{"sender": map[string]any{"sender_type": "user", "sender_id": map[string]any{"open_id": "ou_owner"}, "tenant_key": "tenant-test"},
			"message": map[string]any{"message_id": "om_source", "chat_id": "oc_chat", "chat_type": "p2p", "message_type": "text", "create_time": "1791775999000", "content": `{"text":"hello"}`}}}
}

func fixtureRaw(t *testing.T, event map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func eventHeader(event map[string]any) map[string]any { return event["header"].(map[string]any) }
func eventBody(event map[string]any) map[string]any   { return event["event"].(map[string]any) }
func eventSender(event map[string]any) map[string]any {
	return eventBody(event)["sender"].(map[string]any)
}
func eventMessage(event map[string]any) map[string]any {
	return eventBody(event)["message"].(map[string]any)
}

func groupFixture() map[string]any {
	event := fixtureEvent()
	eventSender(event)["sender_id"] = map[string]any{"open_id": "ou_colleague"}
	message := eventMessage(event)
	message["chat_type"] = "group"
	message["content"] = `{"text":"@_user_1 hello"}`
	message["mentions"] = []any{map[string]any{"key": "@_user_1", "id": map[string]any{"open_id": "ou_bot"}, "name": "Untrusted name"}}
	return event
}

func TestOwnerPrivateIntakeRetainsOriginalReplyTargetAndSourceTime(t *testing.T) {
	p := Parser{Config: fixtureConfig()}
	event := fixtureEvent()
	message, err := p.Parse(fixtureRaw(t, event))
	if err != nil || message.ID != "om_source" || message.ConversationID != "oc_chat" || message.ConversationType != "1" || message.SenderID != "ou_owner" || message.TenantID != "tenant-test" || message.MessageType != "text" || message.Text != "hello" || !message.CreatedAt.Equal(time.UnixMilli(1791775999000)) {
		t.Fatalf("private intake: %+v %v", message, err)
	}
	if message.ReplyTarget != (state.ChannelReplyTarget{Kind: state.ReplyTargetFeishu, ID: "oc_chat", MessageID: "om_source"}) {
		t.Fatalf("reply target: %+v", message.ReplyTarget)
	}
	eventHeader(event)["event_id"] = "another-delivery"
	redelivery, err := p.Parse(fixtureRaw(t, event))
	if err != nil || !reflect.DeepEqual(message, redelivery) {
		t.Fatalf("delivery id changed original message identity: %+v %v", redelivery, err)
	}
	delete(eventSender(event), "tenant_key")
	delete(eventMessage(event), "create_time")
	message, err = p.Parse(fixtureRaw(t, event))
	if err != nil || message.ID == "" || !message.CreatedAt.Equal(time.UnixMilli(1791776000000)) {
		t.Fatalf("known owner/header time fallback: %+v %v", message, err)
	}
}

func TestGroupUsesStructuredBotMentionAndStripsOnlyOwnLeadingKeys(t *testing.T) {
	event := groupFixture()
	eventMessage(event)["content"] = `{"text":"  @_user_1 /status  "}`
	message, err := (Parser{Config: fixtureConfig()}).Parse(fixtureRaw(t, event))
	if err != nil || message.Text != "/status" || message.ConversationType != "2" || message.SenderID != "ou_colleague" {
		t.Fatalf("group intake: %+v %v", message, err)
	}
	for _, tc := range []struct{ text, want string }{
		{"@_user_1你好", "你好"},
		{"@_user_1 @_user_1 hello", "hello"},
		{"@_user_10 hello", "@_user_10 hello"},
		{"quote @_user_1 hello", "quote @_user_1 hello"},
		{"@_user_2 @_user_1 hello", "@_user_2 @_user_1 hello"},
		{"@_user_1 keep quoted @_user_1", "keep quoted @_user_1"},
	} {
		t.Run(tc.text, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"text": tc.text})
			eventMessage(event)["content"] = string(body)
			m, err := (Parser{Config: fixtureConfig()}).Parse(fixtureRaw(t, event))
			if err != nil || m.Text != tc.want {
				t.Fatalf("mention stripping: %q %v", m.Text, err)
			}
		})
	}
}

func TestIgnoredSourcesCannotBecomeModelRequests(t *testing.T) {
	for _, tc := range []struct {
		name   string
		group  bool
		mutate func(map[string]any)
	}{
		{"non-owner private", false, func(e map[string]any) { eventSender(e)["sender_id"] = map[string]any{"open_id": "ou_other"} }},
		{"private foreign tenant", false, func(e map[string]any) { eventSender(e)["tenant_key"] = "another-tenant" }},
		{"bot sender", false, func(e map[string]any) { eventSender(e)["sender_type"] = "bot" }},
		{"self as user", false, func(e map[string]any) { eventSender(e)["sender_id"] = map[string]any{"open_id": "ou_bot"} }},
		{"anonymous sender", false, func(e map[string]any) { eventSender(e)["sender_type"] = "anonymous" }},
		{"unsupported card", false, func(e map[string]any) {
			eventMessage(e)["message_type"] = "video_chat"
			eventMessage(e)["content"] = "not decoded"
		}},
		{"unsupported post", false, func(e map[string]any) { eventMessage(e)["message_type"] = "post" }},
		{"topic group", true, func(e map[string]any) { eventMessage(e)["chat_type"] = "topic_group" }},
		{"group absent sender tenant", true, func(e map[string]any) { delete(eventSender(e), "tenant_key") }},
		{"group foreign sender tenant", true, func(e map[string]any) { eventSender(e)["tenant_key"] = "another-tenant" }},
		{"group missing mention", true, func(e map[string]any) { delete(eventMessage(e), "mentions") }},
		{"group another bot", true, func(e map[string]any) {
			eventMessage(e)["mentions"] = []any{map[string]any{"key": "@_user_1", "id": map[string]any{"open_id": "ou_otherbot"}, "name": "Bot"}}
		}},
		{"group all mention", true, func(e map[string]any) {
			eventMessage(e)["mentions"] = []any{map[string]any{"key": "@_all", "id": map[string]any{"open_id": "all"}}}
		}},
		{"group plain bot name", true, func(e map[string]any) {
			eventMessage(e)["content"] = `{"text":"@Bot hello"}`
			delete(eventMessage(e), "mentions")
		}},
		{"empty private", false, func(e map[string]any) { eventMessage(e)["content"] = `{"text":"  "}` }},
		{"mention only", true, func(e map[string]any) { eventMessage(e)["content"] = `{"text":"@_user_1"}` }},
		{"different event type", false, func(e map[string]any) { eventHeader(e)["event_type"] = "vc.bot.meeting_activity_v1" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := fixtureEvent()
			if tc.group {
				event = groupFixture()
			}
			tc.mutate(event)
			message, err := (Parser{Config: fixtureConfig()}).Parse(fixtureRaw(t, event))
			if err != nil || message.ID != "" || message.Text != "" || message.ReplyTarget.Kind != "" {
				t.Fatalf("ignored source admitted: %+v %v", message, err)
			}
		})
	}
}

func TestMalformedAndAmbiguousFramesFailBeforeIntake(t *testing.T) {
	base := string(fixtureRaw(t, fixtureEvent()))
	cases := map[string]string{
		"schema missing":         strings.Replace(base, `"schema":"2.0"`, `"schema":"1.0"`, 1),
		"foreign app":            strings.Replace(base, `"app_id":"cli_test"`, `"app_id":"cli_other"`, 1),
		"foreign tenant":         strings.ReplaceAll(base, `"tenant_key":"tenant-test"`, `"tenant_key":"other"`),
		"missing event id":       strings.Replace(base, `"event_id":"event-one"`, `"event_id":""`, 1),
		"event time zero":        strings.Replace(base, `"create_time":"1791776000000"`, `"create_time":0`, 1),
		"event time float":       strings.Replace(base, `"create_time":"1791776000000"`, `"create_time":1791776000000.0`, 1),
		"event time enormous":    strings.Replace(base, `"create_time":"1791776000000"`, `"create_time":"253402300800000"`, 1),
		"bad message id":         strings.Replace(base, `"message_id":"om_source"`, `"message_id":"oc_source"`, 1),
		"bad chat id":            strings.Replace(base, `"chat_id":"oc_chat"`, `"chat_id":"om_chat"`, 1),
		"duplicate header id":    strings.Replace(base, `"app_id":"cli_test"`, `"app_id":"cli_test","APP_ID":"cli_other"`, 1),
		"wrong-case header id":   strings.Replace(base, `"app_id":"cli_test"`, `"APP_ID":"cli_test"`, 1),
		"duplicate sender alias": strings.Replace(base, `"open_id":"ou_owner"`, `"open_id":"ou_owner","OPEN_ID":"ou_other"`, 1),
		"message time bad":       strings.Replace(base, `"create_time":"1791775999000"`, `"create_time":"wrong"`, 1),
		"trailing object":        base + `{}`,
		"top array":              `[` + base + `]`,
		"invalid UTF-8":          base + string([]byte{255}),
		"oversized frame":        `{"padding":"` + strings.Repeat("x", maxFrameBytes) + `"}`,
		"excessive nesting":      `{"unused":` + strings.Repeat(`[`, 33) + `0` + strings.Repeat(`]`, 33) + `}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			m, err := (Parser{Config: fixtureConfig()}).Parse([]byte(raw))
			if err == nil || m.ID != "" || strings.Contains(err.Error(), "cli_other") {
				t.Fatalf("malformed event accepted: %+v %v", m, err)
			}
		})
	}
	for _, content := range []string{`{"text":"hello","TEXT":"other"}`, `{"TEXT":"hello"}`, `{"text":null}`, `{"text":3}`, `{"text":"hello"}{}`, `{"text":"hello\u0000"}`, `{"text":"` + strings.Repeat("x", maxTextBytes+1) + `"}`} {
		event := fixtureEvent()
		eventMessage(event)["content"] = content
		m, err := (Parser{Config: fixtureConfig()}).Parse(fixtureRaw(t, event))
		if err == nil || m.ID != "" {
			t.Fatalf("invalid text accepted: %+v %v", m, err)
		}
	}
}

func TestConfigRequiresDistinctOwnerAndSafeExplicitIdentities(t *testing.T) {
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Profile = "--as" }, func(c *Config) { c.AppID = "other" },
		func(c *Config) { c.OwnerOpenID = "" }, func(c *Config) { c.OwnerOpenID = c.BotOpenID },
		func(c *Config) { c.BotOpenID = "numeric" }, func(c *Config) { c.TenantID = "" },
		func(c *Config) { c.TenantID = "tenant\nother" }, func(c *Config) { c.TenantID = string([]byte{255}) },
	} {
		config := fixtureConfig()
		mutate(&config)
		if config.Validate() == nil {
			t.Fatalf("invalid config accepted: %+v", config)
		}
		if m, err := (Parser{Config: config}).Parse(fixtureRaw(t, fixtureEvent())); err == nil || m.ID != "" {
			t.Fatal("parser bypassed invalid config")
		}
	}
}
