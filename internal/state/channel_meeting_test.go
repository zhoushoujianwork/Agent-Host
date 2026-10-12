package state

import (
	"database/sql"
	"errors"
	"testing"
	"time"
)

func meetingChannelInput(id string) ChannelMessage {
	m := channelInput(id)
	m.Channel = ReplyTargetFeishuMeeting
	m.ConversationType, m.ConversationID = "2", "700000000000000001"
	m.AgentID, m.AudienceID = "default", "meeting-audience"
	m.ReplyExpiresAt = time.Time{}
	m.ReplyTarget = &ChannelReplyTarget{Kind: ReplyTargetFeishuMeeting, ID: m.ConversationID, UUID: "reply-" + id}
	m.DisableReactions = true
	return m
}

func TestMeetingReceiptNeedsNoWebhookAndProducesNoReactionOutbox(t *testing.T) {
	s, _ := testStore(t)
	m := meetingChannelInput("m1")
	if inserted, err := s.EnqueueChannelMessage(testContext, m, ""); err != nil || !inserted {
		t.Fatalf("meeting enqueue: %v %v", inserted, err)
	}
	url, err := s.ChannelReplyURL(testContext, m.ID)
	if err != nil || url != "" {
		t.Fatalf("invented webhook: %q %v", url, err)
	}
	m = claimChannel(t, s, m.Profile)
	if m.ReplyTarget == nil || m.ReplyTarget.ID != m.ConversationID || !m.ReplyExpiresAt.IsZero() {
		t.Fatalf("lost target: %+v", m)
	}
	if err := s.FinishChannelMessage(testContext, m.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareChannelReply(testContext, m.ID, "answer"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishChannelReply(testContext, m.ID, "accepted", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverChannels(testContext); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ChannelReactions(testContext, m.ID)
	if err != nil || len(rows) != 0 {
		t.Fatalf("unsupported feedback was queued: %+v %v", rows, err)
	}
}

func TestMeetingReplyTargetValidationAndDuplicateCollision(t *testing.T) {
	s, _ := testStore(t)
	for name, change := range map[string]func(*ChannelMessage){
		"unknown kind":         func(m *ChannelMessage) { m.ReplyTarget.Kind = "arbitrary-api" },
		"wrong channel":        func(m *ChannelMessage) { m.Channel = "dingtalk" },
		"wrong meeting":        func(m *ChannelMessage) { m.ReplyTarget.ID = "other" },
		"missing uuid":         func(m *ChannelMessage) { m.ReplyTarget.UUID = "" },
		"fake expiry":          func(m *ChannelMessage) { m.ReplyExpiresAt = time.Now().Add(time.Hour) },
		"reactions":            func(m *ChannelMessage) { m.DisableReactions = false },
		"private audience":     func(m *ChannelMessage) { m.AudienceID = OwnerAudienceID },
		"private conversation": func(m *ChannelMessage) { m.ConversationType = "1" },
	} {
		t.Run(name, func(t *testing.T) {
			m := meetingChannelInput(name)
			change(&m)
			if inserted, err := s.EnqueueChannelMessage(testContext, m, ""); err == nil || inserted {
				t.Fatalf("invalid target admitted: %v %v", inserted, err)
			}
		})
	}
	m := meetingChannelInput("stable")
	if _, err := s.EnqueueChannelMessage(testContext, m, testReplyURL); err == nil {
		t.Fatal("meeting accepted a webhook")
	}
	if inserted, err := s.EnqueueChannelMessage(testContext, m, ""); err != nil || !inserted {
		t.Fatal(inserted, err)
	}
	duplicate := meetingChannelInput("stable")
	duplicate.Profile, duplicate.Binding = "new-profile", "new-binding"
	if inserted, err := s.EnqueueChannelMessage(testContext, duplicate, ""); err != nil || inserted {
		t.Fatal("config change must not requeue", inserted, err)
	}
	duplicate.ReplyTarget.UUID = "replacement"
	if inserted, err := s.EnqueueChannelMessage(testContext, duplicate, ""); err == nil || inserted {
		t.Fatal("duplicate replaced delivery identity", inserted, err)
	}
}

func TestMeetingInterruptedSendKeepsTargetAndCannotReplay(t *testing.T) {
	s, _ := testStore(t)
	m := meetingChannelInput("pending")
	if _, err := s.EnqueueChannelMessage(testContext, m, ""); err != nil {
		t.Fatal(err)
	}
	claimChannel(t, s, m.Profile)
	if err := s.FinishChannelMessage(testContext, m.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareChannelReply(testContext, m.ID, "exact outgoing answer"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverChannels(testContext); err != nil {
		t.Fatal(err)
	}
	saved, err := s.ChannelMessage(testContext, m.ID)
	if err != nil || saved.ReplyStatus != "unknown" || saved.ReplyText != "exact outgoing answer" || saved.ReplyTarget == nil || *saved.ReplyTarget != *m.ReplyTarget {
		t.Fatalf("pending receipt lost: %+v %v", saved, err)
	}
	if err := s.PrepareChannelReply(testContext, m.ID, "retry"); err == nil {
		t.Fatal("ambiguous meeting send replayed")
	}
	if _, err := s.EnqueueChannelMessage(testContext, m, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNextChannelMessage(testContext, m.Profile); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("completed task requeued: %v", err)
	}
}
