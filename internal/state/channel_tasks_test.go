package state

import (
	"agent-host/internal/event"
	"testing"
)

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
