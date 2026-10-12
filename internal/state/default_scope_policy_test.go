package state

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVersionTwoDefaultAuditsRejectChangedSelectionAndSource(t *testing.T) {
	for _, tc := range []struct {
		name, route string
	}{
		{"handoff selects a project", `{"kind":"default","project_ids":["project"],"details":{"policy":"channel-native-handoff-v2","original_decision":{"kind":"clarify"}}}`},
		{"handoff original selects a project", `{"kind":"default","details":{"policy":"channel-native-handoff-v2","original_decision":{"kind":"clarify","project_ids":["project"]}}}`},
		{"handoff adds a continuation", `{"kind":"default","continuation_run_id":"run-legacy-source","details":{"policy":"channel-native-handoff-v2","original_decision":{"kind":"clarify"}}}`},
		{"handoff loses original judgment", `{"kind":"default","details":{"policy":"channel-native-handoff-v2","original_decision":{"kind":"default"}}}`},
		{"continuation changes run reference", `{"kind":"default","continuation_run_id":"run-legacy-source","details":{"policy":"channel-default-continuation-v2","original_decision":{"kind":"default","continuation_run_id":"another-run"}}}`},
		{"continuation changes message reference", `{"kind":"default","continuation_message_id":"native-legacy-source","details":{"policy":"channel-default-continuation-v2","original_decision":{"kind":"default","continuation_message_id":"another-message"}}}`},
		{"continuation adds selected project", `{"kind":"default","project_ids":["project"],"continuation_run_id":"run-legacy-source","details":{"policy":"channel-default-continuation-v2","original_decision":{"kind":"default","continuation_run_id":"run-legacy-source"}}}`},
		{"continuation original selects project", `{"kind":"default","continuation_run_id":"run-legacy-source","details":{"policy":"channel-default-continuation-v2","original_decision":{"kind":"default","primary_id":"project","continuation_run_id":"run-legacy-source"}}}`},
		{"continuation loses source", `{"kind":"default","details":{"policy":"channel-default-continuation-v2","original_decision":{"kind":"default"}}}`},
		{"continuation original is not default", `{"kind":"default","continuation_run_id":"run-legacy-source","details":{"policy":"channel-default-continuation-v2","original_decision":{"kind":"clarify","continuation_run_id":"run-legacy-source"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := testStore(t)
			legacyDefaultPolicyTurn(t, s, channelInput("legacy-source"))
			m := channelInput("invalid-current")
			enqueueChannel(t, s, m)
			m = claimChannel(t, s, m.Profile)
			raw := json.RawMessage(tc.route)
			if _, err := channelMemoryAttribution(raw); err == nil {
				t.Fatal("changed version 2 audit was accepted")
			}
			if err := s.SaveChannelRoutingDecision(testContext, m.ID, raw); err != nil {
				t.Fatal(err)
			}
			run := channelRun(t, s, m)
			if err := s.CreateChannelRun(testContext, run, m.ID); err == nil {
				t.Fatal("changed version 2 audit created an execution")
			}
			saved, err := s.ChannelMessage(testContext, m.ID)
			if err != nil || saved.RunID != "" || saved.Status != "claimed" {
				t.Fatal("rejected audit partially committed its execution", saved, err)
			}
		})
	}
}

func legacyDefaultPolicyTurn(t *testing.T, s *Store, m ChannelMessage) ChannelMessage {
	t.Helper()
	partition := s.WithPartition(m.AgentID, m.AudienceID)
	m.Profile = m.ID
	m.Text = "Historical default handoff with a retained answer."
	enqueueChannel(t, partition, m)
	m = claimChannel(t, partition, m.Profile)
	if err := partition.SaveChannelRoutingDecision(testContext, m.ID, json.RawMessage(unresolvedRoutingFixture)); err != nil {
		t.Fatal(err)
	}
	run := channelRun(t, partition, m)
	if err := partition.CreateChannelRun(testContext, run, m.ID); err != nil {
		t.Fatal(err)
	}
	if err := partition.CompleteRun(testContext, run.ID, "completed", "Original native answer.", "fixture", ""); err != nil {
		t.Fatal(err)
	}
	if err := partition.FinishChannelMessage(testContext, m.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err := partition.PrepareChannelReply(testContext, m.ID, "Original saved reply."); err != nil {
		t.Fatal(err)
	}
	if err := partition.FinishChannelReply(testContext, m.ID, "accepted", ""); err != nil {
		t.Fatal(err)
	}
	saved, err := partition.ChannelMessage(testContext, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestHistoricalDefaultHistoryPreservesEvidenceWithinScopeAndAudience(t *testing.T) {
	for _, tc := range []struct {
		name        string
		sourceGroup bool
		change      func(*ChannelMessage)
		allowed     bool
	}{
		{name: "same owner default scope", allowed: true},
		{name: "changed owner default scope", change: func(m *ChannelMessage) { m.Scope = "/new-default-workspace" }},
		{name: "another audience", change: func(m *ChannelMessage) { m.AudienceID = "another-audience" }},
		{name: "another owner conversation", change: func(m *ChannelMessage) { m.ConversationID, m.Binding = "other-dm", "other-dm-binding" }},
		{name: "group cannot read owner history", change: func(m *ChannelMessage) {
			m.ConversationType, m.AudienceID, m.ConversationID, m.Binding = "2", "group-a", "group-a", "group-a-binding"
		}},
		{name: "same group default scope", sourceGroup: true, allowed: true},
		{name: "changed group default scope", sourceGroup: true, change: func(m *ChannelMessage) { m.Scope = "/new-default-workspace" }},
		{name: "another group", sourceGroup: true, change: func(m *ChannelMessage) {
			m.AudienceID, m.ConversationID, m.Binding = "group-b", "group-b", "group-b-binding"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := testStore(t)
			m := channelInput("legacy-history")
			m.AgentID, m.AudienceID, m.ConversationType = DefaultAgentID, OwnerAudienceID, "1"
			if tc.sourceGroup {
				m.ConversationType, m.AudienceID, m.ConversationID, m.Binding = "2", "group-a", "group-a", "group-a-binding"
			}
			prior := legacyDefaultPolicyTurn(t, s, m)
			current := m
			current.ID, current.MessageID = "current", "native-current"
			if tc.change != nil {
				tc.change(&current)
			}
			historyTask(t, s, current, nil)
			var originalReceipt, originalRun []byte
			if err := s.db.QueryRow("SELECT payload FROM channel_messages WHERE id=?", prior.ID).Scan(&originalReceipt); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow("SELECT payload FROM runs WHERE id=?", prior.RunID).Scan(&originalRun); err != nil {
				t.Fatal(err)
			}
			// Store uses one SQLite connection, so total_changes detects any reader
			// write, including a replay or memory finalization outside these rows.
			var changesBefore int64
			if err := s.db.QueryRow("SELECT total_changes()").Scan(&changesBefore); err != nil {
				t.Fatal(err)
			}
			binding := historyBinding(t, s, current.ID)
			found, err := s.SearchConversationHistory(testContext, binding, ConversationHistorySearch{Query: "retained answer", Limit: 5})
			if err != nil {
				t.Fatal(err)
			}
			read, readErr := s.ReadConversationHistory(testContext, binding, ConversationHistoryRead{Reference: "host-history:" + prior.ID, Limit: 200})
			if tc.allowed {
				if len(found.Turns) != 1 || found.Turns[0].ReceiptID != prior.ID || readErr != nil || len(read.Turns) != 1 || read.Turns[0].Reply.Text != "Original saved reply." {
					t.Fatal("same-scope historical handoff was lost or changed", found, read, readErr)
				}
			} else if len(found.Turns) != 0 || readErr == nil {
				t.Fatal("historical handoff escaped its saved scope or audience", found, read, readErr)
			}
			partition := s.WithPartition(prior.AgentID, prior.AudienceID)
			if _, err := partition.ConversationHistoryBinding(testContext, prior.ID); err != nil {
				t.Fatal("historical source lost its original readable default scope", err)
			}
			saved, err := partition.Run(testContext, prior.RunID)
			if err != nil || saved.MemoryAttribution != "unresolved" || saved.MemoryStatus != "skipped" || saved.ExperienceStatus != "skipped" || saved.Output != "Original native answer." || !strings.Contains(string(saved.Decision), "unresolved_project_memory_attribution") {
				t.Fatal("history retrieval changed old execution or learning evidence", saved, err)
			}
			var receiptAfter, runAfter []byte
			if err := s.db.QueryRow("SELECT payload FROM channel_messages WHERE id=?", prior.ID).Scan(&receiptAfter); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow("SELECT payload FROM runs WHERE id=?", prior.RunID).Scan(&runAfter); err != nil {
				t.Fatal(err)
			}
			var changesAfter int64
			if err := s.db.QueryRow("SELECT total_changes()").Scan(&changesAfter); err != nil {
				t.Fatal(err)
			}
			if string(originalReceipt) != string(receiptAfter) || string(originalRun) != string(runAfter) || changesBefore != changesAfter {
				t.Fatal("history reader wrote state or rewrote historical evidence")
			}
		})
	}
}
