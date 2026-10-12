package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"agent-host/internal/decision/jev"
	"agent-host/internal/state"
)

type fakeEvaluator struct {
	calls     int
	responses []jev.Response
	err       error
	inspect   func(int, any, map[string]jev.Question)
}

func (f *fakeEvaluator) Provider() string { return "typesafe" }
func (f *fakeEvaluator) Endpoint() string { return jev.Endpoint }
func (f *fakeEvaluator) Model() string    { return "jev-latest" }
func (f *fakeEvaluator) Evaluate(_ context.Context, s any, q map[string]jev.Question) (jev.Response, error) {
	i := f.calls
	f.calls++
	if f.inspect != nil {
		f.inspect(i, s, q)
	}
	if i >= len(f.responses) {
		if f.err != nil {
			return jev.Response{}, f.err
		}
		return jev.Response{}, errors.New("unexpected evaluation")
	}
	return f.responses[i], nil
}

func decisionAnswer(choice string, prob float64) jev.Answer {
	return jev.Answer{Type: "choice", Choice: choice, Probabilities: map[string]float64{choice: prob}}
}
func classification(kind string, prob float64) jev.Response {
	eligibility := "remember"
	if kind == "none" {
		eligibility = "skip"
	}
	return jev.Response{Model: "resolved-classifier", Answers: map[string]jev.Answer{"memory_kind": decisionAnswer(kind, prob), "eligibility": decisionAnswer(eligibility, .99)}}
}
func relations(answers ...jev.Answer) jev.Response {
	m := map[string]jev.Answer{}
	for i, a := range answers {
		m[fmt.Sprintf("m%d", i)] = a
	}
	return jev.Response{Model: "resolved-relations", Answers: m}
}
func existing(id, text, status string) state.Memory {
	return state.Memory{ID: id, Scope: "/project", Text: text, Kind: "preference", Status: status, Revision: 1}
}

func TestExactUserStatementAndAudit(t *testing.T) {
	input := "我偏好中文回复。"
	f := &fakeEvaluator{responses: []jev.Response{classification("preference", .93)}, inspect: func(i int, s any, q map[string]jev.Question) {
		m := s.(map[string]any)
		if m["statement"] != input || m["source_role"] != "user" || len(m) != 2 {
			t.Fatalf("unexpected source evidence: %#v", m)
		}
		if len(q) != 2 || q["eligibility"].Type != "choice" || q["memory_kind"].Type != "choice" {
			t.Fatal("eligibility and category must be separate questions in one request")
		}
	}}
	d, err := (&Processor{client: f}).Decide(context.Background(), input, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Store || d.Kind != "preference" || d.Model != "resolved-classifier" {
		t.Fatalf("%+v", d)
	}
	var audit decisionAudit
	if err := json.Unmarshal(d.Details, &audit); err != nil {
		t.Fatal(err)
	}
	if audit.PolicyVersion != "user-statement-v4:attributed-actors" || audit.Provider != "typesafe" || audit.Endpoint != jev.Endpoint || audit.RequestedModel != "jev-latest" || audit.Threshold != .8 || audit.RelationThreshold != .8 || len(audit.Evaluations) != 1 {
		t.Fatalf("%+v", audit)
	}
}

func TestNonMemoryAndLowProbabilityAbstain(t *testing.T) {
	for _, tc := range []struct {
		name, input, kind string
		prob              float64
	}{
		{"assistant assertion", "助手说生产部署成功了。", "none", .99},
		{"question", "应该用什么数据库？", "none", .98},
		{"task", "帮我改用 Go。", "none", .96},
		{"hypothetical", "如果改用 Go 呢？", "none", .96},
		{"low probability", "项目的主色可能是蓝色。", "project_fact", .79},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := classification(tc.kind, tc.prob)
			if tc.kind != "none" {
				response.Answers["eligibility"] = decisionAnswer("remember", tc.prob)
			}
			f := &fakeEvaluator{responses: []jev.Response{response}}
			d, err := (&Processor{client: f}).Decide(context.Background(), tc.input, []state.Memory{existing("m1", "Old fact", "active")})
			if err != nil {
				t.Fatal(err)
			}
			if d.Store || d.Kind != "none" || f.calls != 1 {
				t.Fatalf("unsafe promotion: %+v calls=%d", d, f.calls)
			}
			// This verifies the application's handling of a typed decision; it is
			// not an accuracy claim about Jev on these example inputs.
		})
	}
}

func TestEligibilityIsSeparateFromCategoryProbability(t *testing.T) {
	cases := []struct {
		name, eligibility, kind                 string
		eligibilityProbability, kindProbability float64
		store                                   bool
		reason                                  string
	}{
		{"overlapping category labels", "remember", "constraint", .97, .52, true, "new_user_statement"},
		{"low eligibility cannot use confident category", "remember", "project_fact", .79, .99, false, "eligibility_abstained_low_probability"},
		{"contradictory remember and none", "remember", "none", .99, .99, false, "eligibility_kind_disagreement"},
		{"skip cannot use confident category", "skip", "constraint", .96, .99, false, "not_a_single_durable_user_statement"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := classification(tc.kind, tc.kindProbability)
			response.Answers["eligibility"] = decisionAnswer(tc.eligibility, tc.eligibilityProbability)
			f := &fakeEvaluator{responses: []jev.Response{response}}
			d, err := (&Processor{client: f}).Decide(context.Background(), "The project uses a fixed report format.", nil)
			if err != nil {
				t.Fatal(err)
			}
			if d.Store != tc.store || f.calls != 1 {
				t.Fatalf("%+v calls=%d", d, f.calls)
			}
			if tc.store && d.Kind != tc.kind {
				t.Fatalf("kind=%s", d.Kind)
			}
			if !tc.store && d.Kind != "none" {
				t.Fatalf("abstention retained an eligible kind: %s", d.Kind)
			}
			var audit decisionAudit
			if err := json.Unmarshal(d.Details, &audit); err != nil {
				t.Fatal(err)
			}
			if audit.Reason != tc.reason || len(audit.Evaluations[0].Response.Answers) != 2 {
				t.Fatalf("%+v", audit)
			}
		})
	}
}

func TestRelationsQuarantineWithoutOverwrite(t *testing.T) {
	cases := []struct {
		name                  string
		answers               []jev.Answer
		statuses              []string
		store                 bool
		duplicates, conflicts []string
	}{
		{"independent", []jev.Answer{decisionAnswer("independent", .99)}, []string{"active"}, true, nil, nil},
		{"duplicate", []jev.Answer{decisionAnswer("same", .91)}, []string{"active"}, false, []string{"id0"}, nil},
		{"conflict", []jev.Answer{decisionAnswer("conflict", .96)}, []string{"active"}, true, nil, []string{"id0"}},
		{"uncertain", []jev.Answer{decisionAnswer("uncertain", .99)}, []string{"active"}, true, nil, []string{"id0"}},
		{"low probability independent", []jev.Answer{decisionAnswer("independent", .79)}, []string{"active"}, true, nil, []string{"id0"}},
		{"quarantined duplicate", []jev.Answer{decisionAnswer("same", .99)}, []string{"conflicted"}, true, nil, []string{"id0"}},
		{"duplicate cannot hide conflict", []jev.Answer{decisionAnswer("same", .99), decisionAnswer("conflict", .95)}, []string{"active", "active"}, true, []string{"id0"}, []string{"id1", "id0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			memories := []state.Memory{}
			for i, status := range tc.statuses {
				memories = append(memories, existing(fmt.Sprintf("id%d", i), fmt.Sprintf("Existing statement %d", i), status))
			}
			f := &fakeEvaluator{responses: []jev.Response{classification("preference", .92), relations(tc.answers...)}, inspect: func(i int, s any, q map[string]jev.Question) {
				if i == 1 {
					if len(q) != len(memories) || len(s.(map[string]any)["existing"].(map[string]any)) != len(memories) {
						t.Fatal("comparison silently truncated records")
					}
				}
			}}
			d, err := (&Processor{client: f}).Decide(context.Background(), "I prefer Chinese responses", memories)
			if err != nil {
				t.Fatal(err)
			}
			if d.Store != tc.store || !reflect.DeepEqual(d.DuplicateIDs, tc.duplicates) || !reflect.DeepEqual(d.ConflictIDs, tc.conflicts) {
				t.Fatalf("%+v", d)
			}
			if d.Model != "resolved-relations" {
				t.Fatal(d.Model)
			}
		})
	}
}

func TestFailuresDoNotBecomeNoMemory(t *testing.T) {
	for _, failRelations := range []bool{false, true} {
		f := &fakeEvaluator{err: errors.New("provider unavailable")}
		if failRelations {
			f.responses = []jev.Response{classification("project_fact", .99)}
		}
		d, err := (&Processor{client: f}).Decide(context.Background(), "This project uses Go", []state.Memory{existing("m", "Existing fact", "active")})
		if err == nil || d.Store {
			t.Fatalf("failure concealed: %+v %v", d, err)
		}
		if len(d.Details) == 0 {
			t.Fatal("missing failure audit")
		}
	}
	if _, err := New(nil).Decide(context.Background(), "input", nil); err == nil {
		t.Fatal("nil client accepted")
	}
}

func TestLimitsAreExplicitAndNeverTruncate(t *testing.T) {
	t.Run("long statement abstains", func(t *testing.T) {
		f := &fakeEvaluator{}
		d, err := (&Processor{client: f}).Decide(context.Background(), strings.Repeat("a", MaxStatementBytes+1), nil)
		if err != nil || d.Store || f.calls != 0 || !strings.Contains(string(d.Details), "4_KiB") {
			t.Fatalf("%+v %v", d, err)
		}
	})
	t.Run("too many memories errors", func(t *testing.T) {
		f := &fakeEvaluator{}
		memories := make([]state.Memory, MaxExisting+1)
		d, err := (&Processor{client: f}).Decide(context.Background(), "input", memories)
		if err == nil || d.Store || f.calls != 0 {
			t.Fatalf("%+v %v", d, err)
		}
	})
	t.Run("aggregate state errors", func(t *testing.T) {
		f := &fakeEvaluator{}
		d, err := (&Processor{client: f}).Decide(context.Background(), "input", []state.Memory{existing("m", strings.Repeat("a", MaxDecisionStateBytes), "active")})
		if err == nil || d.Store || f.calls != 0 {
			t.Fatalf("%+v %v", d, err)
		}
	})
	t.Run("mixed scope errors", func(t *testing.T) {
		f := &fakeEvaluator{}
		a, b := existing("a", "a", "active"), existing("b", "b", "active")
		b.Scope = "other"
		if _, err := (&Processor{client: f}).Decide(context.Background(), "input", []state.Memory{a, b}); err == nil {
			t.Fatal("mixed scopes accepted")
		}
	})
	t.Run("duplicate ID errors", func(t *testing.T) {
		f := &fakeEvaluator{}
		a := existing("a", "a", "active")
		if _, err := (&Processor{client: f}).Decide(context.Background(), "input", []state.Memory{a, a}); err == nil {
			t.Fatal("duplicate IDs accepted")
		}
	})
	t.Run("canceled", func(t *testing.T) {
		f := &fakeEvaluator{}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := (&Processor{client: f}).Decide(ctx, "input", nil); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if f.calls != 0 {
			t.Fatal("called provider after cancellation")
		}
	})
}

func TestStatementComparedWithReviewedExperience(t *testing.T) {
	for _, relation := range []string{"same", "conflict", "uncertain"} {
		t.Run(relation, func(t *testing.T) {
			m := existing("procedure", "Use strings.Fields for whitespace normalization", "active")
			m.SourceType = "reviewed_experience"
			m.Conditions = "Go whitespace normalization"
			f := &fakeEvaluator{responses: []jev.Response{classification("constraint", .99), relations(decisionAnswer(relation, .99))}, inspect: func(i int, s any, _ map[string]jev.Question) {
				if i != 1 {
					return
				}
				record := s.(map[string]any)["existing"].(map[string]any)["m0"].(map[string]any)
				if record["source_type"] != m.SourceType || record["conditions"] != m.Conditions {
					t.Fatal("provenance omitted")
				}
			}}
			d, err := (&Processor{client: f}).Decide(context.Background(), "Whitespace normalization must preserve internal spaces", []state.Memory{m})
			if err != nil {
				t.Fatal(err)
			}
			if !d.Store || len(d.DuplicateIDs) != 0 {
				t.Fatalf("cross-source record merged: %+v", d)
			}
			if (relation != "same") != (len(d.ConflictIDs) == 1) {
				t.Fatalf("conflict missing: %+v", d)
			}
		})
	}
}
