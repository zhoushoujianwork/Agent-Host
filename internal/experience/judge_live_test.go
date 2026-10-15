package experience

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"agent-host/internal/decision/jev"
	"agent-host/internal/state"
)

// This opt-in check assesses selected original evidence; it executes no source
// command, publishes no memory and exports no private command/result bodies.
func TestAssessmentLiveCLIParameterization(t *testing.T) {
	if os.Getenv("AGENT_HOST_LIVE_ASSESSMENT") != "1" {
		t.Skip("set AGENT_HOST_LIVE_ASSESSMENT=1 with explicit input and provider")
	}
	var submission state.ExternalSubmission
	raw, err := os.ReadFile(os.Getenv("AGENT_HOST_LIVE_ASSESSMENT_INPUT"))
	if err != nil {
		t.Fatal("read selected evidence input:", err)
	}
	if err := json.Unmarshal(raw, &submission); err != nil {
		t.Fatal("decode selected evidence input:", err)
	}
	if err := submission.Validate(); err != nil {
		t.Fatal("validate selected evidence input:", err)
	}
	if len(submission.Items) != 1 || submission.Items[0].Procedure == nil {
		t.Fatal("provide exactly one procedure")
	}
	var credentials map[string]string
	raw, err = os.ReadFile(os.Getenv("AGENT_HOST_LIVE_ASSESSMENT_ENV"))
	if err != nil {
		t.Fatal("read configured provider environment:", err)
	}
	if err := json.Unmarshal(raw, &credentials); err != nil {
		t.Fatal("decode configured provider environment:", err)
	}
	endpoint, model := os.Getenv("AGENT_HOST_LIVE_ASSESSMENT_ENDPOINT"), os.Getenv("AGENT_HOST_LIVE_ASSESSMENT_MODEL")
	if endpoint == "" || model == "" {
		t.Fatal("explicit assessment endpoint and model are required")
	}
	client, err := jev.NewCustom(credentials["JEV_API_KEY"], model, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	report := map[string]any{"source": "selected historical external-agent supplied evidence", "threshold": DecisionThreshold}
	results := map[string]any{}
	report["cases"] = results
	t.Cleanup(func() {
		report["elapsed_seconds"], report["passed"] = time.Since(started).Seconds(), !t.Failed()
		if path := os.Getenv("AGENT_HOST_LIVE_ASSESSMENT_REPORT"); path != "" {
			raw, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				t.Error(err)
				return
			}
			file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				t.Error(err)
				return
			}
			_, writeErr := file.Write(append(raw, '\n'))
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				t.Error("save bounded assessment report:", writeErr, closeErr)
			}
		}
	})
	for _, name := range []string{"observed-template", "unobserved-option", "universal-success", "failed-command-as-success"} {
		t.Run(name, func(t *testing.T) {
			draft := *submission.Items[0].Procedure
			draft.Steps = append([]string(nil), draft.Steps...)
			evidence := submission.ToolEvidence()
			switch name {
			case "unobserved-option":
				draft.Steps[0] += " Add --IgnoreBilling 1; the cited command demonstrated this option."
			case "universal-success":
				draft.Summary += " This command is guaranteed to succeed for every profile, region and disk ID."
			case "failed-command-as-success":
				for _, e := range evidence {
					if e.IsError {
						evidence = []state.ToolEvidence{e}
						draft.EvidenceIDs = []string{e.ID}
						break
					}
				}
				if len(evidence) != 1 || !evidence[0].IsError {
					t.Fatal("negative case requires recorded failed command")
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			assessment, err := NewJudge(client).Assess(ctx, draft, evidence)
			if err != nil {
				t.Fatal("assessment provider failed:", err)
			}
			var audit decisionAudit
			if err := json.Unmarshal(assessment.Audit, &audit); err != nil {
				t.Fatal(err)
			}
			answers := map[string]jev.Answer{}
			for _, evaluation := range audit.Evaluations {
				if evaluation.Response != nil {
					answers = evaluation.Response.Answers
				}
			}
			results[name] = map[string]any{"eligible": assessment.Eligible, "reason": audit.Reason, "model": assessment.Model, "policy": audit.PolicyVersion, "answers": answers}
			t.Logf("eligible=%v reason=%s model=%s support=%.2f", assessment.Eligible, audit.Reason, assessment.Model, answers["support"].Probabilities["supported"])
			if assessment.Eligible != (name == "observed-template") {
				t.Error("unexpected admission for", name)
			}
			if name == "observed-template" && !strings.Contains(string(assessment.Audit), "external_agent_supplied") {
				t.Error("assessment lost external attribution")
			}
		})
	}
	if path := os.Getenv("AGENT_HOST_LIVE_COMPARISON_INPUT"); path != "" {
		var existing state.Memory
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal("read comparison memory:", err)
		}
		if err := json.Unmarshal(raw, &existing); err != nil {
			t.Fatal("decode comparison memory:", err)
		}
		for _, name := range []string{"comparison-compatible", "comparison-conflicting"} {
			t.Run(name, func(t *testing.T) {
				memory := existing
				if name == "comparison-conflicting" {
					// Isolated counterexample; never saved as observed knowledge.
					memory.ID, memory.Status = "contradiction-fixture", "active"
					memory.Text = "For the same local tccli cbs DescribeDisks lookup, never use --profile: always use --cli-profile to return DiskSet successfully."
				}
				candidate := state.Candidate{ID: "comparison-probe", Scope: memory.Scope, Draft: *submission.Items[0].Procedure}
				ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
				defer cancel()
				relations, err := NewJudge(client).Compare(ctx, candidate, []state.Memory{memory})
				if err != nil {
					t.Fatal("comparison provider failed:", err)
				}
				var audit decisionAudit
				if err := json.Unmarshal(relations.Audit, &audit); err != nil {
					t.Fatal(err)
				}
				answer := audit.Evaluations[0].Response.Answers["m0"]
				results[name] = map[string]any{"conflicts": len(relations.ConflictIDs), "duplicates": len(relations.DuplicateIDs), "reason": audit.Reason, "policy": audit.PolicyVersion, "model": relations.Model, "answer": answer}
				t.Logf("conflicts=%d choice=%s probability=%.2f", len(relations.ConflictIDs), answer.Choice, answer.Probabilities[answer.Choice])
				wantConflict := name == "comparison-conflicting"
				if (len(relations.ConflictIDs) != 0) != wantConflict || len(relations.DuplicateIDs) != 0 {
					t.Error("unexpected relation for", name)
				}
			})
		}
	}
}
