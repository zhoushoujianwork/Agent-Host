package state

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func feishuChannelInput(id, conversationType string) ChannelMessage {
	m := meetingChannelInput(id)
	m.Channel, m.MessageID = ReplyTargetFeishu, "om_"+id
	m.ConversationType, m.ConversationID = conversationType, "oc_chat"
	m.Binding, m.AudienceID = "feishu-binding-"+conversationType, "feishu-audience-"+conversationType
	m.ReplyTarget = &ChannelReplyTarget{Kind: ReplyTargetFeishu, ID: m.ConversationID, MessageID: m.MessageID, UUID: "reply-" + id}
	return m
}

func TestFeishuReceiptDestinationAndAudienceFailClosed(t *testing.T) {
	s, _ := testStore(t)
	for name, change := range map[string]func(*ChannelMessage){
		"missing target":        func(m *ChannelMessage) { m.ReplyTarget = nil },
		"wrong kind":            func(m *ChannelMessage) { m.ReplyTarget.Kind = ReplyTargetFeishuMeeting },
		"wrong channel":         func(m *ChannelMessage) { m.Channel = "dingtalk" },
		"wrong chat":            func(m *ChannelMessage) { m.ReplyTarget.ID = "oc_other" },
		"wrong message":         func(m *ChannelMessage) { m.ReplyTarget.MessageID = "om_other" },
		"missing message":       func(m *ChannelMessage) { m.ReplyTarget.MessageID = "" },
		"non IM source":         func(m *ChannelMessage) { m.MessageID, m.ReplyTarget.MessageID = "event", "event" },
		"empty IM source":       func(m *ChannelMessage) { m.MessageID, m.ReplyTarget.MessageID = "om_", "om_" },
		"large UUID":            func(m *ChannelMessage) { m.ReplyTarget.UUID = strings.Repeat("x", 51) },
		"webhook expiry":        func(m *ChannelMessage) { m.ReplyExpiresAt = time.Now().Add(time.Hour) },
		"implicit conversation": func(m *ChannelMessage) { m.ConversationType = "" },
		"implicit audience":     func(m *ChannelMessage) { m.AudienceID = "" },
		"owner group":           func(m *ChannelMessage) { m.ConversationType, m.AudienceID = "2", OwnerAudienceID },
		"media":                 func(m *ChannelMessage) { m.MessageType = "picture" },
		"input material":        func(m *ChannelMessage) { m.Input = &ChannelInput{} },
	} {
		t.Run(name, func(t *testing.T) {
			m := feishuChannelInput(name, "1")
			change(&m)
			if inserted, err := s.EnqueueChannelMessage(testContext, m, ""); inserted || err == nil {
				t.Fatal("invalid IM capability was admitted", inserted, err)
			}
			if historyIdentityValid(m) {
				t.Fatal("invalid IM capability granted history access")
			}
		})
	}
	owner := feishuChannelInput("owner", "1")
	owner.AudienceID = OwnerAudienceID
	if inserted, err := s.EnqueueChannelMessage(testContext, owner, ""); err != nil || !inserted || !historyIdentityValid(owner) {
		t.Fatal("explicit private owner admission failed", inserted, err)
	}
	webhook := feishuChannelInput("webhook", "1")
	if _, err := s.EnqueueChannelMessage(testContext, webhook, testReplyURL); err == nil {
		t.Fatal("IM accepted a webhook capability")
	}
	meeting := meetingChannelInput("meeting-with-message")
	meeting.ReplyTarget.MessageID = "om_other"
	if _, err := s.EnqueueChannelMessage(testContext, meeting, ""); err == nil || historyIdentityValid(meeting) {
		t.Fatal("meeting accepted an IM reply target")
	}
}

func TestFeishuMixedDurableQueuePreservesTargetsAndNeverReplaysAmbiguousReply(t *testing.T) {
	s, _ := testStore(t)
	im := feishuChannelInput("same-source", "1")
	meeting := meetingChannelInput("meeting-source")
	// Provider namespaces remain distinct even when their opaque source IDs match.
	meeting.MessageID = im.MessageID
	meeting.ReceivedAt = im.ReceivedAt.Add(time.Millisecond)
	for _, m := range []ChannelMessage{im, meeting} {
		if inserted, err := s.EnqueueChannelMessage(testContext, m, ""); err != nil || !inserted {
			t.Fatal(inserted, err)
		}
	}
	claimed := claimChannel(t, s, im.Profile)
	if claimed.ID != im.ID {
		t.Fatal("mixed profile did not preserve FIFO")
	}
	if err := s.FinishChannelMessage(testContext, im.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareChannelReply(testContext, im.ID, "exact IM answer"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverChannels(testContext); err != nil {
		t.Fatal(err)
	}
	saved, err := s.ChannelMessage(testContext, im.ID)
	if err != nil || saved.ReplyStatus != "unknown" || saved.ReplyTarget == nil || *saved.ReplyTarget != *im.ReplyTarget || saved.ReplyText != "exact IM answer" {
		t.Fatal("recovery lost IM delivery identity", saved, err)
	}
	duplicate := feishuChannelInput("same-source", "1")
	duplicate.Profile, duplicate.Binding = "replacement-profile", "replacement-binding"
	if inserted, err := s.EnqueueChannelMessage(testContext, duplicate, ""); err != nil || inserted {
		t.Fatal("configuration change replayed IM receipt", inserted, err)
	}
	duplicate.ReplyTarget.UUID = "new-send"
	if inserted, err := s.EnqueueChannelMessage(testContext, duplicate, ""); err == nil || inserted {
		t.Fatal("duplicate replaced durable reply identity")
	}
	duplicate = feishuChannelInput("same-source", "1")
	duplicate.ConversationID, duplicate.ReplyTarget.ID = "oc_other", "oc_other"
	if inserted, err := s.EnqueueChannelMessage(testContext, duplicate, ""); err == nil || inserted {
		t.Fatal("source collision rerouted the reply")
	}
	if err := s.PrepareChannelReply(testContext, im.ID, "replayed answer"); err == nil {
		t.Fatal("ambiguous IM send was retried")
	}
	if next := claimChannel(t, s, im.Profile); next.ID != meeting.ID || next.Channel != ReplyTargetFeishuMeeting {
		t.Fatal("IM recovery hid the queued meeting", next)
	}
	if _, err := s.ClaimNextChannelMessage(testContext, im.Profile); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("ambiguous IM task was requeued", err)
	}
	for _, m := range []ChannelMessage{im, meeting} {
		if rows, err := s.ChannelReactions(testContext, m.ID); err != nil || len(rows) != 0 {
			t.Fatal("API text channel acquired reaction work", rows, err)
		}
		if url, err := s.ChannelReplyURL(testContext, m.ID); err != nil || url != "" {
			t.Fatal("API destination acquired a webhook", err)
		}
	}
}

func TestFeishuReactionPolicyChangesPreserveOriginalReceiptWithoutBackfill(t *testing.T) {
	for _, originallyDisabled := range []bool{true, false} {
		name := "enabled"
		if originallyDisabled {
			name = "disabled"
		}
		t.Run(name, func(t *testing.T) {
			s, _ := testStore(t)
			m := feishuChannelInput("policy", "1")
			m.DisableReactions = originallyDisabled
			if inserted, err := s.EnqueueChannelMessage(testContext, m, ""); err != nil || !inserted || !historyIdentityValid(m) {
				t.Fatal("valid IM policy rejected", inserted, err)
			}
			duplicate := feishuChannelInput("policy", "1")
			duplicate.DisableReactions = !originallyDisabled
			duplicate.Profile = "changed-profile"
			if inserted, err := s.EnqueueChannelMessage(testContext, duplicate, ""); err != nil || inserted {
				t.Fatal("policy upgrade broke receipt deduplication", inserted, err)
			}
			// Allowing policy changes must not allow a different delivery target.
			duplicate.ReplyTarget.UUID = "changed-delivery"
			if inserted, err := s.EnqueueChannelMessage(testContext, duplicate, ""); err == nil || inserted {
				t.Fatal("feedback upgrade replaced durable reply identity")
			}
			claimChannel(t, s, m.Profile)
			reactionReply(t, s, m, "completed", "accepted")
			if err := s.RecoverChannels(testContext); err != nil {
				t.Fatal(err)
			}
			saved, err := s.ChannelMessage(testContext, m.ID)
			wantCount := 3
			if originallyDisabled {
				wantCount = 0
			}
			if err != nil || saved.DisableReactions != originallyDisabled || saved.Profile != m.Profile || len(saved.Reactions) != wantCount || !historyIdentityValid(saved) {
				t.Fatal("redelivery mutated policy or backfilled feedback", saved, err)
			}
		})
	}
}
