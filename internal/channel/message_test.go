package channel

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"agent-host/internal/harness"
	"agent-host/internal/state"
)

type meetingReplyCall struct {
	target state.ChannelReplyTarget
	text   string
}

type meetingTestReplier struct {
	calls []meetingReplyCall
	reply func(state.ChannelReplyTarget, string) (string, error)
}

func (r *meetingTestReplier) ReplyText(_ context.Context, target state.ChannelReplyTarget, text string) (string, error) {
	r.calls = append(r.calls, meetingReplyCall{target, text})
	if r.reply != nil {
		return r.reply(target, text)
	}
	return "accepted", nil
}

func newMeetingFixture(t *testing.T) (*serviceFixture, *meetingTestReplier) {
	t.Helper()
	f := newServiceFixture(t)
	r := &meetingTestReplier{}
	f.service.Channel, f.service.DisableReactions = state.ReplyTargetFeishuMeeting, true
	f.service.TextReplier, f.service.Replier, f.service.Reactor = r, nil, nil
	base := *f.service
	f.service.ResolveAgent = func(_ context.Context, m state.ChannelMessage) (*Service, error) {
		child := base
		child.Store = f.service.Store.WithPartition(m.AgentID, m.AudienceID)
		h := *f.host
		h.Store = child.Store
		h.Config.AgentID, h.Config.AudienceID = m.AgentID, m.AudienceID
		child.Host = &h
		return &child, nil
	}
	return f, r
}

func meetingMessage(id, meeting, sender, text string) Message {
	return Message{ID: meeting + ":" + id, ConversationID: meeting, ConversationType: "2", SenderID: sender,
		TenantID: "tenant", MessageType: "text", Text: text, CreatedAt: time.Now().UTC(),
		ReplyTarget: state.ChannelReplyTarget{Kind: state.ReplyTargetFeishuMeeting, ID: meeting}}
}

func processMeeting(t *testing.T, f *serviceFixture, message Message) state.ChannelMessage {
	t.Helper()
	if err := f.service.ReceiveMessage(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	m := f.next(t)
	if err := f.service.process(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	saved, err := f.service.Store.ChannelMessage(context.Background(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestMeetingServiceSharesSessionAcrossSpeakersWithIsolatedAudience(t *testing.T) {
	f, replier := newMeetingFixture(t)
	first := meetingMessage("one", "700000000000000001", "alice", "What is the next step?")
	a := processMeeting(t, f, first)
	b := processMeeting(t, f, meetingMessage("two", first.ConversationID, "bob", "Please explain further."))
	c := processMeeting(t, f, meetingMessage("one", "700000000000000002", "alice", "Separate meeting."))
	if a.Status != "completed" || a.ReplyStatus != "accepted" || a.SessionID == "" || a.SessionID != b.SessionID || a.Binding != b.Binding || c.SessionID == a.SessionID || a.AudienceID == state.OwnerAudienceID || c.AudienceID == a.AudienceID {
		t.Fatalf("meeting session isolation failed: a=%+v b=%+v c=%+v", a, b, c)
	}
	if len(replier.calls) != 3 || len(f.replier.snapshot()) != 0 {
		t.Fatal("wrong delivery adapter or count", replier.calls)
	}
	historyBinding, err := f.service.Store.ConversationHistoryBinding(context.Background(), b.ID)
	if err != nil {
		t.Fatal("meeting history is unavailable", err)
	}
	history, err := f.service.Store.SearchConversationHistory(context.Background(), historyBinding, state.ConversationHistorySearch{Limit: 10})
	if err != nil || len(history.Turns) != 1 || history.Turns[0].ReceiptID != a.ID {
		t.Fatal("meeting history lost its source or audience", history, err)
	}
	for i, m := range []state.ChannelMessage{a, b, c} {
		call := replier.calls[i]
		if m.ReplyTarget == nil || *m.ReplyTarget != call.target || call.target.UUID == "" || call.target.ID != m.ConversationID || strings.Contains(call.text, "@"+m.SenderID) {
			t.Fatal("meeting reply destination or presentation changed", call)
		}
		run, err := f.service.Store.WithPartition(m.AgentID, m.AudienceID).Run(context.Background(), m.RunID)
		if err != nil || run.Source == nil || run.Source.Channel != state.ReplyTargetFeishuMeeting || run.Source.SenderID != m.SenderID {
			t.Fatal("lost meeting sender provenance", run, err)
		}
		rows, err := f.service.Store.ChannelReactions(context.Background(), m.ID)
		if err != nil || len(rows) != 0 {
			t.Fatal("unsupported reactions acquired an outbox", rows, err)
		}
	}
	if err := f.service.ReceiveMessage(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Store.ClaimNextChannelMessage(context.Background(), f.service.Profile); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("duplicate meeting request was requeued", err)
	}
}

func TestMeetingReplyIsDurableBeforeAmbiguousDelivery(t *testing.T) {
	f, replier := newMeetingFixture(t)
	replier.reply = func(target state.ChannelReplyTarget, text string) (string, error) {
		messages, err := f.service.Store.ChannelMessages(context.Background(), 10)
		if err != nil || len(messages) != 1 || messages[0].ReplyStatus != "sending" || messages[0].ReplyText != text || messages[0].ReplyTarget == nil || *messages[0].ReplyTarget != target {
			t.Fatal("delivery preceded durable preparation", messages, err)
		}
		return "unknown", errors.New("fixture ambiguous send")
	}
	input := meetingMessage("one", "700000000000000001", "alice", "hello")
	m := processMeeting(t, f, input)
	if m.ReplyStatus != "unknown" || m.Status != "completed" {
		t.Fatal("execution and delivery were conflated", m)
	}
	if err := f.service.Store.RecoverChannels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.service.ReceiveMessage(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Store.ClaimNextChannelMessage(context.Background(), f.service.Profile); !errors.Is(err, sql.ErrNoRows) || len(replier.calls) != 1 {
		t.Fatal("ambiguous send replayed", err)
	}
}

func TestMeetingInputRejectsUnsupportedCapabilitiesAndOwnerPartition(t *testing.T) {
	f, _ := newMeetingFixture(t)
	for name, change := range map[string]func(*Message){
		"media":   func(m *Message) { m.MessageType = "picture" },
		"private": func(m *Message) { m.ConversationType = "1" },
		"kind":    func(m *Message) { m.ReplyTarget.Kind = "arbitrary" },
		"target":  func(m *Message) { m.ReplyTarget.ID = "different" },
		"tenant":  func(m *Message) { m.TenantID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			m := meetingMessage(name, "700000000000000001", "alice", "hello")
			change(&m)
			if err := f.service.ReceiveMessage(context.Background(), m); err == nil {
				t.Fatal("invalid meeting input accepted")
			}
		})
	}
	f.service.SelectMessageAgent = func(context.Context, Message) (AgentSelection, error) {
		return AgentSelection{AgentID: "default", AudienceID: state.OwnerAudienceID}, nil
	}
	if err := f.service.ReceiveMessage(context.Background(), meetingMessage("owner", "700000000000000001", "alice", "hello")); err == nil {
		t.Fatal("meeting acquired private owner audience")
	}
}

func TestMeetingConversationKeysSeparateConfigurationAndAgents(t *testing.T) {
	f, _ := newMeetingFixture(t)
	ctx := context.Background()
	input := meetingMessage("one", "700000000000000001", "alice", "hello")
	if err := f.service.ReceiveMessage(ctx, input); err != nil {
		t.Fatal(err)
	}
	a := f.next(t)
	f.service.Profile = "another-profile"
	input.ID = "another"
	if err := f.service.ReceiveMessage(ctx, input); err != nil {
		t.Fatal(err)
	}
	b := f.next(t)
	if a.Binding == b.Binding || a.AudienceID == b.AudienceID {
		t.Fatal("profile change reused meeting partition")
	}
	f.service.SelectMessageAgent = func(context.Context, Message) (AgentSelection, error) {
		return AgentSelection{AgentID: "support", AudienceID: b.AudienceID}, nil
	}
	input.ID = "support"
	if err := f.service.ReceiveMessage(ctx, input); err != nil {
		t.Fatal(err)
	}
	c := f.next(t)
	if b.Binding == c.Binding {
		t.Fatal("agent change reused native conversation")
	}
}

func TestMeetingTextKeepsLiteralAddressAndHasConservativeOutputLimit(t *testing.T) {
	m := state.ChannelMessage{Channel: state.ReplyTargetFeishuMeeting, ConversationType: "2", Text: "@alice literal question"}
	if addressedText(m) != m.Text {
		t.Fatal("DingTalk mention parser changed meeting input")
	}
	text := meetingReplyText(strings.Repeat("中", 2000))
	if !utf8.ValidString(text) || len(text) > 4000 || !strings.Contains(text, "节选") {
		t.Fatal("meeting text output is not safely bounded")
	}
}

func TestMeetingLongRunReplyPreservesPublicRuntimeFooter(t *testing.T) {
	f, replier := newMeetingFixture(t)
	f.runner.run = func(_ context.Context, req harness.Request) (harness.Result, error) {
		return harness.Result{Status: "completed", Text: "```go\n" + strings.Repeat("fmt.Println(\"结果\")\n", 500), Model: "public-actual-model", NativeSessionID: req.NativeSessionID}, nil
	}
	m := processMeeting(t, f, meetingMessage("long-answer", "700000000000000001", "alice", "hello"))
	if m.Status != "completed" || m.ReplyStatus != "accepted" || len(replier.calls) != 1 || replier.calls[0].text != m.ReplyText || len(m.ReplyText) > 4000 {
		t.Fatal("meeting reply lost its delivery or byte budget", m.ReplyText)
	}
	if !strings.HasSuffix(m.ReplyText, " · 🤖 public-actual-model") || !strings.Contains(m.ReplyText, "\n\n---\n⏱ 执行 ") || closingFence(m.ReplyText) != "" || !strings.Contains(m.ReplyText, "节选") {
		t.Fatal("meeting reply lost runtime footer or code closure", m.ReplyText)
	}
}
