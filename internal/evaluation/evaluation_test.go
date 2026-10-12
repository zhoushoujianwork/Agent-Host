package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"agent-host/internal/experience"
	"agent-host/internal/memory"
	"agent-host/internal/state"
)

func boolValue(v bool) *bool { return &v }

func recallCase(id string) Case {
	return Case{ID: id, Category: "retrieval.lexical", Title: "中文召回", Rationale: "Same-scope literal matches are relevant.", Origin: Origin{Kind: "synthetic"}, Kind: "recall", Input: "支付超时", Scope: "project-a", Memories: []SeedMemory{}, Expected: Expected{RecallIDs: []string{}}}
}

func statementCase(id string) Case {
	return Case{ID: id, Category: "admission.preference", Title: "长期偏好", Rationale: "An explicit stable user preference is eligible.", Origin: Origin{Kind: "synthetic"}, Kind: "statement", Input: "我偏好中文回复。", Scope: "project-a", Expected: Expected{Store: boolValue(true), Kind: "preference", ConflictIDs: []string{}, DuplicateIDs: []string{}}}
}

func experienceCase(id string) Case {
	return Case{ID: id, Category: "evidence.support", Title: "保留测试来源", Rationale: "The bounded procedure attributes the result to the observed test command.", Origin: Origin{Kind: "synthetic"}, Kind: "experience", Scope: "project-a", Draft: &state.ExperienceDraft{Title: "运行现有测试", Summary: "工具报告现有测试通过。", Conditions: "仅用于当前测试套件。", Steps: []string{"运行 go test ./... 并记录报告结果。"}, EvidenceIDs: []string{"test"}}, Evidence: []state.ToolEvidence{{ID: "test", Name: "Bash", Input: json.RawMessage(`{"command":"go test ./..."}`), Result: json.RawMessage(`"ok example.test/parser"`), CallSeq: 1, ResultSeq: 2}}, Expected: Expected{Eligible: boolValue(true), Kind: "procedure"}}
}

func testSuite(cases ...Case) Suite {
	return Suite{Version: 1, ID: "test-suite", Description: "Runner behavior tests; fake providers do not measure model quality.", Cases: cases}
}

func regressionOptions() Options { return Options{Mode: "regression", Timeout: time.Second} }

func TestRecallUsesSQLiteScopeLifecycleAndPersistentContext(t *testing.T) {
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)
	c := recallCase("scoped")
	c.Memories = []SeedMemory{
		{ID: "payment", Scope: c.Scope, Kind: "project_fact", Text: "支付超时阈值为三秒。", Status: "active"},
		{ID: "foreign", Scope: "project-b", Kind: "project_fact", Text: "支付超时阈值为十秒。", Status: "active"},
		{ID: "conflicted", Scope: c.Scope, Kind: "project_fact", Text: "支付超时阈值待确认。", Status: "conflicted"},
		{ID: "invalidated", Scope: c.Scope, Kind: "project_fact", Text: "支付超时阈值原为五秒。", Status: "invalidated"},
		{ID: "preference", Scope: c.Scope, Kind: "preference", Text: "我偏好中文回复。", Status: "active"},
		{ID: "constraint", Scope: c.Scope, Kind: "constraint", Text: "禁止自动推送代码。", Status: "active"},
	}
	c.Expected.RecallIDs = []string{"payment", "preference", "constraint"}
	// A following case has the same workspace label but must receive a fresh DB.
	report, err := Run(t.Context(), testSuite(c, recallCase("fresh-case")), regressionOptions(), nil)
	if err != nil || !report.Success || report.Totals.Passed != 2 {
		t.Fatalf("regression report=%+v error=%v", report, err)
	}
	if !reflect.DeepEqual(report.Cases[0].Actual.MatchedIDs, []string{"payment"}) {
		t.Fatalf("FTS leaked an ineligible memory: %+v", report.Cases[0].Actual)
	}
	if len(report.Cases[1].Actual.RecallIDs) != 0 {
		t.Fatalf("fixture state leaked between cases: %+v", report.Cases[1].Actual)
	}
	entries, err := os.ReadDir(tempRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary evaluation databases were not removed: %v %v", entries, err)
	}
}

func TestQualityMismatchRemainsFailureAndDoesNotStopIndependentCases(t *testing.T) {
	c := recallCase("missing-and-unexpected")
	c.Memories = []SeedMemory{
		{ID: "lexical", Scope: c.Scope, Kind: "project_fact", Text: "支付超时阈值为三秒。", Status: "active"},
		{ID: "semantic", Scope: c.Scope, Kind: "project_fact", Text: "离线数据保存在磁盘。", Status: "active"},
	}
	c.Expected.RecallIDs = []string{"semantic"}
	report, err := Run(t.Context(), testSuite(c, recallCase("next")), regressionOptions(), nil)
	if err != nil || report.Success || report.Totals.Failed != 1 || report.Totals.Passed != 1 || report.Totals.Errors != 0 {
		t.Fatalf("quality mismatch was hidden or aborted the suite: %+v %v", report, err)
	}
	actual := report.Cases[0].Actual
	if !reflect.DeepEqual(actual.MissingIDs, []string{"semantic"}) || !reflect.DeepEqual(actual.UnexpectedIDs, []string{"lexical"}) {
		t.Fatalf("expected/actual difference was lost: %+v", actual)
	}
	if report.ByCategory[c.Category].Failed != 1 || report.ByCategory[c.Category].Passed != 1 {
		t.Fatalf("category accounting is incorrect: %+v", report.ByCategory)
	}
}

func TestFixtureIDsCannotCollideWithConflictSetupRuns(t *testing.T) {
	c := recallCase("fixture-identities")
	c.Memories = []SeedMemory{
		{ID: "a", Scope: c.Scope, Kind: "project_fact", Text: "支付超时为三秒。", Status: "conflicted"},
		{ID: "a-conflict", Scope: c.Scope, Kind: "project_fact", Text: "支付超时为五秒。", Status: "active"},
	}
	c.Expected.RecallIDs = []string{"a-conflict"}
	report, err := Run(t.Context(), testSuite(c), regressionOptions(), nil)
	if err != nil || !report.Success || !reflect.DeepEqual(report.Cases[0].Actual.RecallIDs, c.Expected.RecallIDs) {
		t.Fatalf("fixture labels collided with internal setup identities: %+v %v", report, err)
	}
}

type testDecider func(context.Context, string, []state.Memory) (memory.Decision, error)

func (f testDecider) Decide(ctx context.Context, input string, existing []state.Memory) (memory.Decision, error) {
	return f(ctx, input, existing)
}

type testJudge func(context.Context, state.ExperienceDraft, []state.ToolEvidence) (experience.Assessment, error)

func (f testJudge) Assess(ctx context.Context, draft state.ExperienceDraft, evidence []state.ToolEvidence) (experience.Assessment, error) {
	return f(ctx, draft, evidence)
}

func (testJudge) Compare(context.Context, state.Candidate, []state.Memory) (experience.Relations, error) {
	return experience.Relations{}, errors.New("evaluation must not publish or compare candidates")
}

func TestLiveRunnerComparesDecisionsAndPreservesEvidenceWithoutMeasuringModelQuality(t *testing.T) {
	c := statementCase("statement")
	c.Memories = []SeedMemory{
		{ID: "old", Scope: c.Scope, Kind: "preference", Text: "我偏好英文回复。", Status: "active"},
		{ID: "unsettled", Scope: c.Scope, Kind: "decision", Text: "旧方案尚有冲突。", Status: "conflicted"},
		{ID: "revoked", Scope: c.Scope, Kind: "decision", Text: "已作废方案。", Status: "invalidated"},
		{ID: "foreign", Scope: "project-b", Kind: "preference", Text: "其他项目偏好英文。", Status: "active"},
	}
	c.Expected.ConflictIDs = []string{"old"}
	e := experienceCase("experience")
	var decisions, assessments int
	options := Options{Mode: "live", Timeout: time.Second, Provider: "test-double", Model: "requested-model", Decider: testDecider(func(_ context.Context, input string, existing []state.Memory) (memory.Decision, error) {
		decisions++
		if input != c.Input || len(existing) != 2 || existing[0].ID != "old" || existing[1].ID != "unsettled" || existing[0].SourceType != "user_statement" {
			t.Fatalf("incorrect provider source context: %q %+v", input, existing)
		}
		return memory.Decision{Store: true, Kind: "preference", ConflictIDs: []string{"old"}, Model: "observed-statement-model", Details: json.RawMessage(`{"unit_test_only":true}`)}, nil
	}), Judge: testJudge(func(_ context.Context, draft state.ExperienceDraft, evidence []state.ToolEvidence) (experience.Assessment, error) {
		assessments++
		if !reflect.DeepEqual(draft, *e.Draft) || !reflect.DeepEqual(evidence, e.Evidence) {
			t.Fatal("provider did not receive exact experience source")
		}
		return experience.Assessment{Eligible: true, Kind: "procedure", Model: "observed-experience-model", Audit: json.RawMessage(`{"unit_test_only":true}`)}, nil
	})}
	report, err := Run(t.Context(), testSuite(recallCase("offline"), c, e), options, nil)
	if err != nil || !report.Success || decisions != 1 || assessments != 1 || report.Totals.NotRun != 1 || report.Totals.Passed != 2 {
		t.Fatalf("live comparison report=%+v decisions=%d assessments=%d error=%v", report, decisions, assessments, err)
	}
	if report.Cases[1].Model != "observed-statement-model" || report.Cases[2].Model != "observed-experience-model" || report.RequestedModel != "requested-model" || report.Provider != "test-double" {
		t.Fatalf("requested/observed model attribution lost: %+v", report)
	}
	if string(report.Cases[1].Actual.Decision.Details) != `{"unit_test_only":true}` || string(report.Cases[2].Actual.Assessment.Audit) != `{"unit_test_only":true}` {
		t.Fatal("decision audit evidence was not retained")
	}
	// A mismatched relation cannot be made successful by matching Store alone.
	options.CaseID = c.ID
	options.Decider = testDecider(func(context.Context, string, []state.Memory) (memory.Decision, error) {
		return memory.Decision{Store: true, Kind: "preference", DuplicateIDs: []string{"old"}}, nil
	})
	report, err = Run(t.Context(), testSuite(c, e), options, nil)
	if err != nil || report.Success || report.Totals.Failed != 1 || report.Cases[1].Status != "not_run" {
		t.Fatalf("relation mismatch or case filter was hidden: %+v %v", report, err)
	}
}

func TestFilteringAndEmptySelection(t *testing.T) {
	s := testSuite(recallCase("one"), statementCase("two"))
	for name, options := range map[string]Options{
		"missing category": {Mode: "regression", Category: "missing", Timeout: time.Second},
		"missing case":     {Mode: "regression", CaseID: "missing", Timeout: time.Second},
		"wrong mode case":  {Mode: "regression", CaseID: "two", Timeout: time.Second},
		"missing provider": {Mode: "live", Timeout: time.Second},
		"bad mode":         {Mode: "fake", Timeout: time.Second},
		"zero timeout":     {Mode: "regression"},
	} {
		t.Run(name, func(t *testing.T) {
			report, err := Run(t.Context(), s, options, nil)
			if err == nil || report.Success || report.Error == "" {
				t.Fatalf("invalid evaluation became success: %+v %v", report, err)
			}
		})
	}
	options := regressionOptions()
	options.Category = "retrieval.lexical"
	report, err := Run(t.Context(), s, options, nil)
	if err != nil || !report.Success || report.Totals.Selected != 1 || report.Totals.NotRun != 1 || report.Cases[1].Selected {
		t.Fatalf("category or mode did not exclude unselected cases: %+v %v", report, err)
	}
}

func TestOperationalErrorStopsProviderCallsAndPreservesFailure(t *testing.T) {
	providerError := errors.New("provider request unavailable")
	calls := 0
	options := Options{Mode: "live", Timeout: time.Second, Decider: testDecider(func(context.Context, string, []state.Memory) (memory.Decision, error) {
		calls++
		return memory.Decision{Model: "attempted-model"}, providerError
	})}
	var emitted []string
	report, err := Run(t.Context(), testSuite(statementCase("first"), statementCase("second")), options, func(r CaseResult) error {
		emitted = append(emitted, r.ID+":"+r.Status)
		return nil
	})
	if !errors.Is(err, providerError) || report.Success || calls != 1 || report.Totals.Errors != 1 || report.Totals.NotRun != 1 || report.Cases[1].Status != "not_run" {
		t.Fatalf("failed provider was retried or hidden: %+v calls=%d err=%v", report, calls, err)
	}
	if !strings.Contains(report.Cases[0].Reason, providerError.Error()) || report.Error == "" {
		t.Fatalf("failure context missing: %+v", report)
	}
	if !reflect.DeepEqual(emitted, []string{"first:running", "first:error", "second:not_run"}) {
		t.Fatalf("operational failure did not reach the live event stream: %v", emitted)
	}
}

func TestEventFailurePropagatesBeforeAndAfterProviderExecution(t *testing.T) {
	for _, failStatus := range []string{"running", "passed"} {
		t.Run(failStatus, func(t *testing.T) {
			outputError := errors.New("event writer closed")
			calls := 0
			options := Options{Mode: "live", Timeout: time.Second, Decider: testDecider(func(context.Context, string, []state.Memory) (memory.Decision, error) {
				calls++
				return memory.Decision{Store: true, Kind: "preference"}, nil
			})}
			report, err := Run(t.Context(), testSuite(statementCase("first"), statementCase("second")), options, func(result CaseResult) error {
				if result.Status == failStatus {
					return outputError
				}
				return nil
			})
			wantCalls := 0
			if failStatus == "passed" {
				wantCalls = 1
			}
			if !errors.Is(err, outputError) || report.Success || calls != wantCalls || report.Cases[1].Status != "not_run" {
				t.Fatalf("output failure did not stop work: %+v calls=%d error=%v", report, calls, err)
			}
		})
	}
}

func TestPerCaseDeadlineAndCancellationReachProvider(t *testing.T) {
	options := Options{Mode: "live", Timeout: 10 * time.Millisecond, Decider: testDecider(func(ctx context.Context, _ string, _ []state.Memory) (memory.Decision, error) {
		<-ctx.Done()
		return memory.Decision{}, ctx.Err()
	})}
	report, err := Run(t.Context(), testSuite(statementCase("deadline")), options, nil)
	if !errors.Is(err, context.DeadlineExceeded) || report.Success || report.Totals.Errors != 1 {
		t.Fatalf("per-case timeout did not propagate: %+v %v", report, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	report, err = Run(ctx, testSuite(statementCase("cancelled")), options, nil)
	if !errors.Is(err, context.Canceled) || report.Success || report.Totals.NotRun != 1 {
		t.Fatalf("cancelled evaluation ran provider work: %+v %v", report, err)
	}
}

func TestSuiteRejectsUnreviewableOrInconsistentLabels(t *testing.T) {
	base := recallCase("base")
	base.Memories = []SeedMemory{{ID: "m", Scope: base.Scope, Kind: "project_fact", Text: "支付超时为三秒。", Status: "active"}}
	base.Expected.RecallIDs = []string{"m"}
	for name, mutate := range map[string]func(*Suite){
		"wrong version":         func(s *Suite) { s.Version = 2 },
		"empty suite":           func(s *Suite) { s.Cases = nil },
		"duplicate case":        func(s *Suite) { s.Cases = append(s.Cases, s.Cases[0]) },
		"missing rationale":     func(s *Suite) { s.Cases[0].Rationale = " " },
		"unattributed incident": func(s *Suite) { s.Cases[0].Origin = Origin{Kind: "incident"} },
		"invented origin":       func(s *Suite) { s.Cases[0].Origin.Kind = "live" },
		"missing gold":          func(s *Suite) { s.Cases[0].Expected.RecallIDs = nil },
		"unknown gold ID":       func(s *Suite) { s.Cases[0].Expected.RecallIDs = []string{"missing"} },
		"duplicate gold ID":     func(s *Suite) { s.Cases[0].Expected.RecallIDs = []string{"m", "m"} },
		"cross-scope gold":      func(s *Suite) { s.Cases[0].Memories[0].Scope = "project-b" },
		"conflicted gold":       func(s *Suite) { s.Cases[0].Memories[0].Status = "conflicted" },
		"invalidated gold":      func(s *Suite) { s.Cases[0].Memories[0].Status = "invalidated" },
		"duplicate seed ID":     func(s *Suite) { s.Cases[0].Memories = append(s.Cases[0].Memories, s.Cases[0].Memories[0]) },
		"duplicate text": func(s *Suite) {
			m := s.Cases[0].Memories[0]
			m.ID = "other"
			s.Cases[0].Memories = append(s.Cases[0].Memories, m)
		},
		"mixed kinds":             func(s *Suite) { s.Cases[0].Expected.Store = boolValue(true) },
		"missing relation labels": func(s *Suite) { s.Cases[0] = statementCase("statement"); s.Cases[0].Expected.ConflictIDs = nil },
		"none stored":             func(s *Suite) { s.Cases[0] = statementCase("statement"); s.Cases[0].Expected.Kind = "none" },
		"uncited experience": func(s *Suite) {
			s.Cases[0] = experienceCase("experience")
			s.Cases[0].Draft.EvidenceIDs = []string{"absent"}
		},
		"missing tool result": func(s *Suite) { s.Cases[0] = experienceCase("experience"); s.Cases[0].Evidence[0].ResultSeq = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			// JSON copying prevents mutations from leaking across table cases.
			raw, err := json.Marshal(testSuite(base))
			if err != nil {
				t.Fatal(err)
			}
			var s Suite
			if err := json.Unmarshal(raw, &s); err != nil {
				t.Fatal(err)
			}
			mutate(&s)
			if err := Validate(s); err == nil {
				t.Fatal("invalid or unreviewable suite was accepted")
			}
		})
	}
	if err := Validate(testSuite(base, statementCase("statement"), experienceCase("experience"))); err != nil {
		t.Fatalf("valid dotted categories rejected: %v", err)
	}
}

func TestLoadStrictJSONAndContentFingerprint(t *testing.T) {
	raw, err := json.Marshal(testSuite(recallCase("empty-result")))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "suite.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	s, hash, err := Load(path)
	want := sha256.Sum256(raw)
	if err != nil || hash != hex.EncodeToString(want[:]) || len(s.Cases) != 1 {
		t.Fatalf("load fingerprint=%s cases=%d error=%v", hash, len(s.Cases), err)
	}
	for name, invalid := range map[string][]byte{
		"unknown field":          append([]byte(`{"unknown":true,`), raw[1:]...),
		"extra object":           append(append([]byte{}, raw...), []byte(` {}`)...),
		"trailing text":          append(append([]byte{}, raw...), []byte(` invalid`)...),
		"oversize":               []byte(strings.Repeat(" ", maxSuiteBytes+1)),
		"duplicate top key":      []byte(strings.Replace(string(raw), `"version":1`, `"version":1,"version":1`, 1)),
		"duplicate escaped key":  []byte(strings.Replace(string(raw), `"version":1`, `"version":1,"\u0076ersion":1`, 1)),
		"duplicate case key":     []byte(strings.Replace(string(raw), `"id":"empty-result"`, `"id":"empty-result","id":"empty-result"`, 1)),
		"duplicate expected key": []byte(strings.Replace(string(raw), `"recall_ids":[]`, `"recall_ids":[],"recall_ids":[]`, 1)),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, invalid, 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := Load(path); err == nil {
				t.Fatal("invalid suite JSON accepted")
			}
		})
	}
}

func TestCheckedInCorpusHasValidReviewableLabels(t *testing.T) {
	// Structural validation does not assert current model quality or turn known
	// retrieval gaps into passing behavior.
	for _, name := range []string{"memory-v1.json", "memory-v2.json"} {
		if _, _, err := Load(filepath.Join("..", "..", "evals", name)); err != nil {
			t.Fatal(err)
		}
	}
}
