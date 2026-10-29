package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"agent-host/internal/event"
	"agent-host/internal/experience"
	"agent-host/internal/harness"
	"agent-host/internal/memory"
	"agent-host/internal/state"
)

type experienceRunner struct {
	requests []harness.Request
	tools    bool
	unpaired bool
	status   string
}

func (r *experienceRunner) Run(_ context.Context, req harness.Request, emit func(harness.Observation) error) (harness.Result, error) {
	r.requests = append(r.requests, req)
	status := r.status
	if status == "" {
		status = "completed"
	}
	observations := []harness.Observation{{Type: "init", Model: "resolved-native", NativeSessionID: req.NativeSessionID}}
	if r.tools {
		observations = append(observations,
			harness.Observation{Type: "tool_start", Tool: &harness.ToolObservation{ID: "edit-1", Name: "Edit", Stage: "call", Input: json.RawMessage(`{"file_path":"fixture.go","old_string":"n + 1","new_string":"n - 1"}`)}},
			harness.Observation{Type: "tool_result", Tool: &harness.ToolObservation{ID: "edit-1", Name: "Edit", Stage: "result", Result: json.RawMessage(`"Updated fixture.go"`)}},
			harness.Observation{Type: "tool_start", Tool: &harness.ToolObservation{ID: "test-1", Name: "Bash", Stage: "call", Input: json.RawMessage(`{"command":"go test ./..."}`)}},
		)
		if !r.unpaired {
			observations = append(observations, harness.Observation{Type: "tool_result", Tool: &harness.ToolObservation{ID: "test-1", Name: "Bash", Stage: "result", Result: json.RawMessage(`{"stdout":"ok fixture 0.001s","exit_code":0}`)}})
		}
	}
	observations = append(observations, harness.Observation{Type: "text_delta", Delta: "The test command reported passing.", Model: "resolved-native"}, harness.Observation{Type: "result", Message: status, Model: "resolved-native"})
	for _, o := range observations {
		if err := emit(o); err != nil {
			return harness.Result{Status: "unknown", Error: err.Error()}, err
		}
	}
	return harness.Result{Status: status, Text: "The test command reported passing.", Model: "resolved-native", NativeSessionID: req.NativeSessionID}, nil
}

type experienceExtractorFunc func(context.Context, state.Run, []state.ToolEvidence, func(harness.Observation) error) (experience.Extraction, error)

func (f experienceExtractorFunc) Extract(ctx context.Context, r state.Run, e []state.ToolEvidence, emit func(harness.Observation) error) (experience.Extraction, error) {
	return f(ctx, r, e, emit)
}

type experienceJudge struct {
	assess       func(context.Context, state.ExperienceDraft, []state.ToolEvidence) (experience.Assessment, error)
	compare      func(context.Context, state.Candidate, []state.Memory) (experience.Relations, error)
	assessCalls  int
	compareCalls int
}

func (j *experienceJudge) Assess(ctx context.Context, d state.ExperienceDraft, e []state.ToolEvidence) (experience.Assessment, error) {
	j.assessCalls++
	if j.assess != nil {
		return j.assess(ctx, d, e)
	}
	return experience.Assessment{Eligible: true, Kind: "procedure", Model: "resolved-jev", Audit: json.RawMessage(`{"reason":"eligible_for_human_review"}`)}, nil
}
func (j *experienceJudge) Compare(ctx context.Context, c state.Candidate, m []state.Memory) (experience.Relations, error) {
	j.compareCalls++
	if j.compare != nil {
		return j.compare(ctx, c, m)
	}
	return experience.Relations{Model: "resolved-jev", Audit: json.RawMessage(`{"reason":"independent"}`)}, nil
}

func procedureDraft() state.ExperienceDraft {
	return state.ExperienceDraft{Title: "Correct the fixture decrement", Summary: "Changing the fixture increment to a decrement was followed by a passing Go test report.", Conditions: "For the fixture decrement function with the demonstrated off-by-two failure", Steps: []string{"Change n + 1 to n - 1 in fixture.go.", "Run go test ./... and inspect the reported result."}, EvidenceIDs: []string{"edit-1", "test-1"}}
}

func experienceHost(t *testing.T) (*Host, *experienceRunner, *experienceJudge) {
	t.Helper()
	h, _ := testHost(t)
	runner := &experienceRunner{tools: true}
	judge := &experienceJudge{}
	h.Runner = runner
	h.Judge = judge
	h.Extractor = experienceExtractorFunc(func(_ context.Context, r state.Run, e []state.ToolEvidence, emit func(harness.Observation) error) (experience.Extraction, error) {
		if r.ExecutionStatus != "completed" || r.Output == "" || len(e) != 2 || e[0].CallSeq >= e[0].ResultSeq || e[1].ResultSeq <= e[1].CallSeq {
			t.Fatalf("extraction did not receive saved paired execution evidence: run=%+v evidence=%+v", r, e)
		}
		persisted, err := h.Store.Run(context.Background(), r.ID)
		if err != nil || persisted.Output != r.Output {
			t.Fatalf("extraction ran before persistence: %v", err)
		}
		if err := emit(harness.Observation{Type: "text_delta", Delta: "Extracting a procedural lesson.", Model: "resolved-extractor", NativeSessionID: "independent-extraction"}); err != nil {
			return experience.Extraction{}, err
		}
		return experience.Extraction{Drafts: []state.ExperienceDraft{procedureDraft()}, Model: "resolved-extractor", Audit: json.RawMessage(`{"reason":"extracted"}`)}, nil
	})
	return h, runner, judge
}

func onlyCandidate(t *testing.T, h *Host, runID string) state.Candidate {
	t.Helper()
	cs, err := h.Store.Candidates(context.Background(), h.Config.Scope, runID, "all")
	if err != nil || len(cs) != 1 {
		t.Fatalf("candidates=%+v err=%v", cs, err)
	}
	return cs[0]
}

func TestRestructureExperienceReassessesOriginalEvidenceWithoutReplay(t *testing.T) {
	h, runner, judge := experienceHost(t)
	session, err := h.Session(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	run, err := h.Run(t.Context(), session, "Fix the fixture decrement")
	if err != nil {
		t.Fatal(err)
	}
	c := onlyCandidate(t, h, run.ID)
	m, err := h.AcceptExperience(t.Context(), c.ID, "adopted", nil)
	if err != nil {
		t.Fatal(err)
	}
	nativeCalls := len(runner.requests)
	draft := procedureDraft()
	draft.Steps = []string{"Apply the observed decrement correction.", "Run the demonstrated test command and inspect its result."}
	draft.Avoid = []string{"Do not treat the fixture test result as production verification."}
	h.Extractor = experienceExtractorFunc(func(_ context.Context, r state.Run, e []state.ToolEvidence, _ func(harness.Observation) error) (experience.Extraction, error) {
		if r.ID != run.ID || len(e) != 2 {
			t.Fatal("restructure changed evidence source", r.ID, len(e))
		}
		return experience.Extraction{Drafts: []state.ExperienceDraft{draft}, Model: "structure-model", Audit: json.RawMessage(`{"policy":"fixture"}`)}, nil
	})
	revised, err := h.RestructureExperience(t.Context(), m.ID, 1, "separate successful path and pitfalls")
	if err != nil || revised.Revision != 2 || revised.Status != "active" || len(revised.Avoid) != 1 {
		t.Fatal(revised, err)
	}
	if len(runner.requests) != nativeCalls || revised.SourceRunID != m.SourceRunID || revised.CandidateID != c.ID || !reflect.DeepEqual(revised.EvidenceIDs, m.EvidenceIDs) {
		t.Fatal("source replayed or provenance altered")
	}
	original, _ := h.Store.GetCandidate(t.Context(), c.ID)
	if !reflect.DeepEqual(original.Draft, c.Draft) {
		t.Fatal("original adopted draft was rewritten")
	}
	if _, err := h.RestructureExperience(t.Context(), m.ID, 1, "stale"); err == nil {
		t.Fatal("stale restructure accepted")
	}
	judge.assess = func(context.Context, state.ExperienceDraft, []state.ToolEvidence) (experience.Assessment, error) {
		return experience.Assessment{Eligible: false}, nil
	}
	if _, err := h.RestructureExperience(t.Context(), m.ID, 2, "unsupported"); err == nil {
		t.Fatal("ineligible restructure accepted")
	}
	current, _ := h.Store.Memory(t.Context(), m.ID)
	if current.Revision != 2 {
		t.Fatal("failed assessment changed memory")
	}
}

func TestExperienceEvidenceReviewAndFreshSessionRecall(t *testing.T) {
	h, runner, judge := experienceHost(t)
	var observed []event.Event
	h.Sink = func(e event.Event) error { observed = append(observed, e); return nil }
	h.Decider = deciderFunc(func(_ context.Context, _ string, existing []state.Memory) (memory.Decision, error) {
		for _, m := range existing {
			if m.SourceType != "reviewed_experience" || m.CandidateID == "" || m.SourceRunID == "" || !reflect.DeepEqual(m.EvidenceIDs, procedureDraft().EvidenceIDs) {
				t.Fatal("reviewed procedure lost provenance during comparison with user input")
			}
		}
		return memory.Decision{Kind: "none", Model: "resolved-jev"}, nil
	})
	firstSession := newSession(t, h)
	r := runOK(t, h, firstSession, "Fix the fixture decrement test")
	if r.ExperienceStatus != "review_pending" || r.MemoryStatus != "unchanged" {
		t.Fatalf("receipt=%+v", r)
	}
	c := onlyCandidate(t, h, r.ID)
	if c.Status != "pending" || c.Model != "resolved-extractor" || c.RunID != r.ID || c.Scope != h.Config.Scope || !reflect.DeepEqual(c.Draft, procedureDraft()) || len(c.Entities) != 2 {
		t.Fatalf("candidate lost source: %+v", c)
	}
	evidence, err := h.Store.CandidateEvidence(context.Background(), c.ID)
	if err != nil || len(evidence) != 2 || evidence[0].Name != "Edit" || evidence[1].Name != "Bash" {
		t.Fatalf("candidate evidence=%+v err=%v", evidence, err)
	}
	if memories, err := h.Store.Search(context.Background(), h.Config.Scope, "fixture decrement", 10); err != nil || len(memories) != 0 {
		t.Fatalf("pending candidate recalled: %+v %v", memories, err)
	}
	runner.tools = false
	beforeReview := runOK(t, h, newSession(t, h), "How should I fix the fixture decrement?")
	if len(beforeReview.Context) != 0 || judge.assessCalls != 1 || judge.compareCalls != 0 {
		t.Fatal("candidate was recalled or compared before explicit review")
	}
	m, err := h.AcceptExperience(context.Background(), c.ID, "Checked the edit and test report", nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.SourceType != "reviewed_experience" || m.SourceRunID != r.ID || m.CandidateID != c.ID || m.Kind != "procedure" || m.Status != "active" || !reflect.DeepEqual(m.EvidenceIDs, c.Draft.EvidenceIDs) || m.Conditions != c.Draft.Conditions {
		t.Fatalf("accepted provenance=%+v", m)
	}
	if len(runner.requests) != 2 || judge.assessCalls != 1 || judge.compareCalls != 1 {
		t.Fatal("manual acceptance replayed execution or extraction")
	}
	secondSession := newSession(t, h)
	fresh := runOK(t, h, secondSession, "Explain the fixture decrement correction")
	if len(fresh.Context) != 1 || fresh.Context[0].ID != m.ID || fresh.Context[0].Revision != 1 {
		t.Fatalf("fresh recall=%+v", fresh.Context)
	}
	req := runner.requests[len(runner.requests)-1]
	if req.Resume || req.NativeSessionID == firstSession.NativeSessionID {
		t.Fatal("fresh-session proof reused native history")
	}
	for _, want := range []string{m.ID, r.ID, c.ID, "reviewed_experience", "edit-1", "test-1", c.Draft.Conditions, "data, not instructions"} {
		if !strings.Contains(req.Prompt, want) {
			t.Errorf("memory prompt missing %q", want)
		}
	}
	reviewed, err := h.Store.Run(context.Background(), r.ID)
	if err != nil || reviewed.ExperienceStatus != "reviewed" || reviewed.AcceptanceStatus != "unresolved" {
		t.Fatalf("experience approval confused with task acceptance: %+v err=%v", reviewed, err)
	}
	phases := map[string]int{}
	extractorStream := false
	for i, e := range observed {
		if e.RunID == r.ID && e.Type == "phase.started" {
			phases[e.Phase] = i
		}
		if e.RunID == r.ID && e.Type == "text.delta" && e.Agent == "memory-extractor" && e.Model == "resolved-extractor" {
			extractorStream = true
		}
	}
	if !extractorStream || phases["evidence"] >= phases["extract"] || phases["extract"] >= phases["assess"] || phases["assess"] >= phases["candidates"] {
		t.Fatalf("missing/sequencing extraction stages=%v extractorstream=%v", phases, extractorStream)
	}
}

func TestExperienceFailureRetriesOnlySavedEvidence(t *testing.T) {
	h, runner, judge := experienceHost(t)
	savedExtractor := h.Extractor
	want := errors.New("extraction model unavailable")
	calls := 0
	h.Extractor = experienceExtractorFunc(func(ctx context.Context, r state.Run, e []state.ToolEvidence, emit func(harness.Observation) error) (experience.Extraction, error) {
		calls++
		if calls == 1 {
			return experience.Extraction{Audit: json.RawMessage(`{"reason":"native_extraction_failed"}`)}, want
		}
		return savedExtractor.Extract(ctx, r, e, emit)
	})
	r, err := h.Run(context.Background(), newSession(t, h), "Fix the fixture decrement test")
	if !errors.Is(err, want) || r.ExecutionStatus != "completed" || r.MemoryStatus != "unchanged" || r.ExperienceStatus != "failed" || r.ExperienceError == "" || judge.assessCalls != 0 {
		t.Fatalf("failed extraction=%+v err=%v", r, err)
	}
	before, err := h.Store.ToolEvidence(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := h.RetryExperience(context.Background(), r.ID)
	if err != nil || retried.ExperienceStatus != "review_pending" || retried.ExperienceError != "" || calls != 2 || len(runner.requests) != 1 || judge.assessCalls != 1 {
		t.Fatalf("retry replayed task: %+v calls=%d native=%d err=%v", retried, calls, len(runner.requests), err)
	}
	after, err := h.Store.ToolEvidence(context.Background(), r.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("extraction retry polluted original evidence: before=%+v after=%+v err=%v", before, after, err)
	}
	if _, err := h.RetryExperience(context.Background(), r.ID); err == nil || calls != 2 || len(runner.requests) != 1 {
		t.Fatal("successful extraction was replayed")
	}
	if onlyCandidate(t, h, r.ID).Status != "pending" {
		t.Fatal("retry auto-approved candidate")
	}
}

func TestExperienceRetryCannotBypassUserMemoryFailure(t *testing.T) {
	h, runner, judge := experienceHost(t)
	want := errors.New("user memory decision unavailable")
	h.Decider = deciderFunc(func(context.Context, string, []state.Memory) (memory.Decision, error) {
		return memory.Decision{}, want
	})
	r, err := h.Run(context.Background(), newSession(t, h), "Fix the fixture decrement test")
	if !errors.Is(err, want) || r.MemoryStatus != "failed" || r.ExperienceStatus != "pending" || judge.assessCalls != 0 {
		t.Fatalf("failed first finalization: %+v %v", r, err)
	}
	if _, err := h.RetryExperience(context.Background(), r.ID); err == nil || judge.assessCalls != 0 || len(runner.requests) != 1 {
		t.Fatal("experience retry bypassed incomplete user memory finalization")
	}
	h.Decider = deciderFunc(func(context.Context, string, []state.Memory) (memory.Decision, error) {
		return memory.Decision{Kind: "none", Model: "resolved-jev"}, nil
	})
	retried, err := h.RetryMemory(context.Background(), r.ID)
	if err != nil || retried.MemoryStatus != "unchanged" || retried.ExperienceStatus != "review_pending" || judge.assessCalls != 1 || len(runner.requests) != 1 {
		t.Fatalf("user memory retry did not complete dependent extraction once: %+v err=%v", retried, err)
	}
}

func TestRejectedAndInvalidatedExperienceAreNotRecalled(t *testing.T) {
	h, runner, judge := experienceHost(t)
	first := runOK(t, h, newSession(t, h), "Fix the fixture decrement test")
	rejected := onlyCandidate(t, h, first.ID)
	if err := h.Store.RejectCandidate(context.Background(), rejected.ID, "The scope is too broad"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.AcceptExperience(context.Background(), rejected.ID, "", nil); err == nil || judge.compareCalls != 0 {
		t.Fatal("rejected candidate accepted or sent for comparison")
	}
	second := runOK(t, h, newSession(t, h), "Fix the fixture decrement with narrower conditions")
	c := onlyCandidate(t, h, second.ID)
	m, err := h.AcceptExperience(context.Background(), c.ID, "Reviewed", nil)
	if err != nil {
		t.Fatal(err)
	}
	runner.tools = false
	readingSession := newSession(t, h)
	before := runOK(t, h, readingSession, "fixture decrement")
	if len(before.Context) != 1 {
		t.Fatal("accepted experience not recalled")
	}
	if err := h.Store.InvalidateMemory(context.Background(), m.ID, "The fixture implementation changed"); err != nil {
		t.Fatal(err)
	}
	resumed, err := h.Session(context.Background(), readingSession.ID)
	if err != nil {
		t.Fatal(err)
	}
	after := runOK(t, h, resumed, "fixture decrement")
	if len(after.Context) != 0 {
		t.Fatal("invalidated experience recalled")
	}
	request := runner.requests[len(runner.requests)-1]
	for _, want := range []string{m.ID, "revision 1", "revision 2", "status invalidated"} {
		if !strings.Contains(request.Prompt, want) {
			t.Errorf("missing resumed invalidation notice %q", want)
		}
	}
	fresh := runOK(t, h, newSession(t, h), "fixture decrement")
	if len(fresh.Context) != 0 {
		t.Fatal("fresh session recalled invalidated experience")
	}
	historical, err := h.Store.Run(context.Background(), before.ID)
	if err != nil || len(historical.Context) != 1 || historical.Context[0].Status != "active" || historical.Context[0].Revision != 1 {
		t.Fatalf("historical context modified: %+v %v", historical.Context, err)
	}
}

func TestExperienceUnsupportedOrAbsentEvidenceCannotBecomeMemory(t *testing.T) {
	for _, mode := range []string{"assistant-only", "unsupported", "unpaired", "unknown-reference", "failed-execution", "canceled-execution"} {
		t.Run(mode, func(t *testing.T) {
			h, runner, judge := experienceHost(t)
			extractCalls := 0
			extractor := h.Extractor
			h.Extractor = experienceExtractorFunc(func(ctx context.Context, r state.Run, e []state.ToolEvidence, emit func(harness.Observation) error) (experience.Extraction, error) {
				extractCalls++
				out, err := extractor.Extract(ctx, r, e, emit)
				if mode == "unknown-reference" {
					out.Drafts[0].EvidenceIDs = []string{"assistant-success"}
				}
				return out, err
			})
			switch mode {
			case "assistant-only":
				runner.tools = false
			case "unsupported":
				judge.assess = func(context.Context, state.ExperienceDraft, []state.ToolEvidence) (experience.Assessment, error) {
					return experience.Assessment{Eligible: false, Kind: "procedure", Model: "resolved-jev", Audit: json.RawMessage(`{"reason":"not_supported"}`)}, nil
				}
			case "unpaired":
				runner.unpaired = true
			case "failed-execution":
				runner.status = "failed"
			case "canceled-execution":
				runner.status = "canceled"
			}
			r, err := h.Run(context.Background(), newSession(t, h), "Fix the fixture decrement test")
			wantError := mode != "assistant-only" && mode != "unsupported"
			if (err != nil) != wantError {
				t.Fatalf("receipt=%+v err=%v", r, err)
			}
			cs, readErr := h.Store.Candidates(context.Background(), h.Config.Scope, r.ID, "all")
			if readErr != nil || len(cs) != 0 {
				t.Fatalf("unsupported candidate saved=%+v err=%v", cs, readErr)
			}
			memories, readErr := h.Store.Memories(context.Background(), h.Config.Scope)
			if readErr != nil || len(memories) != 0 {
				t.Fatalf("unsupported memory saved=%+v err=%v", memories, readErr)
			}
			if (mode == "assistant-only" || mode == "unpaired" || mode == "failed-execution" || mode == "canceled-execution") && (extractCalls != 0 || judge.assessCalls != 0) {
				t.Fatal("invalid execution evidence reached model")
			}
			if mode == "unsupported" && (!strings.Contains(string(r.ExperienceDecision), "not_supported") || r.ExperienceStatus != "empty") {
				t.Fatal("unsupported assessment was not retained")
			}
		})
	}
}

func TestExperienceReviewComparisonFailureAndScopeCannotPublish(t *testing.T) {
	h, runner, judge := experienceHost(t)
	r := runOK(t, h, newSession(t, h), "Fix the fixture decrement test")
	c := onlyCandidate(t, h, r.ID)
	want := errors.New("comparison provider unavailable")
	judge.compare = func(context.Context, state.Candidate, []state.Memory) (experience.Relations, error) {
		return experience.Relations{Audit: json.RawMessage(`{"reason":"comparison_failed"}`)}, want
	}
	if _, err := h.AcceptExperience(context.Background(), c.ID, "Reviewed", nil); !errors.Is(err, want) {
		t.Fatal(err)
	}
	if current := onlyCandidate(t, h, r.ID); current.Status != "pending" {
		t.Fatal("failed comparison consumed pending candidate")
	}
	originalScope := h.Config.Scope
	h.Config.Scope = t.TempDir()
	if _, err := h.AcceptExperience(context.Background(), c.ID, "Reviewed", nil); err == nil || judge.compareCalls != 1 {
		t.Fatal("out-of-scope review reached model")
	}
	if _, err := h.RetryExperience(context.Background(), r.ID); err == nil {
		t.Fatal("out-of-scope retry allowed")
	}
	h.Config.Scope = originalScope
	if len(runner.requests) != 1 {
		t.Fatal("review replayed native execution")
	}
	stored, err := h.Store.Run(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	failureRecorded := false
	for _, e := range stored.Events {
		if e.Type == "candidate.compared" && e.Status == "failed" {
			failureRecorded = true
		}
	}
	if !failureRecorded {
		t.Fatal("failed review comparison missing from event journal")
	}
}

func TestExperienceConflictCannotHideBehindAnotherDuplicate(t *testing.T) {
	h, _, judge := experienceHost(t)
	create := func(label string) state.Memory {
		extractor := h.Extractor
		h.Extractor = experienceExtractorFunc(func(ctx context.Context, r state.Run, e []state.ToolEvidence, emit func(harness.Observation) error) (experience.Extraction, error) {
			out, err := extractor.Extract(ctx, r, e, emit)
			out.Drafts[0].Title = label
			return out, err
		})
		r := runOK(t, h, newSession(t, h), "Fix the fixture decrement test")
		m, err := h.AcceptExperience(context.Background(), onlyCandidate(t, h, r.ID).ID, "Reviewed", nil)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	first := create("First fixture procedure")
	second := create("Second fixture procedure")
	extractor := h.Extractor
	h.Extractor = experienceExtractorFunc(func(ctx context.Context, r state.Run, e []state.ToolEvidence, emit func(harness.Observation) error) (experience.Extraction, error) {
		out, err := extractor.Extract(ctx, r, e, emit)
		out.Drafts[0].Title = "Third conflicting fixture procedure"
		return out, err
	})
	r := runOK(t, h, newSession(t, h), "Fix the fixture decrement test")
	judge.compare = func(_ context.Context, _ state.Candidate, existing []state.Memory) (experience.Relations, error) {
		if len(existing) != 2 {
			return experience.Relations{}, fmt.Errorf("expected both existing procedures, got %d", len(existing))
		}
		return experience.Relations{DuplicateIDs: []string{first.ID}, ConflictIDs: []string{second.ID}, Model: "resolved-jev", Audit: json.RawMessage(`{"reason":"mixed_relations"}`)}, nil
	}
	m, err := h.AcceptExperience(context.Background(), onlyCandidate(t, h, r.ID).ID, "Reviewed conflict", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{first.ID, second.ID, m.ID} {
		stored, err := h.Store.Memory(context.Background(), id)
		if err != nil || stored.Status != "conflicted" {
			t.Fatalf("conflict left active knowledge: %+v %v", stored, err)
		}
	}
}
