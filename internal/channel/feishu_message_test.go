package channel

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"agent-host/internal/harness"
	"agent-host/internal/state"
)

func newFeishuFixture(t *testing.T) (*serviceFixture, *meetingTestReplier) {
	t.Helper()
	f, replier := newMeetingFixture(t)
	f.service.Channel = state.ReplyTargetFeishu
	resolve := f.service.ResolveAgent
	f.service.ResolveAgent = func(ctx context.Context, m state.ChannelMessage) (*Service, error) {
		child, err := resolve(ctx, m)
		if child != nil {
			child.Channel = state.ReplyTargetFeishu
		}
		return child, err
	}
	return f, replier
}

func feishuMessage(id, conversationType, chat, sender, text string) Message {
	return Message{ID: "om_" + id, ConversationID: chat, ConversationType: conversationType, SenderID: sender,
		TenantID: "tenant", MessageType: "text", Text: text, CreatedAt: time.Now().UTC(),
		ReplyTarget: state.ChannelReplyTarget{Kind: state.ReplyTargetFeishu, ID: chat, MessageID: "om_" + id}}
}

func TestFeishuOwnerGroupAndMeetingShareServiceWithoutSharingConversationHistory(t *testing.T) {
	f, replier := newFeishuFixture(t)
	f.service.SelectMessageAgent = func(_ context.Context, m Message) (AgentSelection, error) {
		audience := "shared-" + m.ReplyTarget.Kind
		if m.ReplyTarget.Kind == state.ReplyTargetFeishu && m.ConversationType == "1" {
			audience = state.OwnerAudienceID
		}
		return AgentSelection{AgentID: "default", AudienceID: audience}, nil
	}
	const chat = "oc_same-chat"
	privateA := processMeeting(t, f, feishuMessage("private-a", "1", chat, "ou_owner", "Private first question."))
	groupA := processMeeting(t, f, feishuMessage("group-a", "2", chat, "ou_alice", "Group first question."))
	meetingInput := meetingMessage("meeting-a", chat, "ou_owner", "Meeting question.")
	// Use the same provider source ID too: the actual channel must distinguish it.
	meetingInput.ID = "om_private-a"
	meeting := processMeeting(t, f, meetingInput)
	legacy := *f.service
	legacy.Channel, legacy.DisableReactions, legacy.ResolveAgent = "dingtalk", false, nil
	legacy.Replier, legacy.Reactor, legacy.TextReplier = f.replier, &serviceReactor{}, nil
	if err := legacy.Receive(t.Context(), f.message(t, "om_private-a", "DingTalk question.", "text", chat)); err != nil {
		t.Fatal(err)
	}
	dingtalk := f.next(t)
	if err := legacy.process(t.Context(), dingtalk); err != nil {
		t.Fatal(err)
	}
	groupB := processMeeting(t, f, feishuMessage("group-b", "2", chat, "ou_bob", "Group follow-up."))
	privateB := processMeeting(t, f, feishuMessage("private-b", "1", chat, "ou_owner", "Private follow-up."))
	if privateA.AudienceID != state.OwnerAudienceID || privateA.SessionID != privateB.SessionID || groupA.SessionID != groupB.SessionID || groupA.Binding != groupB.Binding || privateA.SessionID == groupA.SessionID || groupA.SessionID == meeting.SessionID {
		t.Fatal("private/shared conversation partition was lost", privateA, groupA, meeting)
	}
	if privateA.ID == meeting.ID || privateA.Binding == meeting.Binding || privateA.ReplyTarget.UUID == meeting.ReplyTarget.UUID || len(privateA.ReplyTarget.UUID) != 48 || len(meeting.ReplyTarget.UUID) != 64 {
		t.Fatal("actual provider did not scope receipt/reply identity")
	}
	for _, pair := range [][2]state.ChannelMessage{{privateB, privateA}, {groupB, groupA}} {
		binding, err := f.service.Store.ConversationHistoryBinding(t.Context(), pair[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		history, err := f.service.Store.SearchConversationHistory(t.Context(), binding, state.ConversationHistorySearch{Limit: 10})
		if err != nil || len(history.Turns) != 1 || history.Turns[0].ReceiptID != pair[1].ID {
			t.Fatal("same chat IDs crossed provider or audience history", history, err)
		}
	}
	for _, m := range []state.ChannelMessage{privateA, privateB, groupA, groupB, meeting} {
		run, err := f.service.Store.WithPartition(m.AgentID, m.AudienceID).Run(t.Context(), m.RunID)
		if err != nil || run.Source == nil || run.Source.Channel != m.Channel || run.Source.MessageID != m.MessageID || run.Source.SenderID != m.SenderID {
			t.Fatal("mixed service changed source provenance", run.Source, err)
		}
		if m.Channel == state.ReplyTargetFeishu && m.ReplyTarget.MessageID != m.MessageID {
			t.Fatal("IM reply lost its original source message")
		}
	}
	if len(replier.calls) != 5 || len(f.replier.snapshot()) != 1 {
		t.Fatal("text/webhook delivery adapters were mixed")
	}
}

func TestFeishuIntakeRestrictsOwnerAndTransportCapabilities(t *testing.T) {
	f, _ := newFeishuFixture(t)
	for name, change := range map[string]func(*Message){
		"wrong reply message":   func(m *Message) { m.ReplyTarget.MessageID = "om_other" },
		"missing reply message": func(m *Message) { m.ReplyTarget.MessageID = "" },
		"wrong reply chat":      func(m *Message) { m.ReplyTarget.ID = "oc_other" },
		"non IM source":         func(m *Message) { m.ID, m.ReplyTarget.MessageID = "event", "event" },
		"wrong kind":            func(m *Message) { m.ReplyTarget.Kind = "dingtalk" },
		"media":                 func(m *Message) { m.MessageType = "picture" },
		"missing tenant":        func(m *Message) { m.TenantID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			m := feishuMessage(name, "1", "oc_chat", "ou_owner", "hello")
			change(&m)
			if err := f.service.ReceiveMessage(t.Context(), m); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
	private := feishuMessage("private", "1", "oc_chat", "ou_owner", "hello")
	if err := f.service.ReceiveMessage(t.Context(), private); err != nil {
		t.Fatal(err)
	}
	privateReceipt := f.next(t)
	if privateReceipt.AudienceID == state.OwnerAudienceID {
		t.Fatal("private IM silently acquired owner audience")
	}
	if err := f.service.ReceiveMessage(t.Context(), feishuMessage("group-default", "2", private.ConversationID, "ou_owner", "hello")); err != nil {
		t.Fatal(err)
	}
	if group := f.next(t); group.AudienceID == privateReceipt.AudienceID || group.Binding == privateReceipt.Binding {
		t.Fatal("private and group chat types shared an implicit partition")
	}
	f.service.SelectMessageAgent = func(context.Context, Message) (AgentSelection, error) {
		return AgentSelection{AgentID: "default", AudienceID: state.OwnerAudienceID}, nil
	}
	for _, m := range []Message{feishuMessage("group", "2", "oc_group", "ou_owner", "hello"), meetingMessage("meeting", "700000000000000001", "ou_owner", "hello")} {
		if err := f.service.ReceiveMessage(t.Context(), m); err == nil {
			t.Fatal("shared conversation acquired owner audience")
		}
	}
	f.service.Channel = state.ReplyTargetFeishuMeeting
	if err := f.service.ReceiveMessage(t.Context(), private); err == nil {
		t.Fatal("legacy meeting-only service accepted IM")
	}
}

type feishuReplyFunction func(context.Context, state.ChannelReplyTarget, string) (string, error)

func (f feishuReplyFunction) ReplyText(ctx context.Context, target state.ChannelReplyTarget, text string) (string, error) {
	return f(ctx, target, text)
}

func TestFeishuMixedInputsUseOneWorkerLimitAndStableRedeliveryIdentity(t *testing.T) {
	f, _ := newFeishuFixture(t)
	f.service.MaxConcurrentConversations = 1
	first := feishuMessage("first", "1", "oc_private", "ou_owner", "blocked first")
	meeting := meetingMessage("next", "700000000000000001", "ou_owner", "meeting next")
	for _, m := range []Message{first, meeting, first} {
		if err := f.service.ReceiveMessage(t.Context(), m); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	f.runner.run = func(ctx context.Context, req harness.Request) (harness.Result, error) {
		if strings.Contains(req.Prompt, "blocked first") {
			entered <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return harness.Result{Status: "canceled"}, ctx.Err()
			}
		}
		return harness.Result{Status: "completed", Text: "answer", Model: req.Model}, nil
	}
	delivered := make(chan state.ChannelReplyTarget, 2)
	reply := feishuReplyFunction(func(_ context.Context, target state.ChannelReplyTarget, _ string) (string, error) {
		delivered <- target
		return "accepted", nil
	})
	f.service.TextReplier = reply
	resolve := f.service.ResolveAgent
	f.service.ResolveAgent = func(ctx context.Context, m state.ChannelMessage) (*Service, error) {
		child, err := resolve(ctx, m)
		if child != nil {
			child.TextReplier = reply
		}
		return child, err
	}
	done := make(chan error, 1)
	go func() { done <- f.service.Work(ctx) }()
	waitForWorkerSignal(t, ctx, entered)
	queued, err := f.service.Store.ChannelMessage(t.Context(), Key(meeting.ReplyTarget.Kind, f.service.ClientID, meeting.TenantID, meeting.ConversationID, meeting.ID))
	if err != nil || queued.Status != "queued" || len(f.runner.snapshot()) != 1 {
		t.Fatal("meeting escaped the shared worker limit", queued, err)
	}
	close(release)
	firstTarget := waitForWorkerSignal(t, ctx, delivered)
	secondTarget := waitForWorkerSignal(t, ctx, delivered)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if firstTarget.Kind != state.ReplyTargetFeishu || firstTarget.MessageID != first.ID || secondTarget.Kind != state.ReplyTargetFeishuMeeting || secondTarget.MessageID != "" || len(f.runner.snapshot()) != 2 {
		t.Fatal("mixed queue changed delivery identity or replayed a duplicate")
	}
	f.service.Profile = "changed-runtime"
	first.ReplyTarget.UUID = "transport-cannot-change-delivery"
	if err := f.service.ReceiveMessage(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Store.ClaimNextChannelMessage(t.Context(), f.service.Profile); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("runtime change replayed original IM message", err)
	}
}

func TestFeishuLongNativeReplyKeepsFooterWithinPrefixedAPIBudget(t *testing.T) {
	f, replier := newFeishuFixture(t)
	full := "```go\n" + strings.Repeat("fmt.Println(\"结果\")\n", 500) + "```\n\nComplete native answer tail."
	f.runner.run = func(_ context.Context, req harness.Request) (harness.Result, error) {
		return harness.Result{Status: "completed", Text: full, Model: "actual-im-model", NativeSessionID: req.NativeSessionID}, nil
	}
	var wireText string
	replier.reply = func(target state.ChannelReplyTarget, body string) (string, error) {
		// Exercise the transport's total text budget, including its visible label.
		wireText = "[AI]\n" + body
		if target.Kind != state.ReplyTargetFeishu || len(wireText) > 4000 || !utf8.ValidString(wireText) {
			return "failed", errors.New("fixture API rejected its prefixed text")
		}
		return "accepted", nil
	}
	m := processMeeting(t, f, feishuMessage("long-native", "2", "oc_long-reply", "ou_alice", "Explain the result."))
	if m.Status != "completed" || m.ReplyStatus != "accepted" || len(replier.calls) != 1 || replier.calls[0].text != m.ReplyText || wireText != "[AI]\n"+m.ReplyText || len(wireText) > 4000 {
		t.Fatal("long IM answer lost its delivery or total API byte budget", m.ReplyStatus, len(wireText))
	}
	foot := regexp.MustCompile(`\n\n---\n⏱ 执行 [0-9]+(?:\.[0-9]+)? (?:ms|s) · 🤖 actual-im-model$`)
	if !foot.MatchString(wireText) || closingFence(m.ReplyText) != "" || !strings.Contains(m.ReplyText, "节选") || strings.Contains(m.ReplyText, "Complete native answer tail.") {
		t.Fatal("IM truncation lost actual model, measured duration or Markdown closure", m.ReplyText)
	}
	run, err := f.service.Store.WithPartition(m.AgentID, m.AudienceID).Run(t.Context(), m.RunID)
	if err != nil || run.Output != full || run.Model != "actual-im-model" || len(run.Output) <= 4000 {
		t.Fatal("presentation truncation changed the saved native evidence", len(run.Output), run.Model, err)
	}
}
