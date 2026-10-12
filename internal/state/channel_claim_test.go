package state

import (
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestChannelClaimSkipsActiveBindingsWithoutChangingFIFOOrProfile(t *testing.T) {
	s, _ := testStore(t)
	at := time.Now().UTC()
	for _, entry := range []struct{ id, binding, profile string }{
		{"a1", "a", "profile"}, {"a2", "a", "profile"}, {"inactive", "b", "other-profile"},
		{"b1", "b", "profile"}, {"a3", "a", "profile"}, {"c1", "c", "profile"},
	} {
		m := channelInput(entry.id)
		m.Binding, m.Profile, m.ReceivedAt = entry.binding, entry.profile, at
		m.AgentID, m.AudienceID = "default", entry.binding
		enqueueChannel(t, s, m)
	}
	if first := claimChannel(t, s, "profile"); first.ID != "a1" {
		t.Fatal("oldest receipt was not first", first)
	}
	busy := []ChannelMessage{{Binding: "a", AgentID: "default", AudienceID: "a"}}
	second, err := s.ClaimNextEligibleChannelMessage(testContext, "profile", busy)
	if err != nil || second.ID != "b1" {
		t.Fatal("busy binding blocked an independent conversation", second, err)
	}
	busy = append(busy, second)
	third, err := s.ClaimNextEligibleChannelMessage(testContext, "profile", busy)
	if err != nil || third.ID != "c1" {
		t.Fatal("multiple active bindings were not excluded", third, err)
	}
	if _, err := s.ClaimNextEligibleChannelMessage(testContext, "profile", busy); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("claimed message or busy conversation was claimed", err)
	}
	for _, id := range []string{"a2", "a3"} {
		m, err := s.ChannelMessage(testContext, id)
		if err != nil || m.Status != "queued" {
			t.Fatal("skipping changed the original receipt", m, err)
		}
		if got := claimChannel(t, s, "profile"); got.ID != id {
			t.Fatal("per-binding order changed after release", got)
		}
	}
	if got := claimChannel(t, s, "other-profile"); got.ID != "inactive" {
		t.Fatal("another configuration's queue was consumed", got)
	}
}

func TestChannelClaimSerializesSharedMemoryPartitionIncludingLegacyDefaults(t *testing.T) {
	s, _ := testStore(t)
	first := channelInput("legacy")
	enqueueChannel(t, s, first)
	second := channelInput("owner-explicit")
	second.Binding, second.ConversationID, second.AgentID, second.AudienceID = "second", "other-owner-dm", DefaultAgentID, OwnerAudienceID
	enqueueChannel(t, s, second)
	other := channelInput("group")
	other.Binding, other.AgentID, other.AudienceID = "third", DefaultAgentID, "group"
	enqueueChannel(t, s, other)
	first = claimChannel(t, s, "profile")
	claimed, err := s.ClaimNextEligibleChannelMessage(testContext, "profile", []ChannelMessage{first})
	if err != nil || claimed.ID != other.ID {
		t.Fatal("legacy owner partition did not serialize across bindings", claimed, err)
	}
	if _, err := s.ClaimNextEligibleChannelMessage(testContext, "profile", []ChannelMessage{{Binding: "unrelated", AgentID: DefaultAgentID, AudienceID: OwnerAudienceID}}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("explicit owner partition did not exclude queued owner work", err)
	}
	if m, err := s.ChannelMessage(testContext, second.ID); err != nil || m.Status != "queued" {
		t.Fatal("excluded partition was mutated", m, err)
	}
	if claimed = claimChannel(t, s, "profile"); claimed.ID != second.ID {
		t.Fatal("owner work did not become eligible after release", claimed)
	}
}
