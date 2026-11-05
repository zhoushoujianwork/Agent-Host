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

func TestNativeSkillSelectionRejectsNestedOffload(t *testing.T) {
	base := nativeSkillsRootFixture(t, map[string]string{
		"offload": "---\nname: offload\n---\nDelegate to a worker.",
		"renamed": "---\nname: offload\n---\nDelegate to a worker.",
		"jms-ssh": "---\nname: jms-ssh\n---\nRun the business check directly.",
	})
	for _, name := range []string{"offload", "renamed"} {
		if err := validateNativeSkillSelection(base, []string{name}); err == nil || !strings.Contains(err.Error(), "Offload delegation") {
			t.Fatal("delegation Skill was admitted", name, err)
		}
	}
	if err := validateNativeSkillSelection(base, []string{"jms-ssh"}); err != nil {
		t.Fatal("ordinary business Skill rejected", err)
	}
}

func TestAgentProfileNilSelectionUsesEmptySettingsAndNoAmbientSkills(t *testing.T) {
	base := t.TempDir()
	destination := filepath.Join(base, "instances", "assistant", "audiences", "owner")
	skill := filepath.Join(base, "skills", "example", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("example"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "settings.json"), []byte(`{"ambient":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepareSelectedAgentProfile(base, destination, nil); err != nil {
		t.Fatal(err)
	}
	settings, err := os.ReadFile(filepath.Join(destination, "settings.json"))
	if err != nil || string(settings) != "{}\n" {
		t.Fatalf("controlled settings=%q err=%v", settings, err)
	}
	entries, err := os.ReadDir(filepath.Join(destination, "skills"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("nil selection inherited ambient skills: %+v %v", entries, err)
	}
}

func TestEmptySelectionReplacesOldInheritedSkillLink(t *testing.T) {
	base := t.TempDir()
	destination := t.TempDir()
	if err := os.Chmod(destination, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "skills"), 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(destination, "skills")
	if err := os.Symlink(filepath.Join(base, "skills"), target); err != nil {
		t.Fatal(err)
	}
	if err := prepareAgentProfile(base, destination); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(target)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("old inherited Skill link was reused", info, err)
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
	if first.nativeSkillsFingerprint == "" {
		t.Fatal("explicit legacy-source selection lacks a content fingerprint")
	}
	if settings, err := os.ReadFile(filepath.Join(first.nativeProfile, "settings.json")); err != nil || string(settings) != "{}\n" {
		t.Fatal("routed Agent inherited native settings", string(settings), err)
	}
	if _, err := os.Stat(filepath.Join(first.nativeProfile, "skills", "two")); !os.IsNotExist(err) {
		t.Fatal("unselected skill mounted")
	}
	beforeFingerprint := first.nativeSkillsFingerprint
	resource := filepath.Join(base, "skills", "one", "references", "runbook.md")
	if err := os.MkdirAll(filepath.Dir(resource), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resource, []byte("bounded runbook"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := a.options(o, "owner", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if changed.nativeSkillsFingerprint == beforeFingerprint {
		t.Fatal("legacy explicit Skill resource content was not fingerprinted")
	}
	if body, err := os.ReadFile(filepath.Join(first.nativeProfile, "skills", "one", "references", "runbook.md")); err != nil || string(body) != "bounded runbook" {
		t.Fatal("nested selected Skill resource was stripped", string(body), err)
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
	if _, err := loadAgents(catalogPath); err != nil {
		t.Fatal("explicit root with zero selected Skills was rejected", err)
	}
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	writeBotJSON(t, catalogPath, map[string]any{"default_agent": "ops", "agents": []map[string]any{{"id": "ops", "name": "Operations", "native_skills_root": "operations", "native_skills": []string{"one"}}}})
	if _, err := loadAgents(catalogPath); err == nil || !strings.Contains(err.Error(), "deny group/other access") {
		t.Fatal("untrusted native skills root was accepted", err)
	}
}

func TestDefaultOwnerOmittedNullAndEmptyNativeSkillsSelectZero(t *testing.T) {
	base := nativeSkillsRootFixture(t, map[string]string{"ambient": "must-not-mount"})
	if err := os.WriteFile(filepath.Join(base, "settings.json"), []byte(`{"ambient":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"", `,"native_skills":null`, `,"native_skills":[]`} {
		t.Run(field, func(t *testing.T) {
			catalogPath := filepath.Join(t.TempDir(), "agents.json")
			raw := `{"default_agent":"default","agents":[{"id":"default","name":"Default"` + field + `}]}`
			if err := os.WriteFile(catalogPath, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			catalog, err := loadAgents(catalogPath)
			if err != nil {
				t.Fatal(err)
			}
			selected, err := catalog.Agents[0].options(options{nativeProfile: base}, "owner", false, true)
			if err != nil {
				t.Fatal(err)
			}
			if selected.nativeProfile == base || selected.nativeSkillsFingerprint == "" {
				t.Fatal("default owner reused ambient profile or lacked strict fingerprint")
			}
			entries, err := os.ReadDir(filepath.Join(selected.nativeProfile, "skills"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("zero selection mounted ambient skills: %+v %v", entries, err)
			}
			settings, err := os.ReadFile(filepath.Join(selected.nativeProfile, "settings.json"))
			if err != nil || string(settings) != "{}\n" {
				t.Fatalf("zero selection inherited ambient settings: %q %v", settings, err)
			}
		})
	}
	if got, err := nativeSkillSelectionFingerprint(base, nil); err != nil {
		t.Fatal(err)
	} else if other, err := nativeSkillSelectionFingerprint(t.TempDir(), []string{}); err != nil || got != other {
		t.Fatal("empty selection fingerprint is not stable", got, other, err)
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
