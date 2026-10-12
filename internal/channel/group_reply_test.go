package channel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"agent-host/internal/channel/dingtalk"
	"agent-host/internal/harness"
	"agent-host/internal/routing"
	"agent-host/internal/state"
)

func TestGroupRepliesQuoteCurrentMessageAndMentionItsSender(t *testing.T) {
	f := newRoutingFixture(t)
	instanceDispatch(t, f, map[string]string{})
	for _, tc := range []struct{ id, sender, text, status string }{
		{"answer", "alice", "@Helper general", "completed"},
		{"command", "bob", "@Helper /status", "completed"},
		{"uncertain", "alice", "@Helper ambiguous request", "completed"},
		{"failure", "bob", "@Helper /new extra", "failed"},
		{"reset", "alice", "@Helper /new", "completed"},
	} {
		m := f.process(t, groupInput(t, f.serviceFixture, tc.id, tc.sender, "shared-group", tc.text))
		calls := f.replier.snapshot()
		call := calls[len(calls)-1]
		question := strings.TrimPrefix(tc.text, "@Helper ")
		if m.Status != tc.status || call.atUserID != tc.sender || !strings.HasPrefix(call.text, "@"+tc.sender+"\n\n> **回复：** "+question+"\n\n") {
			t.Fatalf("reply lost its current sender/question: message=%+v call=%+v", m, call)
		}
		if m.ReplyText != call.text || m.Text != tc.text || strings.Contains(call.text, "@Helper") {
			t.Fatal("reply differs from durable presentation or changed source", m, call)
		}
	}
	private := f.process(t, f.message(t, "private", "general", "text", "private-chat"))
	calls := f.replier.snapshot()
	if call := calls[len(calls)-1]; call.atUserID != "" || strings.Contains(call.text, "> **回复：**") || call.text != private.ReplyText {
		t.Fatal("private message acquired group presentation", call)
	}
}

func TestGroupStartupFailureStillAddressesCurrentSender(t *testing.T) {
	f := newRoutingFixture(t)
	f.service.ResolveAgent = func(context.Context, state.ChannelMessage) (*Service, error) {
		return nil, errors.New("internal configuration /private/runtime")
	}
	m := f.process(t, groupInput(t, f.serviceFixture, "unavailable", "bob", "group", "hello"))
	call := f.replier.snapshot()[0]
	if m.Status != "failed" || call.atUserID != "bob" || !strings.Contains(call.text, "> **回复：** hello") || !strings.Contains(call.text, "配置不可用") || strings.Contains(call.text, "/private") {
		t.Fatal("startup failure lost addressing or disclosed diagnostics", m, call)
	}
}

func TestGroupQuestionPreviewCannotForgeMentionsOrMarkdown(t *testing.T) {
	m := state.ChannelMessage{Channel: "dingtalk", ConversationType: "2", SenderID: "alice", MessageType: "text", Text: "@Helper @mallory\n> [open](https://example.test) **urgent** <b>text</b>"}
	reply := replyForMessage(m, "Done.")
	if reply.AtUserID != "alice" || strings.Contains(reply.Text, "@mallory") || strings.Contains(reply.Text, "[open]") || strings.Contains(reply.Text, "<b>") || strings.Count(reply.Text, "\n> ") != 1 {
		t.Fatal("question preview created formatting or extra addressing", reply)
	}
	if !strings.Contains(reply.Text, "＠mallory") || !strings.Contains(reply.Text, "\\[open\\]") || !strings.HasSuffix(reply.Text, "\n\nDone.") {
		t.Fatal("safe preview or answer lost", reply)
	}
	for _, text := range []string{"Inspect /Users/private/workspace/settings", "Read ~/private/file", `Inspect C:\private\file`, "token=synthetic-credential", "密码：synthetic-credential", "Authorization: Bearer synthetic-credential"} {
		m.Text = text
		reply = replyForMessage(m, "Done.")
		if strings.Contains(reply.Text, "synthetic-credential") || strings.Contains(reply.Text, "private") || !strings.Contains(reply.Text, "引用已省略") || reply.AtUserID != "alice" {
			t.Fatal("preview repeated a path or credential assignment", reply)
		}
		if m.Text != text {
			t.Fatal("preview rewrote evidence")
		}
	}
}

func TestGroupReplyBudgetIncludesMentionAndQuote(t *testing.T) {
	m := state.ChannelMessage{Channel: "dingtalk", ConversationType: "2", SenderID: strings.Repeat("a", 512), MessageType: "text", Text: "@Helper " + strings.Repeat("[多字节]", 500)}
	body := "```go\n" + strings.Repeat("fmt.Println(\"结果\")\n", 500) + "```"
	reply := replyForMessage(m, body)
	if len(reply.Text) > dingtalk.MaxReplyBytes || !utf8.ValidString(reply.Text) || closingFence(reply.Text) != "" || !strings.Contains(reply.Text, "节选") || strings.Count(reply.Text, "\n> **回复：**") != 1 || reply.AtUserID != m.SenderID {
		t.Fatalf("invalid bounded group reply: bytes=%d reply=%+v", len(reply.Text), reply)
	}
	short := "  **Done**\n\n```go\nx := 1"
	reply = replyForMessage(m, short)
	if !strings.HasSuffix(reply.Text, short+"\n```") {
		t.Fatal("short body formatting changed or unclosed", reply)
	}
}

func TestGroupUnsupportedContentUsesNeutralQuote(t *testing.T) {
	m := state.ChannelMessage{Channel: "dingtalk", ConversationType: "2", SenderID: "alice", MessageType: "image", Text: "private unsupported payload"}
	reply := replyForMessage(m, "请将问题作为文字发送。")
	if reply.AtUserID != "alice" || !strings.Contains(reply.Text, "原消息（非文字内容）") || strings.Contains(reply.Text, m.Text) {
		t.Fatal("unsupported message preview reflected nontext content", reply)
	}
}

func TestGroupLongRunReplyPreservesPublicRuntimeFooter(t *testing.T) {
	f := newRoutingFixture(t)
	f.service.Router = serviceRouter(func(context.Context, routing.Request) (routing.Decision, error) {
		return routing.Decision{Kind: "default", Reason: "Footer formatting fixture."}, nil
	})
	instanceDispatch(t, f, map[string]string{})
	f.runner.run = func(_ context.Context, req harness.Request) (harness.Result, error) {
		return harness.Result{Status: "completed", Text: "```go\n" + strings.Repeat("fmt.Println(\"结果\")\n", 500), Model: "public-actual-model", NativeSessionID: req.NativeSessionID}, nil
	}
	m := f.process(t, groupInput(t, f.serviceFixture, "long-answer", "alice", "shared-group", "@Helper "+strings.Repeat("多字节问题", 100)))
	if m.Status != "completed" || m.ReplyStatus != "accepted" || len(m.ReplyText) > dingtalk.MaxReplyBytes || !utf8.ValidString(m.ReplyText) {
		t.Fatalf("long group reply failed or exceeded its budget: status=%s delivery=%s error=%s bytes=%d", m.Status, m.ReplyStatus, m.Error, len(m.ReplyText))
	}
	if !strings.HasSuffix(m.ReplyText, " · 🤖 public-actual-model") || !strings.Contains(m.ReplyText, "\n\n---\n⏱ 执行 ") || !strings.Contains(m.ReplyText, "节选") || closingFence(m.ReplyText) != "" || strings.Count(m.ReplyText, "节选") != 1 {
		t.Fatal("group quote displaced footer or left it inside code", m.ReplyText)
	}
	if !strings.HasPrefix(m.ReplyText, "@alice\n\n> **回复：** ") || strings.Contains(m.ReplyText, f.host.Config.Model) {
		t.Fatal("group reply lost requester or used configured model", m.ReplyText)
	}
}
