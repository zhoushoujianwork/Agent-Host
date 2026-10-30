package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"agent-host/internal/harness"
)

func TestCloseoutEvidenceValidation(t *testing.T) {
	for _, tc := range []struct{ name, kind, outcome, reason, quote, status, runID, want string }{
		{"answer", "query", "resolved", "Delivered answer: Paris is the capital.", "Paris is the capital.", "completed", "r", "resolved"},
		{"partial", "ops", "partial", "Checked: Paris is the capital.", "Paris is the capital.", "completed", "r", "partial"},
		{"missing evidence", "ops", "resolved", "Done", "", "completed", "r", "unresolved"},
		{"invented evidence", "ops", "resolved", "Tests pass", "Tests pass", "completed", "r", "unresolved"},
		{"uncited reason", "ops", "resolved", "Done", "Paris", "completed", "r", "unresolved"},
		{"bad category", "Invalid Kind", "resolved", "Paris", "Paris", "completed", "r", "unresolved"},
		{"failed execution", "ops", "resolved", "Paris", "Paris", "failed", "r", "unresolved"},
		{"stale run", "ops", "resolved", "Paris", "Paris", "completed", "previous", "unresolved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]string{"run_id": tc.runID, "task_kind": tc.kind, "outcome": tc.outcome, "reason": tc.reason, "evidence_quote": tc.quote})
			answer, j := judgeCloseout("r", "claude", tc.status, "Paris is the capital.\n"+closeoutOpen+string(raw)+closeoutEnd)
			if answer != "Paris is the capital." || j.Outcome != tc.want || j.Reason == "" || j.Actor == "" {
				t.Fatalf("answer=%q judgment=%+v", answer, j)
			}
		})
	}
}

type closeoutRunner struct{ missing bool }

func (r closeoutRunner) Run(_ context.Context, req harness.Request, _ func(harness.Observation) error) (harness.Result, error) {
	// The native Agent sees the current run ID in trusted Host instructions.
	start := strings.Index(req.HostInstructions, "run_id=") + len("run_id=")
	tail := req.HostInstructions[start:]
	id := strings.SplitN(tail, ",", 2)[0]
	var runID string
	if err := json.Unmarshal([]byte(id), &runID); err != nil {
		return harness.Result{}, err
	}
	text := "The answer is 42."
	if !r.missing {
		raw, _ := json.Marshal(map[string]string{"run_id": runID, "task_kind": "query", "outcome": "resolved", "reason": "Delivered answer: The answer is 42.", "evidence_quote": text})
		text += "\n" + closeoutOpen + string(raw) + closeoutEnd
	}
	return harness.Result{Status: "completed", Text: text, Model: "fixture"}, nil
}
func TestRunPersistsAgentCloseoutWithoutHumanInput(t *testing.T) {
	for _, missing := range []bool{false, true} {
		h, _ := testHost(t)
		h.Runner = closeoutRunner{missing: missing}
		run, err := h.Run(context.Background(), newSession(t, h), "What is the answer?")
		if err != nil {
			t.Fatal(err)
		}
		saved, err := h.Store.Run(context.Background(), run.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := "resolved"
		if missing {
			want = "unresolved"
		}
		if saved.AcceptanceStatus != want || saved.Judgment == nil || saved.Judgment.JudgedAt.IsZero() || saved.Judgment.Reason == "" || saved.Output != "The answer is 42." {
			t.Fatalf("%+v", saved)
		}
		if !missing && (saved.TaskKind != "query" || !strings.Contains(saved.Judgment.Reason, saved.Output)) {
			t.Fatalf("%+v", saved)
		}
	}
}
