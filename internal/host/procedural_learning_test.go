package host

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"agent-host/internal/experience"
	"agent-host/internal/harness"
	"agent-host/internal/state"
)

type correctionRunner struct {
	requests []harness.Request
	teach    bool
}

func (r *correctionRunner) Run(_ context.Context, req harness.Request, emit func(harness.Observation) error) (harness.Result, error) {
	r.requests = append(r.requests, req)
	if r.teach {
		for _, tool := range []harness.ToolObservation{
			{ID: "wrong-profile", Name: "Bash", Stage: "call", Input: json.RawMessage(`{"command":"tccli cbs DescribeDisks --cli-profile fixture --region ap-shanghai --DiskIds '[\"disk-fixture\"]'"}`)},
			{ID: "wrong-profile", Name: "Bash", Stage: "result", IsError: true, Result: json.RawMessage(`"Exit code 255\nUnknown options: --cli-profile"`)},
			{ID: "correct-profile", Name: "Bash", Stage: "call", Input: json.RawMessage(`{"command":"tccli cbs DescribeDisks --profile fixture --region ap-shanghai --DiskIds '[\"disk-fixture\"]'"}`)},
			{ID: "correct-profile", Name: "Bash", Stage: "result", Result: json.RawMessage(`{"TotalCount":1,"DiskSet":[{"DiskId":"disk-fixture"}]}`)},
		} {
			if err := emit(harness.Observation{Type: "tool_activity", Tool: &tool}); err != nil {
				return harness.Result{}, err
			}
		}
	}
	return harness.Result{Status: "completed", Model: "fixture", Text: "The lookup returned one disk.", NativeSessionID: req.NativeSessionID}, nil
}

func narrowCorrectionDraft() state.ExperienceDraft {
	return state.ExperienceDraft{Title: "tccli cbs DescribeDisks profile syntax", Summary: "The observed lookup returned disk data using --profile; --cli-profile was rejected.", Conditions: "For tccli cbs DescribeDisks with an authorized configured profile in the demonstrated environment.", Steps: []string{"Use tccli cbs DescribeDisks --profile <profile> --region <region> --DiskIds '[\"<disk-id>\"]'.", "Inspect the returned DiskSet for the requested disk."}, Avoid: []string{"Do not use --cli-profile for this observed invocation; it reported Unknown options."}, EvidenceIDs: []string{"wrong-profile", "correct-profile"}}
}

type correctionExtractor struct {
	initial  state.ExperienceDraft
	refine   func(context.Context, state.Run, state.ExperienceDraft, []state.ToolEvidence) (experience.Extraction, error)
	refines  int
	multiple bool
}

func (x *correctionExtractor) Extract(_ context.Context, _ state.Run, _ []state.ToolEvidence, _ func(harness.Observation) error) (experience.Extraction, error) {
	drafts := []state.ExperienceDraft{x.initial}
	if x.multiple {
		drafts = append(drafts, x.initial)
	}
	return experience.Extraction{Drafts: drafts, Model: "initial-model", Audit: json.RawMessage(`{"reason":"extracted"}`)}, nil
}

func (x *correctionExtractor) Refine(ctx context.Context, run state.Run, draft state.ExperienceDraft, evidence []state.ToolEvidence) (experience.Extraction, error) {
	x.refines++
	if x.refine != nil {
		return x.refine(ctx, run, draft, evidence)
	}
	return experience.Extraction{Drafts: []state.ExperienceDraft{narrowCorrectionDraft()}, Model: "refined-model", Audit: json.RawMessage(`{"reason":"extracted"}`)}, nil
}

func correctionHost(t *testing.T) (*Host, *correctionRunner, *correctionExtractor, *experienceJudge) {
	t.Helper()
	h, _ := testHost(t)
	var err error
	h.Config.Scope, err = filepath.EvalSymlinks(h.Config.Scope)
	if err != nil {
		t.Fatal(err)
	}
	enableHostFiles(t, h)
	runner := &correctionRunner{teach: true}
	x := &correctionExtractor{initial: narrowCorrectionDraft()}
	x.initial.Steps = append([]string{"Discover all credentials and assume another profile can perform the operation."}, x.initial.Steps...)
	judge := &experienceJudge{assess: func(_ context.Context, draft state.ExperienceDraft, _ []state.ToolEvidence) (experience.Assessment, error) {
		if len(draft.Steps) > 2 {
			return experience.Assessment{Model: "fixture-judge", Audit: json.RawMessage(`{"reason":"assessment_abstained_low_probability"}`)}, nil
		}
		return experience.Assessment{Eligible: true, Kind: "procedure", Model: "fixture-judge", Audit: json.RawMessage(`{"reason":"eligible"}`)}, nil
	}}
	h.Runner, h.Extractor, h.Judge = runner, x, judge
	return h, runner, x, judge
}

func TestCorrectionRefinesOncePublishesAndReachesFreshSkill(t *testing.T) {
	h, runner, x, judge := correctionHost(t)
	first := runOK(t, h, newSession(t, h), "看下这个资源情况")
	candidate := onlyCandidate(t, h, first.ID)
	if x.refines != 1 || judge.assessCalls != 2 || candidate.Status != "accepted" || candidate.Model != "refined-model" {
		t.Fatal("narrowed correction was not reassessed and published", candidate, x.refines, judge.assessCalls)
	}
	runner.teach = false
	second := runOK(t, h, newSession(t, h), "tccli cbs DescribeDisks")
	if len(second.Context) != 1 || second.Context[0].ID != candidate.MemoryID || runner.requests[1].SkillPluginDir == "" {
		t.Fatal("fresh task did not receive the learned skill", second.Context)
	}
	h.Config.ProcedureRecallHints = []state.ProcedureRecallHint{{RunID: first.ID, MemoryID: candidate.MemoryID}}
	third := runOK(t, h, newSession(t, h), "回答了 操作吧")
	if len(third.Context) != 1 || third.Context[0].ID != candidate.MemoryID {
		t.Fatal("same-conversation procedure hint did not recall the correction", third.Context)
	}
	persisted, err := h.Store.Run(t.Context(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	var summary bool
	for _, e := range persisted.Events {
		if e.Type == "experience.learning.summary" && e.Data["refined_eligible"] == float64(1) {
			summary = true
		}
	}
	if !summary || len(runner.requests) != 3 {
		t.Fatal("learning receipt missing or business execution replayed")
	}
	if err := h.Store.InvalidateMemory(t.Context(), candidate.MemoryID, "Withdraw this correction"); err != nil {
		t.Fatal(err)
	}
	fourth := runOK(t, h, newSession(t, h), "操作吧")
	if len(fourth.Context) != 0 || runner.requests[3].SkillPluginDir != "" {
		t.Fatal("hint revived a disabled procedure")
	}
}

func TestCorrectionRefinementStopsAfterOneAttemptAndSurfacesFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "still-withheld", true: "provider-error"}[fail], func(t *testing.T) {
			h, runner, x, judge := correctionHost(t)
			x.multiple = true
			judge.assess = func(context.Context, state.ExperienceDraft, []state.ToolEvidence) (experience.Assessment, error) {
				return experience.Assessment{Audit: json.RawMessage(`{"reason":"not_supported"}`)}, nil
			}
			if fail {
				x.refine = func(context.Context, state.Run, state.ExperienceDraft, []state.ToolEvidence) (experience.Extraction, error) {
					return experience.Extraction{}, errors.New("refinement provider unavailable")
				}
			}
			r, err := h.Run(t.Context(), newSession(t, h), "Inspect a disk")
			if x.refines != 1 || len(runner.requests) != 1 || r.ExecutionStatus != "completed" {
				t.Fatal("unbounded refinement or business replay", x.refines, r)
			}
			if fail {
				if err == nil || !strings.Contains(r.ExperienceError, "provider unavailable") || r.ExperienceStatus != "failed" {
					t.Fatal("refinement error hidden", r, err)
				}
			} else if err != nil || r.ExperienceStatus != "empty" || judge.assessCalls != 3 {
				t.Fatal("withheld correction bypassed the evidence gate", r, err)
			}
		})
	}
}

func TestCorrectionRefinementRequiresAResultAfterTheObservedFailure(t *testing.T) {
	draft := narrowCorrectionDraft()
	evidence := []state.ToolEvidence{
		{ID: "correct-profile", Result: json.RawMessage(`"DiskSet"`), CallSeq: 1, ResultSeq: 2},
		{ID: "wrong-profile", IsError: true, Result: json.RawMessage(`"Unknown options"`), CallSeq: 3, ResultSeq: 4},
	}
	if _, correction := correctionEvidence(draft, evidence); correction {
		t.Fatal("an earlier success was treated as a correction of a later failure")
	}
}
