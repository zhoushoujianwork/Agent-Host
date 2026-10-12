package host

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"agent-host/internal/event"
	"agent-host/internal/experience"
	"agent-host/internal/harness"
	"agent-host/internal/memory"
	"agent-host/internal/state"
)

const unresolvedChannelDecision = `{"kind":"default","reason":"Hand off without established project attribution.","details":{"policy":"channel-native-handoff-v1","original_decision":{"kind":"clarify","reason":"The engineering target is unresolved."}}}`

func attributedChannelRun(t *testing.T, h *Host, session state.Session, id, input, route string) (state.Run, error) {
	t.Helper()
	m := state.ChannelMessage{ID: id, Channel: "dingtalk", AccountID: "app", TenantID: "tenant", MessageID: "message-" + id, ConversationID: "group", SenderID: "member", Profile: "fixture", Binding: "conversation", Scope: h.Config.Scope, MessageType: "text", Text: input, ReplyExpiresAt: time.Now().Add(time.Hour)}
	if inserted, err := h.Store.EnqueueChannelMessage(t.Context(), m, "https://example.invalid/reply"); err != nil || !inserted {
		t.Fatal("enqueue", inserted, err)
	}
	if _, err := h.Store.ClaimNextChannelMessage(t.Context(), m.Profile); err != nil {
		t.Fatal(err)
	}
	if err := h.Store.SaveChannelRoutingDecision(t.Context(), m.ID, json.RawMessage(route)); err != nil {
		t.Fatal(err)
	}
	if err := h.Store.BindChannelSession(t.Context(), m.Binding, session.ID); err != nil {
		t.Fatal(err)
	}
	return h.RunChannel(t.Context(), session, input, m.ID)
}

func TestUnresolvedAttributionExecutesAndRetainsEvidenceWithoutLearning(t *testing.T) {
	h, runner, judge := experienceHost(t)
	h.Config.HostInstructions = ChannelInstructions("", true)
	h.Decider = deciderFunc(func(context.Context, string, []state.Memory) (memory.Decision, error) {
		t.Fatal("unresolved run reached the user-memory classifier")
		return memory.Decision{}, nil
	})
	h.Extractor = experienceExtractorFunc(func(context.Context, state.Run, []state.ToolEvidence, func(harness.Observation) error) (experience.Extraction, error) {
		t.Fatal("unresolved run reached the experience extractor")
		return experience.Extraction{}, nil
	})
	session := newSession(t, h)
	run, err := attributedChannelRun(t, h, session, "unresolved", "Investigate the supplied example without assuming a project.", unresolvedChannelDecision)
	if err != nil || run.ExecutionStatus != "completed" || run.Output == "" || run.MemoryAttribution != "unresolved" || run.MemoryStatus != "skipped" || run.ExperienceStatus != "skipped" || len(runner.requests) != 1 || judge.assessCalls != 0 {
		t.Fatal("unresolved execution or learning boundary failed", run, err)
	}
	evidence, err := h.Store.ToolEvidence(t.Context(), run.ID)
	if err != nil || len(evidence) != 2 || evidence[0].ResultSeq <= evidence[0].CallSeq {
		t.Fatal("unresolved execution lost correlated evidence", evidence, err)
	}
	if !strings.Contains(string(run.Decision), "unresolved_project_memory_attribution") || !strings.Contains(runner.requests[0].Prompt, "not attributed to one project") || !strings.Contains(runner.requests[0].HostInstructions, "default workspace") {
		t.Fatal("saved writeback reason or native handoff instructions absent", run.Decision, runner.requests[0])
	}
	if _, err := h.RetryMemory(t.Context(), run.ID); err == nil {
		t.Fatal("retry-memory bypassed unresolved attribution")
	}
	if _, err := h.RetryExperience(t.Context(), run.ID); err == nil {
		t.Fatal("retry-experience bypassed unresolved attribution")
	}
	if err := h.finalize(t.Context(), &journal{h: h, run: run}); err == nil {
		t.Fatal("direct host finalization bypassed unresolved attribution")
	}
	if err := h.finalizeExperience(t.Context(), &journal{h: h, run: run}); err == nil {
		t.Fatal("direct host extraction bypassed unresolved attribution")
	}
	memories, err := h.Store.Memories(t.Context(), run.Scope)
	if err != nil || len(memories) != 0 {
		t.Fatal("unresolved task polluted default memory", memories, err)
	}
	candidates, err := h.Store.Candidates(t.Context(), run.Scope, run.ID, "")
	if err != nil || len(candidates) != 0 {
		t.Fatal("unresolved task published candidates", candidates, err)
	}
	if _, err := h.Session(t.Context(), session.ID); err != nil {
		t.Fatal("native conversation cannot continue after handoff", err)
	}
	// A later independently established default task keeps ordinary learning.
	runner.tools = false
	decisions := 0
	h.Decider = deciderFunc(func(context.Context, string, []state.Memory) (memory.Decision, error) {
		decisions++
		return saveStatement(), nil
	})
	next, err := attributedChannelRun(t, h, session, "ordinary", "I prefer concise answers.", `{"kind":"default","details":{"policy":"channel-empty-catalog-v1"}}`)
	if err != nil || next.MemoryAttribution != "" || next.MemoryStatus != "saved" || decisions != 1 || len(runner.requests) != 2 || !runner.requests[1].Resume {
		t.Fatal("unresolved attribution leaked into ordinary default learning", next, err)
	}
}

func TestUnresolvedAttributionSurvivesTerminalOutputFailure(t *testing.T) {
	h, runner := testHost(t)
	want := errors.New("fixture output unavailable")
	h.Sink = func(e event.Event) error {
		if e.Type == "phase.finished" && e.Phase == "execute" {
			return want
		}
		return nil
	}
	h.Decider = deciderFunc(func(context.Context, string, []state.Memory) (memory.Decision, error) {
		t.Fatal("output failure enabled automatic learning")
		return memory.Decision{}, nil
	})
	session := newSession(t, h)
	run, err := attributedChannelRun(t, h, session, "output-failed", "Explain an unspecified engineering symptom.", unresolvedChannelDecision)
	if !errors.Is(err, want) || run.ExecutionStatus != "completed" || run.MemoryStatus != "skipped" || run.ExperienceStatus != "skipped" || len(runner.requests) != 1 || !strings.Contains(string(run.Decision), "unresolved_project_memory_attribution") {
		t.Fatal("output failure changed execution or writeback policy", run, err)
	}
	if _, err := h.Session(t.Context(), session.ID); err != nil {
		t.Fatal("output failure stranded compatible native history", err)
	}
}
