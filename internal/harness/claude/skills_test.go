//go:build unix

package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"agent-host/internal/assembly"
	"agent-host/internal/harness"
	"agent-host/internal/state"
)

func nativeSkillFixture(t *testing.T) assembly.SkillPackage {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	package1, err := assembly.BuildSkillPackage(root, "run-skills", []state.Memory{{ID: "memory-1", Kind: "procedure", Revision: 1, Text: "Inspect the token and confirm the connection.", Scope: "project:ops", Status: "active", SourceType: "reviewed_experience", SourceRunID: "run-1", Conditions: "When an expired token was observed.", Steps: []string{"Inspect status.", "Verify the new connection."}, EvidenceIDs: []string{"tool-1"}}}, assembly.SkillIdentity{SourceIdentity: "channel:dingtalk", AgentID: state.DefaultAgentID, AudienceID: state.OwnerAudienceID, Scopes: []string{"project:ops"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return package1
}

func TestClaudeMountsExactInstructionOnlySkillSnapshot(t *testing.T) {
	adapter, request := fixtureAdapter(t, "normal")
	package1 := nativeSkillFixture(t)
	request.SkillPluginDir, request.SkillPluginDigest = package1.Path, package1.Digest
	capturePath := filepath.Join(t.TempDir(), "capture.json")
	t.Setenv("AGENT_HOST_FIXTURE_REQUEST", capturePath)
	var observations []harness.Observation
	result, err := adapter.Run(t.Context(), request, func(o harness.Observation) error { observations = append(observations, o); return nil })
	if err != nil || result.Status != "completed" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	raw, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Args []string `json:"args"`
	}
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	index := slices.Index(capture.Args, "--plugin-dir")
	if index < 0 || capture.Args[index+1] != package1.Path {
		t.Fatalf("plugin flag missing: %v", capture.Args)
	}
	if slices.Contains(capture.Args, "--add-dir") {
		t.Fatal("skill mount expanded filesystem grant")
	}
	if len(observations) == 0 || observations[0].Type != "skills_provided" {
		t.Fatalf("provided receipt missing: %+v", observations)
	}
	for _, toolsDisabled := range []bool{true, false} {
		copy := request
		copy.ToolsDisabled = toolsDisabled
		copy.Extraction = !toolsDisabled
		_, err := arguments(copy)
		if err == nil {
			t.Fatal("tool-free/extraction Skill mount accepted")
		}
	}
}

func TestClaudeRejectsModifiedOrExtendedSkillPackage(t *testing.T) {
	for _, mutation := range []string{"digest", "body", "hook", "symlink", "manifest"} {
		t.Run(mutation, func(t *testing.T) {
			package1 := nativeSkillFixture(t)
			digest := package1.Digest
			switch mutation {
			case "digest":
				digest = strings.Repeat("0", 64)
			case "body":
				if err := os.WriteFile(filepath.Join(package1.Path, "skills", package1.Manifest.Sources[0].Name, "SKILL.md"), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "hook":
				if err := os.WriteFile(filepath.Join(package1.Path, "hooks.json"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("manifest.json", filepath.Join(package1.Path, "reference")); err != nil {
					t.Fatal(err)
				}
			case "manifest":
				if err := os.WriteFile(filepath.Join(package1.Path, "manifest.json"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := validateSkillPlugin(package1.Path, digest); err == nil {
				t.Fatalf("mutation %s accepted", mutation)
			}
		})
	}
}

func TestClaudeUnsupportedSkillMountFailsBeforeBusinessExecution(t *testing.T) {
	adapter, request := fixtureAdapter(t, "skills_unsupported")
	package1 := nativeSkillFixture(t)
	request.SkillPluginDir, request.SkillPluginDigest = package1.Path, package1.Digest
	capture := filepath.Join(t.TempDir(), "capture.json")
	t.Setenv("AGENT_HOST_FIXTURE_REQUEST", capture)
	result, err := adapter.Run(t.Context(), request, nil)
	if err == nil || result.Status != "failed" || !strings.Contains(err.Error(), "does not advertise") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatal("business execution started after failed capability probe")
	}
}
