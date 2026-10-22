package host

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"agent-host/internal/assembly"
	"agent-host/internal/event"
	"agent-host/internal/harness"
)

func TestNativeSkillLoadsKeepExposureResultsAndCoverageDistinct(t *testing.T) {
	p := assembly.SkillPackage{Path: "/private/snapshot", Digest: "snapshot-digest", Manifest: harness.SkillManifest{Sources: []harness.SkillSource{{Name: "procedure-example", SkillID: "regional-delivery"}}, Files: []harness.SkillFile{{Path: "skills/procedure-example/references/us.md", SHA256: "reference-digest"}}}}
	var events []event.Event
	emit := func(e event.Event) error { events = append(events, e); return nil }
	a := &skillActivity{}
	read, _ := json.Marshal(map[string]any{"file_path": filepath.Join(p.Path, p.Manifest.Files[0].Path), "offset": 2, "limit": 1})
	for _, tool := range []harness.ToolObservation{
		{ID: "skill", Name: "Skill", Stage: "call", Input: json.RawMessage(`{"skill":"agent-host:procedure-example"}`)},
		{ID: "skill", Name: "Skill", Stage: "result", IsError: true},
		{ID: "read", Name: "Read", Stage: "call", Input: read},
		{ID: "read", Name: "Read", Stage: "result", Result: json.RawMessage(`"partial native read"`)},
		{ID: "foreign", Name: "Read", Stage: "call", Input: json.RawMessage(`{"file_path":"/other/us.md"}`)},
	} {
		if err := a.observe(emit, p, &tool); err != nil {
			t.Fatal(err)
		}
	}
	if len(events) != 4 || events[0].Status != "requested" || events[1].Status != "failed" || events[2].Type != "skills.reference.observed" || events[3].Status != "completed" {
		t.Fatalf("incorrect evidence projection: %+v", events)
	}
	input := events[3].Data["input"].(map[string]any)
	if input["offset"] != float64(2) || input["limit"] != float64(1) {
		t.Fatal("partial read ranges lost")
	}
}
