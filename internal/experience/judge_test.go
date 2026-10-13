package experience

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"agent-host/internal/decision/jev"
	"agent-host/internal/state"
)

type fakeEvaluator struct {
	fn    func(context.Context, any, map[string]jev.Question) (jev.Response, error)
	calls int
}

func (f *fakeEvaluator) Evaluate(ctx context.Context, input any, questions map[string]jev.Question) (jev.Response, error) {
	f.calls++
	return f.fn(ctx, input, questions)
}
func (*fakeEvaluator) Provider() string { return "typesafe" }
func (*fakeEvaluator) Endpoint() string { return jev.Endpoint }
func (*fakeEvaluator) Model() string    { return "jev-latest" }

func answer(choice string, p float64) jev.Answer {
	return jev.Answer{Type: "choice", Choice: choice, Probabilities: map[string]float64{choice: p}}
}

func TestAssessmentPreservesEvidenceAndRejectsUnsupportedClaims(t *testing.T) {
	for _, mode := range []string{"eligible", "not-durable", "assistant-claim", "uncertain", "low-durability", "low-support"} {
		t.Run(mode, func(t *testing.T) {
			f := &fakeEvaluator{fn: func(_ context.Context, input any, questions map[string]jev.Question) (jev.Response, error) {
				b, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(b), "uncited-secret") || !strings.Contains(string(b), "tool-1") || len(questions) != 2 || questions["support"].Type != "choice" || !strings.Contains(questions["support"].Instructions.(string), "Input commands prove only what was requested") {
					t.Fatalf("bad support input=%s questions=%+v", b, questions)
				}
				r := jev.Response{Model: "resolved-jev", Answers: map[string]jev.Answer{"durable": answer("yes", .95), "support": answer("supported", .95)}}
				switch mode {
				case "not-durable":
					r.Answers["durable"] = answer("no", .95)
				case "assistant-claim":
					r.Answers["support"] = answer("unsupported", .95)
				case "uncertain":
					r.Answers["support"] = answer("uncertain", .95)
				case "low-durability":
					r.Answers["durable"] = answer("yes", .79)
				case "low-support":
					r.Answers["support"] = answer("supported", .79)
				}
				return r, nil
			}}
			evidence := fixtureEvidence()
			evidence = append(evidence, state.ToolEvidence{ID: "uncited", Name: "Bash", Input: json.RawMessage(`{"command":"echo uncited-secret"}`), Result: json.RawMessage(`"uncited-secret"`), CallSeq: 6, ResultSeq: 8})
			out, err := (&JevJudge{client: f}).Assess(context.Background(), fixtureDraft(), evidence)
			if err != nil || out.Eligible != (mode == "eligible") || out.Kind != "procedure" || out.Model != "resolved-jev" {
				t.Fatalf("out=%+v err=%v", out, err)
			}
			var audit decisionAudit
			if err := json.Unmarshal(out.Audit, &audit); err != nil {
				t.Fatal(err)
			}
			if audit.Provider != "typesafe" || audit.Endpoint != jev.Endpoint || audit.RequestedModel != "jev-latest" || len(audit.Evaluations) != 1 || audit.Evaluations[0].Response.Model != "resolved-jev" {
				t.Fatalf("missing auditable provider/request/response: %+v", audit)
			}
		})
	}
}

func TestAssessmentErrorAuditAndInvalidReferences(t *testing.T) {
	f := &fakeEvaluator{fn: func(context.Context, any, map[string]jev.Question) (jev.Response, error) {
		return jev.Response{}, errors.New("HTTP 503")
	}}
	j := &JevJudge{client: f}
	out, err := j.Assess(context.Background(), fixtureDraft(), fixtureEvidence())
	if err == nil || out.Eligible || !strings.Contains(string(out.Audit), "HTTP 503") || !strings.Contains(string(out.Audit), `"questions"`) {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	for _, ids := range [][]string{nil, {"absent"}, {"tool-1", "tool-1"}} {
		d := fixtureDraft()
		d.EvidenceIDs = ids
		if _, err := j.Assess(context.Background(), d, fixtureEvidence()); err == nil {
			t.Fatal("accepted invalid references")
		}
	}
	if f.calls != 1 {
		t.Fatalf("invalid references reached model: %d", f.calls)
	}
	evidence := fixtureEvidence()
	evidence[0].Result = json.RawMessage(`"` + strings.Repeat("x", MaxDecisionBytes) + `"`)
	if _, err := j.Assess(context.Background(), fixtureDraft(), evidence); err == nil || !strings.Contains(err.Error(), "no truncation") {
		t.Fatal(err)
	}
}

func fixtureCandidate() state.Candidate {
	return state.Candidate{ID: "candidate-1", RunID: "run-1", Scope: "/tmp/fixture", Draft: fixtureDraft(), Status: "pending"}
}
func fixtureMemory(id, source string) state.Memory {
	return state.Memory{ID: id, Scope: "/tmp/fixture", Kind: "procedure", Text: "Run the package tests after changing the fixture", SourceType: source, Status: "active", Revision: 1}
}

func TestQuarantinedUserStatementSameRelationRemainsVisibleToPublication(t *testing.T) {
	for _, relation := range []string{"same", "independent"} {
		t.Run(relation, func(t *testing.T) {
			m := fixtureMemory("disabled-assertion", "user_statement")
			m.Status, m.Kind = "conflicted", "constraint"
			f := &fakeEvaluator{fn: func(context.Context, any, map[string]jev.Question) (jev.Response, error) {
				return jev.Response{Model: "fixture-cross-source", Answers: map[string]jev.Answer{"m0": answer(relation, .99)}}, nil
			}}
			out, err := (&JevJudge{client: f}).Compare(t.Context(), fixtureCandidate(), []state.Memory{m})
			if err != nil {
				t.Fatal(err)
			}
			if relation == "same" && strings.Join(out.ConflictIDs, ",") != m.ID {
				t.Fatalf("equivalent user assertion escaped publication relation: %+v", out)
			}
			if relation == "independent" && len(out.ConflictIDs) != 0 {
				t.Fatalf("independent procedure was blocked: %+v", out)
			}
			if len(out.DuplicateIDs) != 0 || m.Status != "conflicted" || m.SourceType != "user_statement" {
				t.Fatalf("comparison relabeled or attached experience evidence to an assertion: %+v %+v", out, m)
			}
		})
	}
}

func TestComparisonHandlesRelationsWithoutRelabelingUserStatements(t *testing.T) {
	existing := []state.Memory{
		fixtureMemory("duplicate-experience", "reviewed_experience"),
		fixtureMemory("same-user-assertion", "user_statement"),
		fixtureMemory("contradiction", "reviewed_experience"),
		fixtureMemory("uncertain", "reviewed_experience"),
		fixtureMemory("weak-independent", "reviewed_experience"),
		fixtureMemory("independent", "reviewed_experience"),
		fixtureMemory("same-quarantined", "reviewed_experience"),
	}
	existing[6].Status = "conflicted"
	f := &fakeEvaluator{fn: func(_ context.Context, input any, questions map[string]jev.Question) (jev.Response, error) {
		if len(questions) != len(existing) {
			t.Fatal("not all memories compared")
		}
		for key, q := range questions {
			if !strings.Contains(q.Instructions.(string), "state.existing."+key+" ONLY") {
				t.Fatal("question missing exact target")
			}
		}
		return jev.Response{Model: "jev-resolved", Answers: map[string]jev.Answer{
			"m0": answer("same", .95), "m1": answer("same", .95), "m2": answer("conflict", .95), "m3": answer("uncertain", .95), "m4": answer("independent", .7), "m5": answer("independent", .95), "m6": answer("same", .95),
		}}, nil
	}}
	out, err := (&JevJudge{client: f}).Compare(context.Background(), fixtureCandidate(), existing)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(out.DuplicateIDs, ",") != "duplicate-experience" || strings.Join(out.ConflictIDs, ",") != "contradiction,uncertain,weak-independent,same-quarantined" {
		t.Fatalf("wrong relations: %+v", out)
	}
	if !strings.Contains(string(out.Audit), "quarantine_conflicting_or_uncertain_memory") {
		t.Fatal(string(out.Audit))
	}
}

func TestComparisonRejectsScopeExpiryAndIncompleteInputs(t *testing.T) {
	f := &fakeEvaluator{fn: func(context.Context, any, map[string]jev.Question) (jev.Response, error) {
		t.Fatal("invalid input reached model")
		return jev.Response{}, nil
	}}
	j := &JevJudge{client: f}
	for _, mode := range []string{"scope", "expired", "deleted", "missing-id", "duplicate-id", "bad-revision", "source", "count", "bytes"} {
		t.Run(mode, func(t *testing.T) {
			existing := []state.Memory{fixtureMemory("m1", "reviewed_experience")}
			switch mode {
			case "scope":
				existing[0].Scope = "/tmp/other"
			case "expired":
				expired := time.Now().Add(-time.Hour)
				existing[0].ExpiresAt = &expired
			case "deleted":
				existing[0].Status = "deleted"
			case "missing-id":
				existing[0].ID = ""
			case "duplicate-id":
				existing = append(existing, existing[0])
			case "bad-revision":
				existing[0].Revision = 0
			case "source":
				existing[0].SourceType = "assistant_claim"
			case "count":
				existing = make([]state.Memory, 101)
			case "bytes":
				existing[0].Text = strings.Repeat("x", MaxDecisionBytes)
			}
			out, err := j.Compare(context.Background(), fixtureCandidate(), existing)
			if err == nil || !json.Valid(out.Audit) {
				t.Fatalf("out=%+v err=%v", out, err)
			}
		})
	}
	out, err := j.Compare(context.Background(), fixtureCandidate(), nil)
	if err != nil || len(out.ConflictIDs) != 0 || len(out.DuplicateIDs) != 0 || f.calls != 0 {
		t.Fatalf("empty comparison=%+v err=%v", out, err)
	}
}

func TestComparisonProviderFailureIsNotIndependent(t *testing.T) {
	f := &fakeEvaluator{fn: func(context.Context, any, map[string]jev.Question) (jev.Response, error) {
		return jev.Response{}, errors.New("HTTP 503")
	}}
	out, err := (&JevJudge{client: f}).Compare(context.Background(), fixtureCandidate(), []state.Memory{fixtureMemory("m1", "reviewed_experience")})
	if err == nil || !strings.Contains(string(out.Audit), "comparison_failed") || !strings.Contains(string(out.Audit), `"questions"`) {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}
