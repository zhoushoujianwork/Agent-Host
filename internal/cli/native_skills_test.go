package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAgentProfileInheritsSkillsEvenWhenSettingsAreUnchanged(t *testing.T) {
	base := t.TempDir()
	destination := filepath.Join(base, "instances", "assistant", "audiences", "owner")
	if err := prepareAgentProfile(base, destination); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(base, "skills", "example", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("example"), 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := prepareAgentProfile(base, destination); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := os.ReadFile(filepath.Join(destination, "skills", "example", "SKILL.md"))
	if err != nil || string(loaded) != "example" {
		t.Fatalf("inherited skill=%s err=%v", loaded, err)
	}
	if err := os.Rename(filepath.Join(base, "skills"), filepath.Join(base, "removed-skills")); err != nil {
		t.Fatal(err)
	}
	if err := prepareAgentProfile(base, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(destination, "skills")); !os.IsNotExist(err) {
		t.Fatal("removed base skills left a stale mount")
	}
}

func TestSkillInheritancePreservesLocalOverridesAndRejectsConflicts(t *testing.T) {
	base := t.TempDir()
	destination := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "skills"), 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(destination, "skills")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := inheritNativeSkills(base, destination); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("operator file"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := inheritNativeSkills(base, destination); err == nil {
		t.Fatal("conflicting file was accepted")
	}
	if body, _ := os.ReadFile(target); string(body) != "operator file" {
		t.Fatal("operator file was overwritten")
	}
}

func TestAgentExplicitNativeSkillsAreIsolatedAndChangesSelectNewProfile(t *testing.T) {
	base := t.TempDir()
	for _, name := range []string{"one", "two"} {
		dir := filepath.Join(base, "skills", name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	o := options{nativeProfile: base}
	a := agentDefinition{ID: "a", Name: "A", NativeSkills: []string{"one"}}
	first, err := a.options(o, "owner", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(first.nativeProfile, "skills", "one", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(first.nativeProfile, "skills", "two")); !os.IsNotExist(err) {
		t.Fatal("unselected skill mounted")
	}
	a.NativeSkills = []string{}
	empty, err := a.options(o, "owner", false, true)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(empty.nativeProfile, "skills"))
	if err != nil || len(entries) != 0 || first.nativeProfile == empty.nativeProfile {
		t.Fatal("empty selection reused capabilities", err)
	}
	if _, err := os.Stat(filepath.Join(first.nativeProfile, "skills", "one", "SKILL.md")); err != nil {
		t.Fatal("previous profile was mutated")
	}
	for _, names := range [][]string{{"../escape"}, {"missing"}, {"one", "one"}} {
		a.NativeSkills = names
		if _, err := a.options(o, "owner", false, false); err == nil {
			t.Fatal("invalid skill selection accepted", names)
		}
	}
	if nativeSkillSelectionID([]string{"one", "two"}) != nativeSkillSelectionID([]string{"two", "one"}) {
		t.Fatal("selection order changed profile")
	}
}
