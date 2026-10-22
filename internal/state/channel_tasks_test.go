package state

import (
	"agent-host/internal/event"
	"strings"
	"testing"
)

func TestTicketReferenceRetainsFailedIntakeAndInstanceLink(t *testing.T) {
	s, _ := testStore(t)
	a := taskCard("failed-card")
	a.Input.Quote.Body = "FTP request https://aflow.dingtalk.com/?pcLink=https%3A%2F%2Fexample.test%2F%3FprocInstId%3Dinstance-exact%26taskId%3Dold-task"
	enqueueChannel(t, s, a)
	a = claimChannel(t, s, a.Profile)
	if err := s.FinishChannelMessage(testContext, a.ID, "failed", "routing HTTP 402"); err != nil {
		t.Fatal(err)
	}
	enqueueChannel(t, s, taskCard("unrelated-card"))
	follow := channelInput("explicit-follow")
	follow.ConversationType, follow.Text, follow.ReplyToID = "1", "Read comments", a.MessageID
	enqueueChannel(t, s, follow)
	follow, err := s.ChannelMessage(testContext, follow.ID)
	if err != nil || follow.TaskRootID != a.ID {
		t.Fatal(follow, err)
	}
	got, err := s.ChannelTicketContext(testContext, follow.ID)
	if err != nil || got == nil || got.MessageID != a.MessageID || strings.Join(got.InstanceIDs, ",") != "instance-exact" || got.Quote.Body != a.Input.Quote.Body {
		t.Fatal("failed intake lost its exact source and instance", got, err)
	}
	// A forged cross-sender root must not expose card content.
	follow.SenderID = "another-user"
	recoveryPayload(t, s, "channel_messages", follow.ID, follow)
	if _, err := s.ChannelTicketContext(testContext, follow.ID); err == nil {
		t.Fatal("cross-sender ticket context allowed")
	}
}

func TestTitleOnlyTicketReplySelectsOriginalNotLatest(t *testing.T) {
	s, _ := testStore(t)
	a := taskCard("title-original")
	enqueueChannel(t, s, a)
	a = claimChannel(t, s, a.Profile)
	reactionReply(t, s, a, "completed", "accepted")
	a, _ = s.ChannelMessage(testContext, a.ID)
	enqueueChannel(t, s, taskCard("newer-unrelated"))
	follow := channelInput("title-follow")
	follow.ConversationType, follow.Text = "1", "Read the revised comment"
	follow.Input = &ChannelInput{Quote: &ChannelQuote{MessageID: "unreturned-bot-id", MessageType: "text", Body: CardReplyTitle(a)}}
	enqueueChannel(t, s, follow)
	got, err := s.ChannelMessage(testContext, follow.ID)
	if err != nil || got.TaskRootID != a.ID || got.AmbiguousTask {
		t.Fatal("title selected wrong ticket", got, err)
	}
	follow.ID, follow.MessageID = "unresolved-title", "unresolved-title"
	follow.Input.Quote.Body = "Agent-Host 回复"
	enqueueChannel(t, s, follow)
	got, err = s.ChannelMessage(testContext, follow.ID)
	if err != nil || !got.AmbiguousTask || got.TaskRootID != "" {
		t.Fatal("unresolved quote guessed a ticket", got, err)
	}
	follow.ID, follow.MessageID = "ordinary-quote", "ordinary-quote"
	follow.Text, follow.Input.Quote.Body = "Explain this sentence", "A colleague's unrelated discussion"
	enqueueChannel(t, s, follow)
	got, err = s.ChannelMessage(testContext, follow.ID)
	if err != nil || got.AmbiguousTask || got.TaskRootID != "" {
		t.Fatal("unrelated quotation was captured by a ticket", got, err)
	}
}

func taskCard(id string) ChannelMessage {
	m := channelInput(id)
	m.ConversationType = "1"
	m.MessageType = "interactiveCard"
	m.Text = "Forwarded interactive card"
	m.Input = &ChannelInput{Quote: &ChannelQuote{MessageType: "interactiveCard", Title: id}}
	return m
}

func TestPrivateCardTaskRoutingAndAmbiguousConfirmation(t *testing.T) {
	s, _ := testStore(t)
	a, b := taskCard("a"), taskCard("b")
	enqueueChannel(t, s, a)
	single := channelInput("single")
	single.ConversationType = "1"
	single.Text = "同意并处理一下"
	enqueueChannel(t, s, single)
	stored, err := s.ChannelMessage(testContext, single.ID)
	if err != nil || stored.TaskRootID != a.ID {
		t.Fatal("one-card followup lost task", stored, err)
	}
	enqueueChannel(t, s, b)
	ambiguous := channelInput("ambiguous")
	ambiguous.ConversationType = "1"
	ambiguous.Text = "同意"
	enqueueChannel(t, s, ambiguous)
	stored, err = s.ChannelMessage(testContext, ambiguous.ID)
	if err != nil || !stored.AmbiguousTask || stored.TaskRootID != "" {
		t.Fatal("ambiguous confirmation selected a card", stored, err)
	}
	quoted := channelInput("quoted")
	quoted.ConversationType = "1"
	quoted.Text = "继续"
	quoted.Input = &ChannelInput{Quote: &ChannelQuote{MessageID: a.MessageID}}
	enqueueChannel(t, s, quoted)
	stored, err = s.ChannelMessage(testContext, quoted.ID)
	if err != nil || stored.TaskRootID != a.ID || stored.AmbiguousTask {
		t.Fatal("explicit reference did not select original ticket", stored, err)
	}
	foreign := quoted
	foreign.ID = "foreign"
	foreign.MessageID = "foreign"
	foreign.SenderID = "other"
	enqueueChannel(t, s, foreign)
	stored, err = s.ChannelMessage(testContext, foreign.ID)
	if err != nil || stored.TaskRootID != "" {
		t.Fatal("reference crossed sender identity", stored, err)
	}
	if inserted, err := s.EnqueueChannelMessage(testContext, a, testReplyURL); err != nil || inserted {
		t.Fatal("duplicate card replayed", inserted, err)
	}
}

func TestPrivateCardReactionOutboxIncludesDurablePhases(t *testing.T) {
	s, _ := testStore(t)
	m := taskCard("reaction-card")
	// Empty callback previews still need receipt feedback before DWS retrieval.
	m.Input.Quote.Title = ""
	enqueueChannel(t, s, m)
	m = claimChannel(t, s, m.Profile)
	run := channelRun(t, s, m)
	if err := s.CreateChannelRun(testContext, run, m.ID); err != nil {
		t.Fatal(err)
	}
	run, err := s.Run(testContext, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.AutomaticMemorySkipReason() != "independent_card_task" {
		t.Fatal("card run not isolated")
	}
	e := event.Event{RunID: run.ID, Type: "phase.started", Phase: "execute"}
	if err := s.AppendEvent(testContext, &e); err != nil {
		t.Fatal(err)
	}
	reactionReply(t, s, m, "completed", "accepted")
	rows := phaseRows(t, s, m.ID)
	for _, stage := range []string{"received", "processing", "execute", "replying", "completed"} {
		if _, ok := rows[stage]; !ok {
			t.Fatal("missing card reaction", stage, rows)
		}
	}
	if rows["execute"].SourceRunID != run.ID || rows["execute"].SourceEventSeq != e.Seq {
		t.Fatal("phase not bound to card event")
	}
}

func TestPrivateCardRecentContextAndHistoryStayWithinTask(t *testing.T) {
	s, _ := testStore(t)
	a, b := taskCard("history-a"), taskCard("history-b")
	for _, m := range []ChannelMessage{a, b} {
		enqueueChannel(t, s, m)
		m = claimChannel(t, s, m.Profile)
		run := channelRun(t, s, m)
		if err := s.CreateChannelRun(testContext, run, m.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.CompleteRun(testContext, run.ID, "completed", m.ID+" answer", "test", ""); err != nil {
			t.Fatal(err)
		}
		reactionReply(t, s, m, "completed", "accepted")
	}
	follow := channelInput("history-follow")
	follow.ConversationType = "1"
	follow.Text = "Continue"
	follow.ReplyToID = a.MessageID
	enqueueChannel(t, s, follow)
	follow = claimChannel(t, s, follow.Profile)
	run := channelRun(t, s, follow)
	if err := s.CreateChannelRun(testContext, run, follow.ID); err != nil {
		t.Fatal(err)
	}
	recent, err := s.ChannelRecentRequests(testContext, run.SessionID, follow.ID)
	if err != nil || len(recent) != 1 || recent[0].MessageID != a.MessageID {
		t.Fatal("other ticket entered recent context", recent, err)
	}
	binding, err := s.ConversationHistoryBinding(testContext, follow.ID)
	if err != nil {
		t.Fatal(err)
	}
	history, err := s.SearchConversationHistory(testContext, binding, ConversationHistorySearch{Limit: 20})
	if err != nil || len(history.Turns) != 1 || history.Turns[0].MessageID != a.MessageID {
		t.Fatal("other ticket entered task history", history, err)
	}
}
