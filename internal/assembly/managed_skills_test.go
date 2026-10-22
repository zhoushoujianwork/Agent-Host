package assembly

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-host/internal/harness"
	"agent-host/internal/managedskills"
	"agent-host/internal/state"
)

func TestManagedCatalogHasAllEntriesAndFullReferences(t *testing.T) {
	var entries []managedskills.Entry
	for i := 0; i < 20; i++ {
		e := managedskills.PluginUsage(fmt.Sprintf("fixture-%d", i), []string{"read_exact_ticket"}, "Read current comments and tasks with the original instance_id.", "opaque-config-digest")
		e.Files["references/china.md"] = "tencent-cn\n" + strings.Repeat("China environment details.\n", 1500)
		e.Files["references/us.md"] = "tencent-us\n" + strings.Repeat("US environment details.\n", 1500)
		e.Digest = e.ContentDigest()
		entries = append(entries, e)
	}
	identity := SkillIdentity{SourceIdentity: "test", AgentID: "default", AudienceID: "owner", Scopes: []string{"project-a"}}
	rejected := state.Memory{ID: "rejected", Status: "invalidated", Kind: "procedure", SourceType: "reviewed_experience", Steps: []string{"must never load"}}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := ManagedSkillPackage(root, "run-a", entries, []state.Memory{rejected}, identity, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Manifest.Sources) != 20 || p.Manifest.Version != harness.ManagedSkillManifestVersion {
		t.Fatalf("catalog truncated: %+v", p.Manifest)
	}
	for _, source := range p.Manifest.Sources {
		b, err := os.ReadFile(filepath.Join(p.Path, "skills", source.Name, "references", "us.md"))
		if err != nil || len(b) < 30000 || !strings.HasPrefix(string(b), "tencent-us") {
			t.Fatalf("reference truncated: %d, %v", len(b), err)
		}
	}
	context := NativeSkillContext(p.Manifest, nil)
	if strings.Count(context, "agent-host:procedure-") != 20 || !strings.Contains(context, "proactively select") {
		t.Fatal("incomplete selection guide")
	}
}

func TestExplicitlyAdoptedHelperRetainsContentAndMode(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "authored")
	if err = os.MkdirAll(filepath.Join(source, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: helper\ndescription: Reviewed diagnostic helper\n---\nUse scripts/check.sh only when execution is authorized.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "scripts/check.sh"), []byte("#!/bin/sh\nprintf 'diagnostic'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	e, err := managedskills.Import(source, managedskills.Entry{ID: "helper", Owner: "operator", Applicability: "authorized diagnostics", Agents: []string{"default"}, Audiences: []string{"owner"}, Scopes: []string{"project-a"}, Provenance: []string{"reviewed existing helper"}, Helpers: true})
	if err != nil {
		t.Fatal(err)
	}
	e.Revision = 1
	e.Digest = e.ContentDigest()
	p, err := ManagedSkillPackage(root, "helpers", []managedskills.Entry{e}, nil, SkillIdentity{SourceIdentity: "test", AgentID: "default", AudienceID: "owner", Scopes: []string{"project-a"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range p.Manifest.Files {
		if strings.HasSuffix(f.Path, "scripts/check.sh") {
			info, err := os.Stat(filepath.Join(p.Path, f.Path))
			if err != nil || !f.Executable || info.Mode().Perm() != 0700 {
				t.Fatal("helper mode not preserved", info, err)
			}
			return
		}
	}
	t.Fatal("helper missing")
}
