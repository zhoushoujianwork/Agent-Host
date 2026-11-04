package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-host/internal/assembly"
	"agent-host/internal/event"
	"agent-host/internal/harness"
)

func TestNativeSkillLoadsKeepExposureResultsAndCoverageDistinct(t *testing.T) {
	p, mounted := skillActivityFixture(t)
	var events []event.Event
	emit := func(e event.Event) error { events = append(events, e); return nil }
	a := &skillActivity{}
	if err := a.bindMount(p, &harness.SkillMount{Digest: p.Digest, Directory: mounted}); err != nil {
		t.Fatal(err)
	}
	read, _ := json.Marshal(map[string]any{"file_path": filepath.Join(mounted, p.Manifest.Files[0].Path), "offset": 2, "limit": 1})
	foreign, _ := json.Marshal(map[string]any{"file_path": filepath.Join(t.TempDir(), p.Manifest.Files[0].Path)})
	for _, tool := range []harness.ToolObservation{
		{ID: "skill", Name: "Skill", Stage: "call", Input: json.RawMessage(`{"skill":"agent-host:procedure-example"}`)},
		{ID: "skill", Name: "Skill", Stage: "result", IsError: true},
		{ID: "read", Name: "Read", Stage: "call", Input: read},
		{ID: "read", Name: "Read", Stage: "result", Result: json.RawMessage(`"partial native read"`)},
		{ID: "foreign", Name: "Read", Stage: "call", Input: foreign},
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
	if events[2].Data["source_root"] != p.Path || events[2].Data["mounted_root"] != mounted {
		t.Fatal("source and controlled mount attribution was lost", events[2].Data)
	}
}

func TestNativeSkillMountMetadataRejectsWrongDigestAndUnsafeRoots(t *testing.T) {
	p, mounted := skillActivityFixture(t)
	for _, tc := range []struct {
		name  string
		mount harness.SkillMount
	}{
		{"wrong-digest", harness.SkillMount{Digest: strings.Repeat("0", 64), Directory: mounted}},
		{"relative", harness.SkillMount{Digest: p.Digest, Directory: "relative/package"}},
		{"missing", harness.SkillMount{Digest: p.Digest, Directory: filepath.Join(t.TempDir(), "missing")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := (&skillActivity{}).bindMount(p, &tc.mount); err == nil {
				t.Fatal("unsafe mount metadata accepted")
			}
		})
	}
	if err := os.Chmod(mounted, 0755); err != nil {
		t.Fatal(err)
	}
	if err := (&skillActivity{}).bindMount(p, &harness.SkillMount{Digest: p.Digest, Directory: mounted}); err == nil {
		t.Fatal("nonprivate mount metadata accepted")
	}
}

func skillActivityFixture(t *testing.T) (assembly.SkillPackage, string) {
	t.Helper()
	manifest := harness.SkillManifest{Version: harness.ManagedSkillManifestVersion, SourceIdentity: "fixture", AgentID: "default", AudienceID: "owner", Scopes: []string{"fixture"}, Sources: []harness.SkillSource{{Name: "procedure-example", SkillID: "regional-delivery", Revision: 1}}, Files: []harness.SkillFile{{Path: "skills/procedure-example/references/us.md", SHA256: "reference-digest"}}}
	digest, err := manifest.ContentDigest()
	if err != nil {
		t.Fatal(err)
	}
	manifest.Digest = digest
	mounted, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(mounted, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mounted, "manifest.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return assembly.SkillPackage{Path: "/private/source-snapshot", Digest: digest, Manifest: manifest}, mounted
}
