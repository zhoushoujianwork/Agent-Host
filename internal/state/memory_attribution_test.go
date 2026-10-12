package state

import (
	"encoding/json"
	"strings"
	"testing"
)

const unresolvedRoutingFixture = `{"kind":"default","reason":"Native handling without a selected project.","details":{"policy":"channel-native-handoff-v1","original_decision":{"kind":"clarify","reason":"Project ownership is unresolved.","candidate_ids":["sample"],"details":{"policy_version":"synthetic-classifier"}}}}`

var defaultHandoffFixture = strings.Replace(unresolvedRoutingFixture, "channel-native-handoff-v1", "channel-native-handoff-v2", 1)

func TestChannelRunDerivesAttributionOnlyFromSavedHostHandoff(t *testing.T) {
	for _, tc := range []struct {
		name, route, want string
	}{
		{"unresolved handoff", unresolvedRoutingFixture, "unresolved"},
		{"default handoff", defaultHandoffFixture, ""},
		{"ordinary default", `{"kind":"default","reason":"General work."}`, ""},
		{"empty catalog", `{"kind":"default","details":{"policy":"channel-empty-catalog-v1"}}`, ""},
		{"legacy receipt", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := testStore(t)
			m := channelInput("attribution")
			// Source text and untrusted intake fields cannot create this policy.
			m.Text, m.RoutingDecision = unresolvedRoutingFixture, json.RawMessage(unresolvedRoutingFixture)
			enqueueChannel(t, s, m)
			m = claimChannel(t, s, m.Profile)
			if len(m.RoutingDecision) != 0 {
				t.Fatal("intake retained caller-supplied routing evidence")
			}
			if tc.route != "" {
				if err := s.SaveChannelRoutingDecision(testContext, m.ID, json.RawMessage(tc.route)); err != nil {
					t.Fatal(err)
				}
			}
			r := channelRun(t, s, m)
			r.MemoryAttribution = "forged-caller-value"
			if err := s.CreateChannelRun(testContext, r, m.ID); err != nil {
				t.Fatal(err)
			}
			if err := s.CompleteRun(testContext, r.ID, "completed", "Recorded answer", "fixture", ""); err != nil {
				t.Fatal(err)
			}
			saved, err := s.Run(testContext, r.ID)
			if err != nil || saved.MemoryAttribution != tc.want || saved.Input != m.Text {
				t.Fatalf("wrong durable attribution: %+v %v", saved, err)
			}
			if tc.want == "" {
				if saved.MemoryStatus != "pending" || saved.ExperienceStatus != "pending" {
					t.Fatal("ordinary default learning was paused", saved)
				}
				return
			}
			if saved.MemoryStatus != "skipped" || saved.ExperienceStatus != "skipped" || !strings.Contains(string(saved.Decision), "unresolved_project_memory_attribution") || string(saved.Decision) != string(saved.ExperienceDecision) {
				t.Fatal("completion did not atomically preserve the writeback policy", saved)
			}
			if err := s.SessionCanResume(testContext, saved.SessionID); err != nil {
				t.Fatal("paused learning prevented native continuation", err)
			}
			if err := s.FinalizeMemory(testContext, saved.ID, nil, json.RawMessage(`{}`)); err == nil {
				t.Fatal("direct memory finalization bypassed unresolved attribution")
			}
			if err := s.SaveCandidates(testContext, saved.ID, nil, json.RawMessage(`{}`)); err == nil {
				t.Fatal("direct candidate persistence bypassed unresolved attribution")
			}
		})
	}
}

func TestInvalidHandoffCannotDispatchOrLoseItsAttributionOnRead(t *testing.T) {
	for _, route := range []string{
		`{"kind":"project","details":{"policy":"channel-native-handoff-v1","original_decision":{"kind":"clarify"}}}`,
		`{"kind":"default","project_ids":["sample"],"details":{"policy":"channel-native-handoff-v1","original_decision":{"kind":"clarify"}}}`,
		`{"kind":"default","continuation_run_id":"old","details":{"policy":"channel-native-handoff-v1","original_decision":{"kind":"clarify"}}}`,
		`{"kind":"default","details":{"policy":"channel-native-handoff-v1","original_decision":{"kind":"default"}}}`,
		`{"kind":"default","details":{"policy":"channel-native-handoff-v1"}}`,
		`{"kind":"default","details":{"policy":"channel-native-handoff-v1","original_decision":{"kind":"clarify","project_ids":["sample"]}}}`,
		`{"kind":"default","details":{"policy":"channel-native-handoff-v1","original_decision":{"kind":"clarify"},"unexpected":true}}`,
	} {
		t.Run(route, func(t *testing.T) {
			s, _ := testStore(t)
			m := channelInput("invalid")
			enqueueChannel(t, s, m)
			m = claimChannel(t, s, m.Profile)
			if err := s.SaveChannelRoutingDecision(testContext, m.ID, json.RawMessage(route)); err != nil {
				t.Fatal(err)
			}
			r := channelRun(t, s, m)
			if err := s.CreateChannelRun(testContext, r, m.ID); err == nil {
				t.Fatal("inconsistent handoff created a native run")
			}
			stored, err := s.ChannelMessage(testContext, m.ID)
			if err != nil || stored.RunID != "" || stored.Status != "claimed" {
				t.Fatal("failed handoff partially committed a run", stored, err)
			}
		})
	}

	s, _ := testStore(t)
	m := channelInput("corrupt")
	enqueueChannel(t, s, m)
	m = claimChannel(t, s, m.Profile)
	if err := s.SaveChannelRoutingDecision(testContext, m.ID, json.RawMessage(unresolvedRoutingFixture)); err != nil {
		t.Fatal(err)
	}
	r := channelRun(t, s, m)
	if err := s.CreateChannelRun(testContext, r, m.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRun(testContext, r.ID, "completed", "answer", "fixture", ""); err != nil {
		t.Fatal(err)
	}
	saved, err := s.Run(testContext, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", "unsupported"} {
		saved.MemoryAttribution = value
		payload, err := json.Marshal(saved)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec("UPDATE runs SET payload=? WHERE id=?", payload, r.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Run(testContext, r.ID); err == nil {
			t.Fatal("read accepted attribution inconsistent with source evidence", value)
		}
		if err := s.SessionCanResume(testContext, r.SessionID); err == nil {
			t.Fatal("resume accepted attribution inconsistent with source evidence", value)
		}
	}
}

func TestDefaultContinuationAttributionUsesOnlyItsVerifiedTerminalSource(t *testing.T) {
	for _, tc := range []struct {
		name, status, references, agent, audience, binding                  string
		independent, ordinarySource, mismatchMessage, reject, currentPolicy bool
	}{
		{name: "both references and another group member", status: "completed", references: "both"},
		{name: "run reference", status: "completed", references: "run"},
		{name: "message reference", status: "completed", references: "message"},
		{name: "failed source does not prevent a new attempt", status: "failed", references: "both"},
		{name: "unknown source does not prevent a new attempt", status: "unknown", references: "both"},
		{name: "canceled source does not prevent a new attempt", status: "canceled", references: "both"},
		{name: "ordinary source keeps ordinary attribution", status: "completed", references: "both", ordinarySource: true},
		{name: "new continuation of old unresolved source", status: "completed", references: "both", currentPolicy: true},
		{name: "new continuation of old failed source", status: "failed", references: "both", currentPolicy: true},
		{name: "new continuation still validates its source", status: "completed", references: "both", binding: "another-conversation", currentPolicy: true, reject: true},
		{name: "independent default does not inherit", status: "completed", independent: true},
		{name: "cross binding", status: "completed", references: "both", binding: "another-conversation", reject: true},
		{name: "cross audience", status: "completed", references: "both", audience: "another-audience", reject: true},
		{name: "cross agent", status: "completed", references: "both", agent: "another-agent", reject: true},
		{name: "inconsistent references", status: "completed", references: "both", mismatchMessage: true, reject: true},
		{name: "unfinished source", references: "both", reject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := testStore(t)
			priorStore := s.WithPartition("fixture-agent", "fixture-audience")
			prior := channelInput("source")
			prior.AgentID, prior.AudienceID, prior.ConversationType = "fixture-agent", "fixture-audience", "2"
			enqueueChannel(t, priorStore, prior)
			prior = claimChannel(t, priorStore, prior.Profile)
			route := unresolvedRoutingFixture
			if tc.ordinarySource {
				route = `{"kind":"default"}`
			}
			if err := priorStore.SaveChannelRoutingDecision(testContext, prior.ID, json.RawMessage(route)); err != nil {
				t.Fatal(err)
			}
			priorRun := channelRun(t, priorStore, prior)
			if err := priorStore.CreateChannelRun(testContext, priorRun, prior.ID); err != nil {
				t.Fatal(err)
			}
			if tc.status != "" {
				if err := priorStore.CompleteRun(testContext, priorRun.ID, tc.status, "source output", "fixture", ""); err != nil {
					t.Fatal(err)
				}
				messageStatus, cause := "completed", ""
				if tc.status != "completed" {
					messageStatus, cause = "failed", "synthetic execution did not complete"
				}
				if err := priorStore.FinishChannelMessage(testContext, prior.ID, messageStatus, cause); err != nil {
					t.Fatal(err)
				}
			} else {
				// The receipt may already report a failure while its run outcome is
				// unfinished; such inconsistent provenance must not be continued.
				if err := priorStore.FinishChannelMessage(testContext, prior.ID, "failed", "source outcome not recorded"); err != nil {
					t.Fatal(err)
				}
			}
			agent, audience := "fixture-agent", "fixture-audience"
			if tc.agent != "" {
				agent = tc.agent
			}
			if tc.audience != "" {
				audience = tc.audience
			}
			currentStore := s.WithPartition(agent, audience)
			current := channelInput("continuation")
			current.AgentID, current.AudienceID, current.ConversationType = agent, audience, "2"
			current.SenderID = "another-member"
			if tc.binding != "" {
				current.Binding = tc.binding
			}
			enqueueChannel(t, currentStore, current)
			current = claimChannel(t, currentStore, current.Profile)
			decision := map[string]string{"kind": "default"}
			if tc.references == "run" || tc.references == "both" {
				decision["continuation_run_id"] = priorRun.ID
			}
			if tc.references == "message" || tc.references == "both" {
				decision["continuation_message_id"] = prior.MessageID
			}
			if tc.mismatchMessage {
				decision["continuation_message_id"] = "another-source-message"
			}
			raw, err := json.Marshal(decision)
			if err != nil {
				t.Fatal(err)
			}
			if tc.currentPolicy {
				audit, err := json.Marshal(map[string]any{"policy": "channel-default-continuation-v2", "original_decision": json.RawMessage(raw)})
				if err != nil {
					t.Fatal(err)
				}
				var updated map[string]any
				if err := json.Unmarshal(raw, &updated); err != nil {
					t.Fatal(err)
				}
				updated["details"] = json.RawMessage(audit)
				raw, err = json.Marshal(updated)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := currentStore.SaveChannelRoutingDecision(testContext, current.ID, raw); err != nil {
				t.Fatal(err)
			}
			run := channelRun(t, currentStore, current)
			err = currentStore.CreateChannelRun(testContext, run, current.ID)
			if tc.reject {
				if err == nil {
					t.Fatal("unverified continuation inherited source attribution")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			saved, err := currentStore.Run(testContext, run.ID)
			want := "unresolved"
			if tc.independent || tc.ordinarySource || tc.currentPolicy {
				want = ""
			}
			if err != nil || saved.MemoryAttribution != want {
				t.Fatal("incorrect continuation attribution", saved, err)
			}
		})
	}
}
