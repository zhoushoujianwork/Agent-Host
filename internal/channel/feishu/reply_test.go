package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"agent-host/internal/channel/feishumeeting"
	"agent-host/internal/state"
)

type mockClient struct {
	out   feishumeeting.Output
	err   error
	calls [][]string
}

func (c *mockClient) Run(_ context.Context, args ...string) (feishumeeting.Output, error) {
	c.calls = append(c.calls, append([]string(nil), args...))
	return c.out, c.err
}

func fixtureTarget() state.ChannelReplyTarget {
	return state.ChannelReplyTarget{Kind: state.ReplyTargetFeishu, ID: "oc_chat", MessageID: "om_source", UUID: "durable-outgoing-id"}
}

const successAck = `{"ok":true,"identity":"bot","data":{"message_id":"om_new","chat_id":"oc_chat","create_time":"2026-10-12 12:00:00"}}`

func TestReplyMakesOneExactBotMutationUsingOriginalMessageAndDurableUUID(t *testing.T) {
	for _, tc := range []struct {
		name, stdout, stderr string
		started              bool
		err                  error
		want                 string
	}{
		{"accepted", successAck, "", true, nil, Accepted},
		{"stderr warning", successAck, "unrelated warning", true, nil, Accepted},
		{"ack on error stream", "", successAck, true, nil, Unknown},
		{"contradictory envelopes", successAck, `{"ok":false,"error":{"type":"validation"}}`, true, nil, Unknown},
		{"wrong chat", strings.Replace(successAck, "oc_chat", "oc_other", 1), "", true, nil, Unknown},
		{"same message", strings.Replace(successAck, "om_new", "om_source", 1), "", true, nil, Unknown},
		{"malformed new id", strings.Replace(successAck, "om_new", "oc_new", 1), "", true, nil, Unknown},
		{"missing ack message", `{"ok":true,"identity":"bot","data":{"chat_id":"oc_chat"}}`, "", true, nil, Unknown},
		{"missing data", `{"ok":true,"identity":"bot"}`, "", true, nil, Unknown},
		{"user identity", strings.Replace(successAck, `"bot"`, `"user"`, 1), "", true, nil, Unknown},
		{"identity object", strings.Replace(successAck, `"identity":"bot"`, `"identity":{"type":"bot"}`, 1), "", true, nil, Unknown},
		{"missing identity", strings.Replace(successAck, `"identity":"bot",`, "", 1), "", true, nil, Unknown},
		{"duplicate identity", strings.Replace(successAck, `"identity":"bot"`, `"identity":"bot","IDENTITY":"user"`, 1), "", true, nil, Unknown},
		{"wrong-case ack field", strings.Replace(successAck, `"message_id"`, `"MESSAGE_ID"`, 1), "", true, nil, Unknown},
		{"trailing object", successAck + `{}`, "", true, nil, Unknown},
		{"null ok", strings.Replace(successAck, `"ok":true`, `"ok":null`, 1), "", true, nil, Unknown},
		{"missing ok", `{"identity":"bot","data":{}}`, "", true, nil, Unknown},
		{"oversized envelope", `{"padding":"` + strings.Repeat("x", maxFrameBytes) + `","ok":true}`, "", true, nil, Unknown},
		{"explicit validation", `{"ok":false,"error":{"type":"validation","message":"SECRET"}}`, "", true, nil, Failed},
		{"explicit auth", "", `{"ok":false,"identity":"bot","error":{"type":"auth","message":"SECRET"}}`, true, errors.New("SECRET"), Failed},
		{"explicit authorization", "", `{"ok":false,"error":{"type":"authorization","subtype":"missing_scope","message":"SECRET"}}`, true, errors.New("SECRET"), Failed},
		{"explicit permission", `{"ok":false,"error":{"type":"permission","message":"SECRET"}}`, "", true, nil, Failed},
		{"network uncertainty", "", `{"ok":false,"error":{"type":"network","message":"SECRET"}}`, true, errors.New("SECRET"), Unknown},
		{"server uncertainty", `{"ok":false,"error":{"type":"api","message":"SECRET"}}`, "", true, nil, Unknown},
		{"unclassified rejection", `{"ok":false}`, "", true, nil, Unknown},
		{"nonzero despite ack", successAck, "", true, errors.New("SECRET"), Unknown},
		{"not started", "", "", false, errors.New("SECRET"), Failed},
		{"ack without started", successAck, "", false, nil, Failed},
		{"nonjson", "SECRET", "SECRET", true, errors.New("SECRET"), Unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &mockClient{out: feishumeeting.Output{Stdout: []byte(tc.stdout), Stderr: []byte(tc.stderr), Started: tc.started}, err: tc.err}
			status, err := (Replier{Config: fixtureConfig(), Client: client}).ReplyText(context.Background(), fixtureTarget(), "hello $(literal) `literal`\n世界")
			if status != tc.want || (err == nil) != (status == Accepted) || err != nil && strings.Contains(err.Error(), "SECRET") || len(client.calls) != 1 {
				t.Fatalf("status=%s err=%v calls=%v", status, err, client.calls)
			}
			want := []string{"--profile", "test-profile", "im", "+messages-reply", "--as", "bot", "--message-id", "om_source", "--msg-type", "post", "--content", client.calls[0][11], "--idempotency-key", "durable-outgoing-id", "--format", "json"}
			if !reflect.DeepEqual(client.calls[0], want) {
				t.Fatalf("reply command changed destination, identity or content: %v", client.calls[0])
			}
			if text := replyMarkdownFixture(t, client.calls[0][11]); text != "hello $(literal) `literal`\n世界" {
				t.Fatal("rich text changed the original reply body", text)
			}
		})
	}
}

func TestInvalidTargetsAndTextCannotStartAReply(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Config, *state.ChannelReplyTarget, *string)
	}{
		{"invalid config", func(c *Config, _ *state.ChannelReplyTarget, _ *string) { c.OwnerOpenID = "" }},
		{"wrong kind", func(_ *Config, t *state.ChannelReplyTarget, _ *string) { t.Kind = state.ReplyTargetFeishuMeeting }},
		{"invalid chat", func(_ *Config, t *state.ChannelReplyTarget, _ *string) { t.ID = "oc_chat\n--as user" }},
		{"missing original message", func(_ *Config, t *state.ChannelReplyTarget, _ *string) { t.MessageID = "" }},
		{"invalid original message", func(_ *Config, t *state.ChannelReplyTarget, _ *string) { t.MessageID = "oc_chat" }},
		{"missing uuid", func(_ *Config, t *state.ChannelReplyTarget, _ *string) { t.UUID = "" }},
		{"long uuid", func(_ *Config, t *state.ChannelReplyTarget, _ *string) { t.UUID = strings.Repeat("a", 51) }},
		{"unsafe uuid", func(_ *Config, t *state.ChannelReplyTarget, _ *string) { t.UUID = "uuid\nsecret" }},
		{"empty text", func(_ *Config, _ *state.ChannelReplyTarget, text *string) { *text = "   " }},
		{"nul text", func(_ *Config, _ *state.ChannelReplyTarget, text *string) { *text = "hello\x00" }},
		{"invalid utf8", func(_ *Config, _ *state.ChannelReplyTarget, text *string) { *text = string([]byte{255}) }},
		{"oversized text", func(_ *Config, _ *state.ChannelReplyTarget, text *string) {
			*text = strings.Repeat("a", maxReplyBytes-len("[AI] ")+1)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, target, text := fixtureConfig(), fixtureTarget(), "hello"
			tc.mutate(&config, &target, &text)
			client := &mockClient{}
			status, err := (Replier{Config: config, Client: client}).ReplyText(context.Background(), target, text)
			if status != Failed || err == nil || len(client.calls) != 0 {
				t.Fatalf("invalid reply reached CLI: %s %v calls=%v", status, err, client.calls)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &mockClient{}
	if status, err := (Replier{Config: fixtureConfig(), Client: client}).ReplyText(ctx, fixtureTarget(), "hello"); status != Failed || err == nil || len(client.calls) != 0 {
		t.Fatal("canceled call started a reply", status, err)
	}
}

func TestReplyByteLimitIncludesPrefixAndPreservesMultibyteText(t *testing.T) {
	client := &mockClient{out: feishumeeting.Output{Stdout: []byte(successAck), Started: true}}
	text := strings.Repeat("中", 1331) + "ab"
	status, err := (Replier{Config: fixtureConfig(), Client: client}).ReplyText(context.Background(), fixtureTarget(), text)
	if status != Accepted || err != nil || len(client.calls) != 1 || len("[AI]\n")+len(replyMarkdownFixture(t, client.calls[0][11])) != maxReplyBytes {
		t.Fatalf("exact byte limit: %s %v calls=%v", status, err, client.calls)
	}
}

func replyMarkdownFixture(t *testing.T, raw string) string {
	t.Helper()
	var post map[string]struct {
		Content [][]map[string]string `json:"content"`
	}
	if err := json.Unmarshal([]byte(raw), &post); err != nil {
		t.Fatal("reply content is not a post JSON object", err)
	}
	rows := post["zh_cn"].Content
	if len(post) != 1 || len(rows) != 2 || len(rows[0]) != 1 || len(rows[1]) != 1 ||
		len(rows[0][0]) != 2 || rows[0][0]["tag"] != "text" || rows[0][0]["text"] != "[AI]" ||
		len(rows[1][0]) != 2 || rows[1][0]["tag"] != "md" {
		t.Fatal("reply must isolate its text label from the sole Markdown body", raw)
	}
	return rows[1][0]["text"]
}

func TestRichReplyPreservesMarkdownWithoutMediaResolutionOrStructuralInjection(t *testing.T) {
	client := &mockClient{out: feishumeeting.Output{Stdout: []byte(successAck), Started: true}}
	text := "# 格式测试\n\n**粗体**与*斜体*\n\n---\n\n- 第一项\n\n```go\nfmt.Println(\"结果\\路径\")\n```\n\n" +
		"[链接](https://example.invalid) ![图片](https://example.invalid/private.png)\n" +
		"$(literal) `literal` {\"tag\":\"img\",\"image_key\":\"img_fake\"}\n\n---\n⏱ 执行 7.0 s · 🤖 actual-model"
	status, err := (Replier{Config: fixtureConfig(), Client: client}).ReplyText(t.Context(), fixtureTarget(), text)
	if status != Accepted || err != nil || len(client.calls) != 1 {
		t.Fatal("rich reply was not one confirmed mutation", status, err, len(client.calls))
	}
	args := client.calls[0]
	if len(args) != 16 || args[8] != "--msg-type" || args[9] != "post" || args[10] != "--content" || args[12] != "--idempotency-key" || args[13] != fixtureTarget().UUID {
		t.Fatal("rich reply changed its transport or durable identity", args)
	}
	for _, flag := range []string{"--markdown", "--text", "--image", "--attachment", "--reply-in-thread"} {
		for _, arg := range args {
			if arg == flag {
				t.Fatal("rich reply acquired media resolution or a different delivery stream", flag)
			}
		}
	}
	if got := replyMarkdownFixture(t, args[11]); got != text {
		t.Fatal("Markdown, code, literal syntax or footer changed during JSON encoding", got)
	}
}

func TestRichReplyLabelCannotDisplaceMarkdownFirstLine(t *testing.T) {
	for name, body := range map[string]string{
		"heading":     "# 标题\n\n正文。",
		"list":        "- 第一项\n- 第二项",
		"fenced code": "```go\nfmt.Println(\"结果\")\n```\n\n---\n⏱ 执行 7.0 s · 🤖 actual-model",
	} {
		t.Run(name, func(t *testing.T) {
			client := &mockClient{out: feishumeeting.Output{Stdout: []byte(successAck), Started: true}}
			status, err := (Replier{Config: fixtureConfig(), Client: client}).ReplyText(t.Context(), fixtureTarget(), body)
			if status != Accepted || err != nil || len(client.calls) != 1 {
				t.Fatal("Markdown reply was not one confirmed mutation", status, err, len(client.calls))
			}
			if got := replyMarkdownFixture(t, client.calls[0][11]); got != body {
				t.Fatal("label or encoding changed Markdown block boundaries", got)
			}
		})
	}
}
