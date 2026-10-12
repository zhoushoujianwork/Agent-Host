//go:build unix

package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"agent-host/internal/harness"
)

func TestNativeCwdMemoryPolicyAndLegacyProjectInstructions(t *testing.T) {
	adapter, request := fixtureAdapter(t, "success")
	request.Workspace, _ = filepath.EvalSymlinks(request.Workspace)
	t.Setenv("AGENT_HOST_FIXTURE_VERSION", "2.1.223 (Claude Code)")
	t.Setenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY", "0")
	path := filepath.Join(t.TempDir(), "request.json")
	t.Setenv("AGENT_HOST_FIXTURE_REQUEST", path)
	rules := filepath.Join(request.Workspace, "AGENTS.md")
	if err := os.WriteFile(rules, []byte("Project marker: ORCHID. Run the project tests."), 0600); err != nil {
		t.Fatal(err)
	}
	request.AllowedTools = []string{"Read", "Bash(go test *)"}
	var seen []harness.Observation
	result, err := adapter.Run(context.Background(), request, func(o harness.Observation) error { seen = append(seen, o); return nil })
	if err != nil || result.Status != "completed" {
		t.Fatalf("%+v %v", result, err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Args   []string `json:"args"`
		Cwd    string   `json:"cwd"`
		Memory string   `json:"auto_memory_disabled"`
	}
	if err = json.Unmarshal(body, &capture); err != nil {
		t.Fatal(err)
	}
	if capture.Cwd != request.Workspace || capture.Memory != "1" || os.Getenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY") != "0" {
		t.Fatalf("cwd/memory policy: %+v", capture)
	}
	index := slices.Index(capture.Args, "--append-system-prompt")
	if index < 0 || !strings.Contains(capture.Args[index+1], rules) || !strings.Contains(capture.Args[index+1], "ORCHID") || slices.Contains(capture.Args, "--system-prompt") {
		t.Fatalf("project instructions: %v", capture.Args)
	}
	if len(seen) == 0 || seen[0].Type != "project_instructions" || !strings.Contains(string(seen[0].Raw), "AGENTS.md") {
		t.Fatalf("instruction provenance: %+v", seen)
	}
	if !slices.Contains(capture.Args, "Bash(go test *)") {
		t.Fatalf("permissions changed: %v", capture.Args)
	}
	if content, err := os.ReadFile(rules); err != nil || !strings.Contains(string(content), "ORCHID") {
		t.Fatal("project instructions were modified")
	}
}

func TestNativeInstructionsOwnSupportedAndClaudeEntryCases(t *testing.T) {
	adapter, request := fixtureAdapter(t, "success")
	request.Workspace, _ = filepath.EvalSymlinks(request.Workspace)
	t.Setenv("AGENT_HOST_FIXTURE_VERSION", "2.1.277 (Claude Code)")
	if err := os.WriteFile(filepath.Join(request.Workspace, "AGENTS.md"), []byte("rules"), 0600); err != nil {
		t.Fatal(err)
	}
	if text, _, err := adapter.projectInstructions(context.Background(), request.Workspace); err != nil || text != "" {
		t.Fatalf("duplicated native AGENTS discovery: %q %v", text, err)
	}
	t.Setenv("AGENT_HOST_FIXTURE_VERSION", "2.1.223 (Claude Code)")
	if err := os.WriteFile(filepath.Join(request.Workspace, "CLAUDE.md"), []byte("@AGENTS.md"), 0600); err != nil {
		t.Fatal(err)
	}
	if text, _, err := adapter.projectInstructions(context.Background(), request.Workspace); err != nil || text != "" {
		t.Fatalf("duplicated CLAUDE entry: %q %v", text, err)
	}
}

func TestAncestorClaudeEntryDoesNotHideLegacyProjectAgents(t *testing.T) {
	adapter, request := fixtureAdapter(t, "success")
	t.Setenv("AGENT_HOST_FIXTURE_VERSION", "2.1.223 (Claude Code)")
	parent := t.TempDir()
	request.Workspace = filepath.Join(parent, "project")
	if err := os.Mkdir(request.Workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "CLAUDE.md"), []byte("Shared parent convention."), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(request.Workspace, "AGENTS.md"), []byte("Project marker: IRIS."), 0600); err != nil {
		t.Fatal(err)
	}
	text, _, err := adapter.projectInstructions(context.Background(), request.Workspace)
	if err != nil || !strings.Contains(text, "IRIS") {
		t.Fatalf("ancestor suppressed project instructions: %q %v", text, err)
	}
}
