package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-host/internal/assembly"
)

func nativeSkillsRootFixture(t *testing.T, skills map[string]string) string {
	t.Helper()
	root := canonicalTemp(t)
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range skills {
		path := filepath.Join(root, "skills", name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

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
	if err := os.WriteFile(filepath.Join(base, "settings.json"), []byte(`{"legacy":"inherited"}`), 0600); err != nil {
		t.Fatal(err)
	}
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
	if first.nativeSkillsFingerprint != "" {
		t.Fatal("legacy native skill selection changed its runtime fingerprint inputs")
	}
	if settings, err := os.ReadFile(filepath.Join(first.nativeProfile, "settings.json")); err != nil || !strings.Contains(string(settings), "inherited") {
		t.Fatal("legacy Agent stopped inheriting native settings", string(settings), err)
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

func TestAgentNativeSkillsRootSelectsOnlyExplicitSource(t *testing.T) {
	base := nativeSkillsRootFixture(t, map[string]string{"shared": "legacy-base", "unselected": "legacy-extra"})
	if err := os.WriteFile(filepath.Join(base, "settings.json"), []byte(`{"env":{"AMBIENT":"value"},"hooks":{"PreToolUse":["ambient"]},"language":"ambient"}`), 0600); err != nil {
		t.Fatal(err)
	}
	source := nativeSkillsRootFixture(t, map[string]string{"other": "not-selected"})
	linkedSkill := nativeSkillsRootFixture(t, map[string]string{"shared": "operations-source"})
	if err := os.Symlink(filepath.Join(linkedSkill, "skills", "shared"), filepath.Join(source, "skills", "shared")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte(`{"source":"skills-root"}`), 0600); err != nil {
		t.Fatal(err)
	}
	a := agentDefinition{ID: "ops", Name: "Operations", NativeSkillsRoot: source, NativeSkills: []string{"shared"}}
	selected, err := a.options(options{nativeProfile: base}, "owner", false, true)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(selected.nativeProfile, "skills", "shared", "SKILL.md"))
	if err != nil || string(body) != "operations-source" {
		t.Fatal("explicit native skill source was not mounted", string(body), err)
	}
	if _, err := os.Stat(filepath.Join(selected.nativeProfile, "skills", "other")); !os.IsNotExist(err) {
		t.Fatal("unselected skill from explicit root was mounted")
	}
	settings, err := os.ReadFile(filepath.Join(selected.nativeProfile, "settings.json"))
	if err != nil || string(settings) != "{}\n" {
		t.Fatal("clean Agent inherited ambient native settings", string(settings), err)
	}
	if !strings.HasPrefix(selected.nativeProfile, filepath.Join(base, "instances", "ops")) || selected.nativeSkillsFingerprint == "" {
		t.Fatal("explicit source changed Agent profile identity or lacked a fingerprint", selected.nativeProfile)
	}
	workspace := canonicalTemp(t)
	before, err := assembly.Assemble("", "", "scope", workspace, assemblyConfig(selected))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "settings.json"), []byte(`{"env":{"CHANGED":"true"},"language":"changed"}`), 0600); err != nil {
		t.Fatal(err)
	}
	selectedAgain, err := a.options(options{nativeProfile: base}, "owner", false, true)
	if err != nil {
		t.Fatal("clean Agent read changed base settings", err)
	}
	after, err := assembly.Assemble("", "", "scope", workspace, assemblyConfig(selectedAgain))
	if err != nil {
		t.Fatal(err)
	}
	if before.Fingerprint != after.Fingerprint || selected.nativeProfile != selectedAgain.nativeProfile {
		t.Fatal("ambient base settings changed clean Agent runtime compatibility")
	}
	if err := os.Remove(filepath.Join(base, "settings.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "settings.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := a.options(options{nativeProfile: base}, "owner", false, true); err != nil {
		t.Fatal("unrelated malformed base settings blocked clean Agent", err)
	}
}

func TestAgentNativeSkillsRootRelativeResolutionAndValidation(t *testing.T) {
	directory := canonicalTemp(t)
	root := filepath.Join(directory, "operations")
	path := filepath.Join(root, "skills", "one", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	catalogPath := filepath.Join(directory, "agents.json")
	writeBotJSON(t, catalogPath, map[string]any{"default_agent": "ops", "agents": []map[string]any{{"id": "ops", "name": "Operations", "native_skills_root": "operations", "native_skills": []string{"one"}}}})
	catalog, err := loadAgents(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Agents[0].NativeSkillsRoot != root {
		t.Fatal("relative native skills root was not resolved from the catalog", catalog.Agents[0].NativeSkillsRoot)
	}
	writeBotJSON(t, catalogPath, map[string]any{"default_agent": "ops", "agents": []map[string]any{{"id": "ops", "name": "Operations", "native_skills_root": "operations", "native_skills": []string{}}}})
	if _, err := loadAgents(catalogPath); err == nil || !strings.Contains(err.Error(), "explicit nonempty native_skills") {
		t.Fatal("native skills root without an explicit selection was accepted", err)
	}
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	writeBotJSON(t, catalogPath, map[string]any{"default_agent": "ops", "agents": []map[string]any{{"id": "ops", "name": "Operations", "native_skills_root": "operations", "native_skills": []string{"one"}}}})
	if _, err := loadAgents(catalogPath); err == nil || !strings.Contains(err.Error(), "deny group/other access") {
		t.Fatal("untrusted native skills root was accepted", err)
	}
}

func TestAgentNativeSkillsRootRejectsUnknownSkill(t *testing.T) {
	base := canonicalTemp(t)
	source := nativeSkillsRootFixture(t, map[string]string{"known": "known"})
	a := agentDefinition{ID: "ops", Name: "Operations", NativeSkillsRoot: source, NativeSkills: []string{"missing"}}
	if _, err := a.options(options{nativeProfile: base}, "owner", false, false); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatal("unknown skill in explicit root was accepted", err)
	}
}

func TestAgentNativeSkillsRootsAreIsolatedAndContentAffectsRuntimeFingerprint(t *testing.T) {
	base := nativeSkillsRootFixture(t, map[string]string{"shared": "legacy-global"})
	firstRoot := nativeSkillsRootFixture(t, map[string]string{"shared": "first-source"})
	secondRoot := nativeSkillsRootFixture(t, map[string]string{"shared": "second-source"})
	firstAgent := agentDefinition{ID: "first", Name: "First", NativeSkillsRoot: firstRoot, NativeSkills: []string{"shared"}}
	secondAgent := agentDefinition{ID: "second", Name: "Second", NativeSkillsRoot: secondRoot, NativeSkills: []string{"shared"}}
	baseOptions := options{nativeProfile: base}
	first, err := firstAgent.options(baseOptions, "owner", false, true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := secondAgent.options(baseOptions, "owner", false, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		o    options
		want string
	}{{first, "first-source"}, {second, "second-source"}} {
		body, err := os.ReadFile(filepath.Join(item.o.nativeProfile, "skills", "shared", "SKILL.md"))
		if err != nil || string(body) != item.want {
			t.Fatal("Agent skill sources crossed", string(body), item.want, err)
		}
	}
	if first.nativeSkillsFingerprint == second.nativeSkillsFingerprint || first.nativeProfile == second.nativeProfile {
		t.Fatal("different Agent skill sources shared fingerprint or profile")
	}
	workspace := canonicalTemp(t)
	before, err := assembly.Assemble("", "", "scope", workspace, assemblyConfig(first))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(firstRoot, "skills", "shared", "SKILL.md"), []byte("first-source-updated"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := firstAgent.options(baseOptions, "owner", false, false)
	if err != nil {
		t.Fatal(err)
	}
	after, err := assembly.Assemble("", "", "scope", workspace, assemblyConfig(changed))
	if err != nil {
		t.Fatal(err)
	}
	if before.Fingerprint == after.Fingerprint || first.nativeProfile != changed.nativeProfile {
		t.Fatal("skill content change did not rotate runtime compatibility without changing profile identity")
	}
}
