package host

import (
	"testing"

	"agent-host/internal/harness"
)

type identifiedRunner struct{ *testRunner }

func (identifiedRunner) Identity() harness.Identity {
	return harness.Identity{Agent: "local-claude-acp", Component: "harness.acp", Engine: "claude", Transport: "acp"}
}

func TestExecutionEvidenceUsesSelectedRunnerIdentity(t *testing.T) {
	h, runner := testHost(t)
	h.Runner = identifiedRunner{runner}
	h.Config.PreviousSessionID = "previous-owned-session"
	run := runOK(t, h, newSession(t, h), "hello")
	if run.Agent != "local-claude-acp" {
		t.Fatalf("run agent=%s", run.Agent)
	}
	seen := map[string]bool{}
	for _, e := range run.Events {
		if e.Type == "conversation.runtime_renewed" {
			if e.Data["previous_session_id"] != "previous-owned-session" || e.Data["native_history_restored"] != false {
				t.Fatalf("renewal evidence=%+v", e.Data)
			}
			seen[e.Type] = true
		}
		if e.Phase == "execute" && (e.Type == "phase.started" || e.Type == "phase.finished" || e.Type == "agent.started" || e.Type == "text.delta" || e.Type == "agent.result") {
			if e.Component != "harness.acp" {
				t.Fatalf("%s attributed to %s", e.Type, e.Component)
			}
			seen[e.Type] = true
		}
	}
	for _, kind := range []string{"phase.started", "phase.finished", "agent.started", "text.delta", "agent.result", "conversation.runtime_renewed"} {
		if !seen[kind] {
			t.Errorf("missing %s evidence", kind)
		}
	}
}
