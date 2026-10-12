package host

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"agent-host/internal/event"
	"agent-host/internal/experience"
	"agent-host/internal/harness"
	"agent-host/internal/state"
)

func TestEmptyExperienceDraftsDoNotReportJevAssessment(t *testing.T) {
	h, _, judge := experienceHost(t)
	h.Extractor = experienceExtractorFunc(func(context.Context, state.Run, []state.ToolEvidence, func(harness.Observation) error) (experience.Extraction, error) {
		return experience.Extraction{Model: "fixture-extractor", Audit: json.RawMessage(`{"reason":"no_reusable_experience"}`)}, nil
	})
	var phases []string
	h.Sink = func(e event.Event) error {
		if e.Type == "phase.started" {
			phases = append(phases, e.Phase)
		}
		return nil
	}
	r := runOK(t, h, newSession(t, h), "Check the fixture")
	want := []string{"recall", "execute", "remember", "persist", "evidence", "extract", "candidates"}
	if !reflect.DeepEqual(phases, want) || judge.assessCalls != 0 || r.ExperienceStatus != "empty" {
		t.Fatalf("empty extraction claimed an assessment: phases=%v calls=%d status=%s", phases, judge.assessCalls, r.ExperienceStatus)
	}
}
