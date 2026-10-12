package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"agent-host/internal/experience"
	"agent-host/internal/harness"
	"agent-host/internal/history"
	"agent-host/internal/memory"
	"agent-host/internal/state"
)

func historyHost(t *testing.T) (*Host, *testRunner) {
	t.Helper()
	home := t.TempDir()
	store, err := state.Open(home, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	runner := &testRunner{}
	h := &Host{Store: store, Runner: runner, Config: Config{
		Scope: t.TempDir(), Model: "fixture", DecisionProvider: "fixture",
		HistoryHome: home, HistoryExecutable: "/fixture/agent-host", HostInstructions: "Existing trusted role instructions.",
	}}
	h.Decider = deciderFunc(func(context.Context, string, []state.Memory) (memory.Decision, error) {
		return memory.Decision{Kind: "none", Model: "fixture", Details: json.RawMessage(`{"reason":"question"}`)}, nil
	})
	return h, runner
}

func historyChannelMessage(t *testing.T, h *Host, session state.Session, id, input, tenant string, channel ...string) state.ChannelMessage {
	t.Helper()
	m := state.ChannelMessage{ID: id, Channel: "dingtalk", AccountID: "app", TenantID: tenant,
		MessageID: "provider-" + id, ConversationID: "dm-owner", ConversationType: "1", SenderID: "owner",
		Profile: "fixture", Binding: "conversation", Scope: h.Config.Scope, MessageType: "text", Text: input,
		ReplyExpiresAt: time.Now().Add(time.Hour)}
	if len(channel) != 0 {
		m.Channel = channel[0]
	}
	if inserted, err := h.Store.EnqueueChannelMessage(t.Context(), m, "synthetic-private-reply"); err != nil || !inserted {
		t.Fatal("enqueue history fixture", inserted, err)
	}
	claimed, err := h.Store.ClaimNextChannelMessage(t.Context(), m.Profile)
	if err != nil || claimed.ID != id {
		t.Fatal("claim history fixture", claimed.ID, err)
	}
	if err := h.Store.BindChannelSession(t.Context(), m.Binding, session.ID); err != nil {
		t.Fatal(err)
	}
	return claimed
}

func TestChannelHistoryMountRefreshesReceiptWithoutResettingNativeContinuation(t *testing.T) {
	h, runner := historyHost(t)
	h.Config.MCPServers = []harness.MCPServer{{Name: "knowledge_docs", Command: "/fixture/knowledge"}}
	session := newSession(t, h)
	old := historyChannelMessage(t, h, session, "old", "Historical deployment discussion that must not be prefetched.", "tenant")
	first, err := h.RunChannel(t.Context(), session, old.Text, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Store.FinishChannelMessage(t.Context(), old.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err := h.Store.PrepareChannelReply(t.Context(), old.ID, "Historical formatted channel answer."); err != nil {
		t.Fatal(err)
	}
	if err := h.Store.FinishChannelReply(t.Context(), old.ID, "accepted", ""); err != nil {
		t.Fatal(err)
	}
	current := historyChannelMessage(t, h, session, "current", "Find our earlier discussion.", "tenant")
	second, err := h.RunChannel(t.Context(), session, current.Text, current.ID)
	if err != nil || len(runner.requests) != 2 {
		t.Fatal("second history turn", second, err)
	}
	for i, receipt := range []state.ChannelMessage{old, current} {
		req := runner.requests[i]
		if len(req.MCPServers) != 2 || req.MCPServers[0].Name != "knowledge_docs" {
			t.Fatalf("history displaced selected knowledge: %+v", req.MCPServers)
		}
		binding, err := history.Resolve(h.Config.HistoryHome, receipt.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := binding.Server(h.Config.HistoryExecutable)
		got := req.MCPServers[1]
		if got.Name != want.Name || got.Command != want.Command || strings.Join(got.Args, "\x00") != strings.Join(want.Args, "\x00") || !strings.Contains(strings.Join(got.Args, " "), "--message "+receipt.ID) {
			t.Fatalf("history mount kept a prior receipt: got=%+v want=%+v", got, want)
		}
		if !strings.Contains(req.HostInstructions, h.Config.HostInstructions) || !strings.Contains(req.HostInstructions, history.Instructions) {
			t.Fatal("history capability absent from trusted instructions")
		}
		if strings.Contains(req.Prompt, "Historical formatted channel answer.") || i == 1 && strings.Contains(req.Prompt, old.Text) {
			t.Fatal("history transcript was prefetched")
		}
	}
	if runner.requests[0].Resume || !runner.requests[1].Resume || runner.requests[0].NativeSessionID != runner.requests[1].NativeSessionID {
		t.Fatal("per-turn history binding reset compatible native continuation")
	}
	for _, run := range []state.Run{first, second} {
		stored, err := h.Store.Run(t.Context(), run.ID)
		if err != nil {
			t.Fatal(err)
		}
		assembled := 0
		for _, e := range stored.Events {
			if e.Type == "history.assembled" {
				assembled++
				if e.Data["binding_fingerprint"] == "" || e.Status != "completed" {
					t.Fatal("history assembly lacks its access receipt", e)
				}
			}
		}
		if assembled != 1 {
			t.Fatal("history assembly was not recorded once", assembled)
		}
	}
}

func TestHistoricalUnresolvedTaskHasHistoryWithoutReprocessingMemory(t *testing.T) {
	h, runner := historyHost(t)
	session := newSession(t, h)
	run, err := attributedChannelRun(t, h, session, "unresolved-history", "Investigate without an established project.", unresolvedChannelDecision)
	if err != nil || run.ExecutionStatus != "completed" || len(runner.requests) != 1 {
		t.Fatal("unresolved history blocked business execution", run, err)
	}
	req := runner.requests[0]
	if len(req.MCPServers) != 1 || !strings.Contains(req.HostInstructions, history.Instructions) || strings.Contains(req.HostInstructions, "Conversation history tools are unavailable") || run.MemoryStatus != "skipped" {
		t.Fatal("historical default history or original memory policy changed", req, run)
	}
}

func TestDefaultHandoffLearnsAndRecallsWithHistoryAcrossFreshSession(t *testing.T) {
	h, _ := historyHost(t)
	runner := &experienceRunner{tools: true}
	h.Runner = runner
	h.Config.HostInstructions = ChannelInstructions("", false)
	h.Decider = deciderFunc(func(context.Context, string, []state.Memory) (memory.Decision, error) {
		return saveStatement(), nil
	})
	h.Judge = &experienceJudge{}
	h.Extractor = experienceExtractorFunc(func(_ context.Context, r state.Run, evidence []state.ToolEvidence, _ func(harness.Observation) error) (experience.Extraction, error) {
		if r.MemoryAttribution != "" || len(evidence) != 2 {
			t.Fatal("default handoff did not reach extraction with tool evidence", r, evidence)
		}
		return experience.Extraction{Drafts: []state.ExperienceDraft{procedureDraft()}, Model: "fixture-extractor"}, nil
	})
	route := strings.Replace(unresolvedChannelDecision, "channel-native-handoff-v1", "channel-native-handoff-v2", 1)
	first, err := attributedChannelRun(t, h, newSession(t, h), "default-statement", "I prefer concise answers.", route)
	if err != nil || first.MemoryAttribution != "" || first.MemoryStatus != "saved" || first.ExperienceStatus != "review_pending" || len(runner.requests[0].MCPServers) != 1 {
		t.Fatal("default handoff lost history or normal learning", first, err)
	}
	if err := h.Store.FinishChannelMessage(t.Context(), "default-statement", "completed", ""); err != nil {
		t.Fatal(err)
	}
	runner.tools = false
	h.Decider = deciderFunc(func(context.Context, string, []state.Memory) (memory.Decision, error) {
		return memory.Decision{Kind: "none", Model: "fixture"}, nil
	})
	second, err := attributedChannelRun(t, h, newSession(t, h), "default-followup", "How do I prefer answers?", route)
	if err != nil || second.SessionID == first.SessionID || len(second.Context) != 1 || second.Context[0].SourceRunID != first.ID || len(runner.requests[1].MCPServers) != 1 || runner.requests[1].Resume {
		t.Fatal("fresh default session lost memory or history", second, err)
	}
	binding, err := h.Store.ConversationHistoryBinding(t.Context(), "default-followup")
	if err != nil {
		t.Fatal(err)
	}
	result, err := h.Store.SearchConversationHistory(t.Context(), binding, state.ConversationHistorySearch{Query: "concise", Limit: 5})
	if err != nil || len(result.Turns) != 1 || result.Turns[0].RunID != first.ID {
		t.Fatal("fresh default history did not retain the earlier turn", result, err)
	}
}

func TestHistoryUnsupportedSourceFailsBeforeNativeExecution(t *testing.T) {
	h, runner := historyHost(t)
	session := newSession(t, h)
	m := historyChannelMessage(t, h, session, "unsupported-source", "Read earlier messages.", "tenant", "unsupported-channel")
	run, err := h.RunChannel(t.Context(), session, m.Text, m.ID)
	if err == nil || !strings.Contains(err.Error(), "assemble conversation history") || len(runner.requests) != 0 || run.ExecutionStatus != "failed" {
		t.Fatal("invalid history identity reached native execution", run, len(runner.requests), err)
	}
}

func TestHistoryNotMountedForToolFreeOrLocalCLITurns(t *testing.T) {
	for _, local := range []bool{false, true} {
		t.Run(map[bool]string{false: "tool-free-channel", true: "local-cli"}[local], func(t *testing.T) {
			h, runner := historyHost(t)
			h.Config.ToolsDisabled = !local
			session := newSession(t, h)
			var err error
			if local {
				_, err = h.Run(t.Context(), session, "Ordinary CLI question.")
			} else {
				m := historyChannelMessage(t, h, session, "tool-free", "Ordinary channel question.", "tenant")
				_, err = h.RunChannel(t.Context(), session, m.Text, m.ID)
			}
			if err != nil || len(runner.requests) != 1 || len(runner.requests[0].MCPServers) != 0 || strings.Contains(runner.requests[0].HostInstructions, history.Instructions) {
				t.Fatal("history mounted outside enabled channel tool execution", err, runner.requests)
			}
		})
	}
}
