package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-host/internal/assembly"
	"agent-host/internal/businessruntime"
)

func TestBusinessRuntimeAgentPolicyIdentityAndSkillPreservation(t *testing.T) {
	base := nativeSkillsRootFixture(t, map[string]string{"selected": "selected", "ambient": "ambient"})
	source := filepath.Join(canonicalTemp(t), "credential")
	if err := os.WriteFile(source, []byte("synthetic-business-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	a := agentDefinition{ID: "ops", Name: "Operations", NativeSkills: []string{"selected"}}
	o := options{nativeProfile: base}
	plain, err := a.options(o, "owner", false, true)
	if err != nil {
		t.Fatal(err)
	}
	a.BusinessRuntime = businessruntime.Policy{}
	empty, err := a.options(o, "owner", false, true)
	if err != nil || plain.nativeProfile != empty.nativeProfile || plain.hostInstructions != empty.hostInstructions {
		t.Fatal("empty policy changed legacy identity", err)
	}
	a.BusinessRuntime = businessruntime.Policy{HomeFiles: []businessruntime.HomeFile{{Source: source, Target: ".tccli/test.credential"}}, BinDirectory: canonicalTemp(t)}
	bound, err := a.options(o, "owner", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if bound.nativeProfile == plain.nativeProfile || bound.nativeSkillsFingerprint != plain.nativeSkillsFingerprint {
		t.Fatal("binding failed to separate profile or changed Skills")
	}
	for _, name := range []string{"selected/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(bound.nativeProfile, "skills", name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(bound.nativeProfile, "skills", "ambient")); !os.IsNotExist(err) {
		t.Fatal("ambient Skill imported")
	}
	workspace := canonicalTemp(t)
	before, err := assembly.Assemble("", "", "scope", workspace, assemblyConfig(bound))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("rotated-synthetic-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	rotated, err := a.options(o, "owner", false, true)
	if err != nil {
		t.Fatal(err)
	}
	after, err := assembly.Assemble("", "", "scope", workspace, assemblyConfig(rotated))
	if err != nil || before.Fingerprint != after.Fingerprint {
		t.Fatal("secret rotation changed runtime identity", err)
	}
	if strings.Contains(bound.hostInstructions, "synthetic-business-secret") {
		t.Fatal("secret in instructions")
	}
	a.BusinessRuntime.HomeFiles[0].Target = ".tccli/other.credential"
	changed, err := a.options(o, "owner", false, true)
	if err != nil || changed.nativeProfile == bound.nativeProfile {
		t.Fatal("changed policy reused profile", err)
	}
	a.ID = "another"
	other, err := a.options(o, "owner", false, true)
	if err != nil || other.nativeProfile == changed.nativeProfile {
		t.Fatal("Agents shared profile", err)
	}
}
