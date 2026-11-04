package host

import (
	"context"
	"encoding/json"
	"testing"

	"agent-host/internal/event"
	"agent-host/internal/harness"
)

type identifiedRunner struct{ *testRunner }

func (identifiedRunner) Identity() harness.Identity {
	return harness.Identity{Agent: "local-claude-acp", Component: "harness.acp", Engine: "claude", Transport: "acp"}
}

type packageEvidenceRunner struct{ request harness.Request }

func (r *packageEvidenceRunner) Run(_ context.Context, req harness.Request, _ func(harness.Observation) error) (harness.Result, error) {
	r.request = req
	return harness.Result{Status: "completed", Text: "answer", NativeSessionID: req.NativeSessionID,
		Evidence: &harness.ExecutionEvidence{RunID: "sdk-run", CallerRunID: req.CallerRunID, Status: "ok", HistoryStatus: "failed", HistoryError: "private-history-path", Raw: json.RawMessage(`{"private":"native evidence"}`)}}, nil
}

func TestPackageCallerCorrelationAndPrivateHistoryEvidence(t *testing.T) {
	h, _ := testHost(t)
	runner := &packageEvidenceRunner{}
	h.Runner = runner
	var public []event.Event
	h.Sink = func(e event.Event) error { public = append(public, e); return nil }
	run := runOK(t, h, newSession(t, h), "hello")
	if runner.request.CallerRunID != "agh:"+run.ID || run.ExecutionStatus != "completed" {
		t.Fatal("business attribution or execution lost")
	}
	private, history := false, false
	for _, e := range run.Events {
		if e.Type == "offload.result" && e.Data["history_error"] == "private-history-path" {
			private = true
		}
	}
	for _, e := range public {
		if e.Type == "offload.result" || e.Data["history_error"] != nil {
			t.Fatal("private execution evidence leaked")
		}
		if e.Type == "offload.history" && e.Status == "failed" {
			history = true
		}
	}
	if !private || !history {
		t.Fatal("independent history outcome missing")
	}
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
