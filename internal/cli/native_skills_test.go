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
